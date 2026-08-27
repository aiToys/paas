package names

import "testing"

func TestValidDNS1035(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"feature-x", true},
		{"a", true},
		{"abc123", true},
		{"a-b-c-9", true},
		{"", false},     // 空
		{"-abc", false}, // 首字符 -
		{"9abc", false}, // 首字符数字
		{"Abc", false},  // 大写
		{"ab_c", false}, // 下划线
		{"ab.c", false}, // 点（分段名不合法）
		{"ab/", false},  // 斜杠（集成分支名清洗前的原始形态）
		{"abc-", false}, // 尾字符 -
	}
	for _, c := range cases {
		if got := ValidDNS1035(c.name); got != c.want {
			t.Errorf("ValidDNS1035(%q) = %v, want %v", c.name, got, c.want)
		}
	}
	// 超长（64 字符合法字符但超限）
	long := ""
	for i := 0; i < 64; i++ {
		long += "a"
	}
	if ValidDNS1035(long) {
		t.Error("64 字符应超限返回 false")
	}
	// 63 字符边界合法
	if !ValidDNS1035(long[:63]) {
		t.Error("63 字符应合法")
	}
}
