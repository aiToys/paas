// Package names 提供平台级命名校验共享 helper（K8s DNS-1035 等命名规则单一真源）。
// 抽自 devops dns1035 / dataplane dns1035Name 的公共校验语义，供 lane/workload 等模块复用。
package names

import "regexp"

// dns1035Pattern K8s Service 名（DNS-1035）：小写字母开头，小写字母数字与 -，≤63 字符。
var dns1035Pattern = regexp.MustCompile(`^[a-z]([-a-z0-9]*[a-z0-9])?$`)

// ValidDNS1035 校验 name 是否为合法 DNS-1035 标签（首字母、合法字符集、≤63）。
func ValidDNS1035(name string) bool {
	return len(name) > 0 && len(name) <= 63 && dns1035Pattern.MatchString(name)
}
