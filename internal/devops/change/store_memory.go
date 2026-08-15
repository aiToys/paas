package change

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/pkg/tenant"
)

// MemoryStore 变更 + 批次的进程内实现。供 cmd/core 在 PAAS_DB_URL 为空时装配。
// 所有方法强制按 ctx 租户过滤；跨租户访问返 NotFound 不泄漏存在性。
type MemoryStore struct {
	mu      sync.RWMutex
	changes map[string]Change
	batches map[string]IntegrationBatch
}

// NewMemoryStore 创建空 store（不 seed 演示数据）。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		changes: map[string]Change{},
		batches: map[string]IntegrationBatch{},
	}
}

// tenantOrErr 从 ctx 取租户，缺失返 ErrNoTenant。
func tenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return "", ErrNoTenant
	}
	return tid, nil
}

// randID 生成带前缀的短 ID（crypto/rand 8 字节 hex，与 pipeline webhook token 同源思路）。
func randID(prefix string) string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error()) // 无熵优于弱 ID
	}
	return prefix + "-" + hex.EncodeToString(b)
}

func cloneStrs(ss []string) []string {
	if len(ss) == 0 {
		return nil
	}
	out := make([]string, len(ss))
	copy(out, ss)
	return out
}

func cloneChange(c Change) Change { return c } // 纯值类型，无引用字段

func cloneBatch(b IntegrationBatch) IntegrationBatch {
	b.ChangeIDs = cloneStrs(b.ChangeIDs)
	b.ReleaseIDs = cloneStrs(b.ReleaseIDs)
	return b
}

// ---------- Change ----------

func (s *MemoryStore) ListChanges(ctx context.Context, appID, status string) ([]Change, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Change, 0)
	for _, c := range s.changes {
		if c.TenantID != tid {
			continue
		}
		if appID != "" && c.AppID != appID {
			continue
		}
		if status != "" && c.Status != status {
			continue
		}
		out = append(out, cloneChange(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *MemoryStore) GetChange(ctx context.Context, id string) (Change, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return Change{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	c, ok := s.changes[id]
	if !ok || c.TenantID != tid {
		return Change{}, ErrChangeNotFound
	}
	return cloneChange(c), nil
}

func (s *MemoryStore) CreateChange(ctx context.Context, in Change) (Change, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return Change{}, err
	}
	if err := in.Validate(); err != nil {
		return Change{}, err
	}
	in.ID = randID("chg")
	in.TenantID = tid // ctx 为准，忽略请求体
	in.Status = ChangeOpen
	now := time.Now().UTC()
	in.CreatedAt, in.UpdatedAt = now, now

	s.mu.Lock()
	defer s.mu.Unlock()
	// 同 (tenant, repo) 分支唯一（遍历查重）
	for _, c := range s.changes {
		if c.TenantID == tid && c.RepoID == in.RepoID && c.Branch == in.Branch {
			return Change{}, ErrChangeExists
		}
	}
	s.changes[in.ID] = in
	return cloneChange(in), nil
}

func (s *MemoryStore) UpdateChange(ctx context.Context, in Change) (Change, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return Change{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.changes[in.ID]
	if !ok || cur.TenantID != tid {
		return Change{}, ErrChangeNotFound
	}
	in.TenantID = tid
	in.CreatedAt = cur.CreatedAt
	in.UpdatedAt = time.Now().UTC()
	s.changes[in.ID] = in
	return cloneChange(in), nil
}

// ---------- IntegrationBatch ----------

func (s *MemoryStore) ListBatches(ctx context.Context, appID, status string) ([]IntegrationBatch, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]IntegrationBatch, 0)
	for _, b := range s.batches {
		if b.TenantID != tid {
			continue
		}
		if appID != "" && b.AppID != appID {
			continue
		}
		if status != "" && b.Status != status {
			continue
		}
		out = append(out, cloneBatch(b))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *MemoryStore) GetBatch(ctx context.Context, id string) (IntegrationBatch, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return IntegrationBatch{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.batches[id]
	if !ok || b.TenantID != tid {
		return IntegrationBatch{}, ErrBatchNotFound
	}
	return cloneBatch(b), nil
}

func (s *MemoryStore) CreateBatch(ctx context.Context, in IntegrationBatch) (IntegrationBatch, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return IntegrationBatch{}, err
	}
	if err := in.ValidateBatch(); err != nil {
		return IntegrationBatch{}, err
	}
	in.ID = randID("batch")
	in.TenantID = tid // ctx 为准，忽略请求体
	in.Status = BatchCollecting
	in.CreatedAt = time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.batches {
		// 唯一键 (tenant, branch)——与 PG idx_batches_tenant_branch 对齐（防两后端语义漂移）
		if b.TenantID == tid && b.Branch == in.Branch {
			return IntegrationBatch{}, ErrBatchExists
		}
	}
	s.batches[in.ID] = in
	return cloneBatch(in), nil
}

func (s *MemoryStore) UpdateBatch(ctx context.Context, in IntegrationBatch) (IntegrationBatch, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return IntegrationBatch{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.batches[in.ID]
	if !ok || cur.TenantID != tid {
		return IntegrationBatch{}, ErrBatchNotFound
	}
	in.TenantID = tid
	in.CreatedAt = cur.CreatedAt
	s.batches[in.ID] = in
	return cloneBatch(in), nil
}
