package auth

import "errors"

var ErrWeakPassword = errors.New("密码至少 8 位且需同时包含字母和数字")

// ValidatePassword 强密码策略：≥8 + 含字母 + 含数字。
// seed demo 账号（123456）由 seed 直接 bcrypt 写入，不经此校验（demo 门控，生产关闭后无弱密码账号）。
// admin 后台 CreateUser/改密码时强制；API Key 通道不涉及密码。
func ValidatePassword(s string) error {
	if len(s) < 8 {
		return ErrWeakPassword
	}
	hasLetter, hasDigit := false, false
	for _, c := range s {
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			hasLetter = true
		case c >= '0' && c <= '9':
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return ErrWeakPassword
	}
	return nil
}
