package configcenter

import (
	"fmt"
	"sort"
)

// fmtHexHash uint32 → 8 位十六进制串。
func fmtHexHash(h uint32) string {
	return fmt.Sprintf("%08x", h)
}

// MergeSnapshot 基线快照 + 泳道覆盖两层 merge（纯函数，可测）。
// 覆盖项覆盖同 key 基线值、新 key 追加；base 不被修改（深拷隔离）。
// 发现解析语义（store 层）：env 精确 → env='' 回退 → 无；lane 同规则取覆盖。
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
