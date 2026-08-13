package real

import (
	"regexp"
	"strings"
)

// appPodRegex 把应用的工作负载 ID 列表转为 PromQL/LogQL pod 名正则：wl-<id1>-.*|wl-<id2>-.*。
// 工作负载 K8s Deployment 名 = 工作负载 ID（wl-<id>），Pod = <deploy>-<rsHash>-<podHash>，
// 故 `pod=~"wl-<id>-.*"` 匹配该工作负载全部 Pod。多 ID 用 | 合并。
//
// 工作负载 ID 形如 wl-1786152991462640049（仅数字/字母/连字符，安全），仍 escape 以防注入。
func appPodRegex(ids []string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		parts = append(parts, regexp.QuoteMeta(id)+"-.*")
	}
	if len(parts) == 0 {
		return "zxnone__" // 无 ID：匹配不到任何 pod（占位，避免空正则）
	}
	// 形如 ^wl-id1-.*|wl-id2-.*$（PromQL/Loki regex 全锚定，无需显式 ^$）。
	return strings.Join(parts, "|")
}

// lokiPodSelector 返回 LogQL pod selector 的 stream 匹配值（与 appPodRegex 同语义）。
// Loki stream label `pod` 即 Pod 名，故复用 appPodRegex。
func lokiPodSelector(ids []string) string {
	return appPodRegex(ids)
}
