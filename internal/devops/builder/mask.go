package builder

import (
	"errors"
	"regexp"
)

// tokenURLRe 匹配嵌入凭证的 HTTPS URL（https://<token>@host），用于构建日志脱敏。
// injectToken 把 Git token 拼成 https://<token>@host 作为 CLONE_URL，git clone 失败时
// 该 URL 写入 stderr -> BuildRun.Log -> GET /api/buildruns/{id} 返给 build:read 权限者。
var tokenURLRe = regexp.MustCompile(`https://[^@\s"']+@`)

// MaskToken 对字符串中的嵌入凭证 HTTPS URL 脱敏（https://<token>@host -> https://***@host）。
// 用于构建日志，防 Git token 经 BuildRun.Log 泄漏。
func MaskToken(s string) string {
	if s == "" {
		return s
	}
	return tokenURLRe.ReplaceAllString(s, "https://***@")
}

// MaskErr 对 error 的 Error() 文本脱敏。用 errors.New 重建（丢失 wrap chain，
// 但 store 仅记日志不判 sentinel，可接受）。
func MaskErr(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(MaskToken(err.Error()))
}
