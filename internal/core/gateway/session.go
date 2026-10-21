package gateway

import (
	"context"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/core/identity"
)

// sessionCacheTTL 是用户会话代次的缓存窗口：撤销会话后的最坏生效延迟。
// 30s 是「不每请求查库」与「撤销近实时」的折中；续期（refresh）路径不受缓存影响，立即拒绝。
const sessionCacheTTL = 30 * time.Second

// sessionGate 校验 JWT 携带的会话代次（claims.ver）是否仍与用户当前代次一致。
// 撤销会话/改密/停用/改角色都会 +1，旧 token 随即失配。
//
// claims.ver 为 0 表示存量 token（本机制上线前签发，omitempty 不写该字段），
// 不做比对——等待自然过期（access ≤15min）是本机制明确的取舍，不做全量踢出。
type sessionGate struct {
	idb identity.Repository
	now func() time.Time

	mu    sync.Mutex
	cache map[string]sessionEntry // tenantID + "/" + userID -> 代次
}

type sessionEntry struct {
	ver int
	err error
	exp time.Time
}

func newSessionGate(idb identity.Repository) *sessionGate {
	return &sessionGate{idb: idb, now: time.Now, cache: map[string]sessionEntry{}}
}

// valid 判断 claims 中的代次是否仍有效。查询失败 fail-closed（拒绝），
// 与平台其余准入判断（冻结/租户缺失）一致，不把「查不到」当「正常」。
func (g *sessionGate) valid(ctx context.Context, tenantID, userID string, claimed int) bool {
	if claimed == 0 {
		return true
	}
	key := tenantID + "/" + userID
	g.mu.Lock()
	if e, ok := g.cache[key]; ok && g.now().Before(e.exp) {
		g.mu.Unlock()
		return e.err == nil && e.ver == claimed
	}
	g.mu.Unlock()

	ver, err := g.load(ctx, tenantID, userID)
	g.mu.Lock()
	g.cache[key] = sessionEntry{ver: ver, err: err, exp: g.now().Add(sessionCacheTTL)}
	g.mu.Unlock()
	return err == nil && ver == claimed
}

func (g *sessionGate) load(ctx context.Context, tenantID, userID string) (int, error) {
	u, err := g.idb.GetUser(ctx, tenantID, userID)
	if err != nil {
		return 0, err
	}
	return u.SessionVersion, nil
}
