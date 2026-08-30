package configcenter

import (
	"fmt"
	"sort"
	"strconv"
)

// fmtHexHash uint32 → 8 位十六进制串。
func fmtHexHash(h uint32) string {
	return fmt.Sprintf("%08x", h)
}

// MergeSnapshot 基线快照 + 泳道覆盖两层 merge（纯函数，可测）。
// 覆盖项覆盖同 key 基线值、新 key 追加；base 不被修改（深拷隔离）。
// 发现解析语义（store 层）：env 精确 → env=” 回退 → 无；lane 同规则取覆盖。
func MergeSnapshot(base map[string]string, overrides []LaneOverride) map[string]string {
	out := make(map[string]string, len(base)+len(overrides))
	for k, v := range base {
		out[k] = v
	}
	for _, o := range overrides {
		out[o.Key] = o.Value
	}
	return out
}

// OverrideHash 泳道覆盖指纹：FNV-1a(排序后 key=value 串)。
// 无覆盖（nil/空）返回空串——发现响应省略 overrideHash 字段（向后兼容：旧客户端无感知）。
// 顺序无关（排序后哈希），覆盖任一 key/value 变化则 hash 变化（客户端据此热更新）。
func OverrideHash(overrides []LaneOverride) string {
	if len(overrides) == 0 {
		return ""
	}
	keys := make([]string, 0, len(overrides))
	byKey := make(map[string]string, len(overrides))
	for _, o := range overrides {
		keys = append(keys, o.Key)
		byKey[o.Key] = o.Value
	}
	sort.Strings(keys)
	var buf []byte
	for i, k := range keys {
		if i > 0 {
			buf = append(buf, ';')
		}
		buf = append(buf, k...)
		buf = append(buf, '=')
		buf = append(buf, byKey[k]...)
	}
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for _, b := range buf {
		h ^= uint32(b)
		h *= prime32
	}
	return fmtHexHash(h)
}

// SharedLayer 发现 merge 的 shared 引用层（快照 + 来源标识，供指纹计算）。
type SharedLayer struct {
	NSID     string
	Version  int // shared ns 当前 active 版本（未发布 0，层贡献空集）
	Snapshot map[string]string
}

// MergeSnapshot3 三层 merge：shared 引用（按引用顺序铺垫，后者覆盖前者）→
// app×env 基线 → lane 覆盖，右者胜。应用自身 key 压制 shared（逃生门），
// lane 覆盖最强（灰度验证语义）。各层均不被修改（拷贝隔离）。
func MergeSnapshot3(shared []SharedLayer, base map[string]string, overrides []LaneOverride) map[string]string {
	out := make(map[string]string, len(base)+len(overrides)+8)
	for _, l := range shared {
		for k, v := range l.Snapshot {
			out[k] = v
		}
	}
	for k, v := range base {
		out[k] = v
	}
	for _, o := range overrides {
		out[o.Key] = o.Value
	}
	return out
}

// SharedHash shared 引用层指纹：FNV-1a(排序后 "nsID:version" 串)。
// 无引用返回空串（发现响应省略 sharedHash 字段，向后兼容）；shared 重新发布 →
// version 变 → hash 变（客户端据此热替换），但应用自身 version 不受污染。
func SharedHash(shared []SharedLayer) string {
	if len(shared) == 0 {
		return ""
	}
	parts := make([]string, 0, len(shared))
	for _, l := range shared {
		parts = append(parts, l.NSID+":"+strconv.Itoa(l.Version))
	}
	sort.Strings(parts)
	var buf []byte
	for i, p := range parts {
		if i > 0 {
			buf = append(buf, ';')
		}
		buf = append(buf, p...)
	}
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	h := uint32(offset32)
	for _, b := range buf {
		h ^= uint32(b)
		h *= prime32
	}
	return fmtHexHash(h)
}
