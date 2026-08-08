package tenant

import "strings"

// 数据面 namespace 命名约定：用户工作负载与数据服务落在按租户派生的 ns（paas-<tenantID>），
// 与控制面 ns（core/postgres/gitea/observability/registry，通常是 helm release ns）分离，
// 实现控制面/数据面 + 租户级 K8s namespace 隔离（NetworkPolicy/ResourceQuota/可见性）。
const NamespacePrefix = "paas-"

// Namespace 返回租户对应的数据面 K8s namespace 名（paas-<tenantID>）。
// tenantID 经 SanitizeName 清洗为合法 K8s namespace 名。空 tenantID 返 "paas-x"（不应发生，
// 调用方应保证 tenant 非空；fail-safe 避免空串作 ns 名）。
func Namespace(tid string) string {
	return NamespacePrefix + SanitizeName(tid)
}

// SanitizeName 把任意字符串清洗为合法 K8s 资源名（namespace 名 / label 值）。
// K8s 规范：小写字母/数字/'-'，首尾非 '-'，≤63 字符。
// 规则：转小写 → 非 [-a-z0-9] 替 '-' → 去首尾 '-' → 截断 63 → 再去边界 '-' → 空返 "x"。
func SanitizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	if out == "" {
		return "x"
	}
	return out
}
