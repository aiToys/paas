package gateway

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/core/identity"
)

// credentialDeny 把「凭证有效但被平台停用」映射为 403。
// Key 写错仍是 401；冻结租户 / 到期 / 吊销是平台主动停用，运维需要能区分这两类。
// 文案不回显租户名或原因，避免凭证持有者探到平台内部处置信息。
func credentialDeny(err error) (int, string, bool) {
	switch {
	case errors.Is(err, identity.ErrTenantFrozen):
		return http.StatusForbidden, "tenant frozen", true
	case errors.Is(err, identity.ErrAPIKeyExpired):
		return http.StatusForbidden, "api key expired", true
	case errors.Is(err, identity.ErrAPIKeyRevoked):
		return http.StatusForbidden, "api key revoked", true
	}
	return 0, "", false
}

// keyUsageTracker 节流回写「最近使用」：每个 Key 每分钟至多一次，避免鉴权热路径每请求写库。
type keyUsageTracker struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (t *keyUsageTracker) shouldTouch(id string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if last, ok := t.last[id]; ok && now.Sub(last) < time.Minute {
		return false
	}
	t.last[id] = now
	// 惰性清理：长时间未再使用的 Key 条目释放，防长期运行内存单调增长。
	if len(t.last) > 4096 {
		for k, v := range t.last {
			if now.Sub(v) > time.Hour {
				delete(t.last, k)
			}
		}
	}
	return true
}

var usageTracker = &keyUsageTracker{last: map[string]time.Time{}}

// touchAPIKeyUsage 节流回写 API Key 最近使用时间。
// 失败静默：使用统计是观测信息，不能改变鉴权结论、也不能让请求失败。
func touchAPIKeyUsage(ctx context.Context, idb identity.Repository, id string) {
	now := time.Now()
	if !usageTracker.shouldTouch(id, now) {
		return
	}
	// 请求可能已被客户端取消，用 WithoutCancel 保住这次回写。
	_ = idb.TouchAPIKeyUsage(context.WithoutCancel(ctx), id, now)
}
