package lane

import (
	"errors"
	"testing"
)

func TestValidateName(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"feature-x", nil},
		{"a", nil},
		{"", ErrLaneNameInvalid},
		{"9abc", ErrLaneNameInvalid},  // 首字符数字
		{"ab_c", ErrLaneNameInvalid},  // 下划线
		{"ab/", ErrLaneNameInvalid},   // 斜杠（分支名原始形态）
		{"Abc", ErrLaneNameInvalid},   // 大写
	}
	for _, c := range cases {
		if err := ValidateName(c.name); !errors.Is(err, c.err) {
			t.Errorf("ValidateName(%q) = %v, want %v", c.name, err, c.err)
		}
	}
	// 超长 64 字符
	long := ""
	for i := 0; i < 64; i++ {
		long += "a"
	}
	if err := ValidateName(long); !errors.Is(err, ErrLaneNameInvalid) {
		t.Errorf("ValidateName(64 字符) = %v, want ErrLaneNameInvalid", err)
	}
}

func TestValidate(t *testing.T) {
	base := func() Lane {
		return Lane{EnvID: "env-1", Name: "feature-x", Mode: ModeStandard, Weight: 50}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("合法 Lane 不应报错: %v", err)
	}
	// EnvID 必填（归属环境是泳道前提）
	l := base()
	l.EnvID = ""
	if err := l.Validate(); err == nil {
		t.Error("EnvID 空应报错")
	}
	// Mode 枚举
	l = base()
	l.Mode = "bogus"
	if err := l.Validate(); err == nil {
		t.Error("非法 Mode 应报错")
	}
	// Weight 边界 0/100 合法、超界非法
	for _, w := range []int{0, 100} {
		l = base()
		l.Weight = w
		if err := l.Validate(); err != nil {
			t.Errorf("Weight=%d 应合法: %v", w, err)
		}
	}
	for _, w := range []int{-1, 101} {
		l = base()
		l.Weight = w
		if err := l.Validate(); err == nil {
			t.Errorf("Weight=%d 应非法", w)
		}
	}
	// Status 枚举
	l = base()
	l.Status = "bogus"
	if err := l.Validate(); err == nil {
		t.Error("非法 Status 应报错")
	}
	// closed 状态合法
	l = base()
	l.Status = StatusClosed
	if err := l.Validate(); err != nil {
		t.Errorf("Status=closed 应合法: %v", err)
	}
}
