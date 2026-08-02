package auth

import "testing"

func TestValidatePassword(t *testing.T) {
	bad := []string{
		"", "123", "abcdefg", "onlyletters", "12345678", "短密码1",
		"allletters", "99999999", // 纯字母或纯数字
	}
	for _, p := range bad {
		if err := ValidatePassword(p); err == nil {
			t.Errorf("弱密码应拒: %q", p)
		}
	}
	good := []string{"Aa123456", "pass1word", "x1y2z3w4"}
	for _, p := range good {
		if err := ValidatePassword(p); err != nil {
			t.Errorf("强密码应过 %q: %v", p, err)
		}
	}
}

func TestValidatePassword_ErrIsSentinel(t *testing.T) {
	if err := ValidatePassword("weak"); err != ErrWeakPassword {
		t.Errorf("应返 ErrWeakPassword sentinel，got %v", err)
	}
}
