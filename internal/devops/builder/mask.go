package builder

import (
	"errors"
	"fmt"
	"regexp"
)

// tokenURLRe 匹配嵌入凭证的 URL（scheme://<user>:<token>@host 或 scheme://<token>@host），
// 用于构建日志脱敏。覆盖 https/http/git 三种 scheme——内置 Gitea 的 CloneURLWithAuth 实际
// 构造 http://paas-bot:<password>@host 形态（集群内 http），仅匹配 https 会漏网（安全审计 I4）。
// injectToken 把 Git token 拼进 CLONE_URL，git clone 失败时该 URL 写入 stderr ->
// BuildRun.Log -> GET /api/buildruns/{id} 返给 build:read 权限者。
var tokenURLRe = regexp.MustCompile(`(https?|git)://[^@\s"']+@`)

// MaskToken 对字符串中的嵌入凭证 URL 脱敏（scheme://<token>@host -> scheme://***@host）。
// 用于构建日志，防 Git token / Gitea 密码经 BuildRun.Log 泄漏。
func MaskToken(s string) string {
	if s == "" {
		return s
	}
	return tokenURLRe.ReplaceAllString(s, "${1}://***@")
}

// MaskErr 对 error 的 Error() 文本脱敏。用 errors.New 重建（丢失 wrap chain，
// 但 store 仅记日志不判 sentinel，可接受）。
func MaskErr(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(MaskToken(err.Error()))
}

// MaxLogBytes 构建日志大小上限（8MB）。恶意/异常构建（如 docker build 刷屏）可产出
// 无界日志，四层不加 cap 会打爆内存与 DB：Pod 日志流 io.Copy、PG Log 列、终端全量
// 返回、SSE 转发（集成审计 I6）。
const MaxLogBytes = 8 << 20

// truncMarker 截断标记（保留头尾各一半：头部错误上下文 + 尾部结论行都在排障时最常看）。
const truncMarker = "\n...[日志超限，已截断 %s]...\n"

// CapLog 把日志截断到 MaxLogBytes：超限时保留头部前半 + 截断标记 + 尾部后半
// （头部=失败根因上下文，尾部=结论行，中间重复刷屏段最无信息量）。
func CapLog(log string) string {
	if len(log) <= MaxLogBytes {
		return log
	}
	half := MaxLogBytes / 2
	dropped := len(log) - MaxLogBytes
	return log[:half] + fmt.Sprintf(truncMarker, humanBytes(dropped)) + log[len(log)-half:]
}

// humanBytes 简易可读字节数（日志截断标记用）。
func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
