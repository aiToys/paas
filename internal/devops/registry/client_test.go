package registry

import "testing"

// TestValidRepoName registry v2 仓库名语法校验（边界审计 R10-#7：Tags/headDigest 的
// 前置校验，注入防护——非法名走 HTTP 前拒绝）。
func TestValidRepoName(t *testing.T) {
	valid := []string{
		"app-cs", "a", "app/cs", "paas/app/frontend", "app__cs", "a.b.c", "a--b", "0abc",
	}
	for _, s := range valid {
		if !validRepoName(s) {
			t.Errorf("%q 应合法", s)
		}
	}
	invalid := []string{
		"", "App", "-app", "app-", ".app", "app/", "/app", "app//cs", "app cs", "app?x=1",
		"app%2Fcs", "../etc/passwd", "app cs/x",
	}
	for _, s := range invalid {
		if validRepoName(s) {
			t.Errorf("%q 应非法", s)
		}
	}
}

// TestValidTag tag 名语法校验（长度/字符集/首字符）。
func TestValidTag(t *testing.T) {
	valid := []string{"v1.0.0", "latest", "0", "sha256abc", "v1.0.0-rc.1", "Main", "_lead"}
	for _, s := range valid {
		if !validTag(s) {
			t.Errorf("%q 应合法", s)
		}
	}
	invalid := []string{
		"", ".hidden", "-lead", "tag with space", "tag?x", "tag%x",
	}
	// 超长（129 字符 > 首字符 + 127 上限）
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	invalid = append(invalid, string(long))
	for _, s := range invalid {
		if validTag(s) {
			t.Errorf("%q 应非法", s)
		}
	}
}
