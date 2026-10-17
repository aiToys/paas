// Package memory —— workload.OperationsRepo 的内存实现（本地 dev / 测试）。
// 与 PG 同语义：受理原子（Restart 同时改 token 与插记录）、幂等键命中返回既有、
// 活跃唯一冲突 ErrOpConflict、终态不可逆。
package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

// OpsStore 是 workload.OperationsRepo 的内存实现。
type OpsStore struct {
	mu  sync.Mutex
	ops []workload.Operation // 按 AcceptedAt 升序 append
	wls *Store
	seq int64
}

// NewOpsStore 创建内存操作仓储（wls 同库共享 workload 状态）。
func NewOpsStore(wls *Store) *OpsStore {
	return &OpsStore{wls: wls}
}

func (s *OpsStore) nextID() string {
	s.seq++
	return fmt.Sprintf("op-%d-%d", time.Now().UnixNano(), s.seq)
}

// CreateRestart 受理滚动重启（锁内「改 token + 插记录」两步原子）。
func (s *OpsStore) CreateRestart(ctx context.Context, w workload.Workload, reason, actor, idemKey string) (workload.Operation, bool, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return workload.Operation{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if op, ok := s.findByIdemLocked(tid, w.ID, idemKey); ok {
		return op, false, nil
	}
	if s.hasActiveLocked(w.ID) {
		return workload.Operation{}, false, workload.ErrOpConflict
	}
	token := fmt.Sprintf("rt-%d", time.Now().UnixNano())
	// 借助 wls 的写接口保持同源校验（锁序：先 OpsStore 后 wls，与所有方法一致防死锁）。
	if err := s.wls.SetRestartToken(tenant.WithTenant(ctx, tid), w.ID, token); err != nil {
		return workload.Operation{}, false, err
	}
	op := workload.Operation{
		ID: s.nextID(), TenantID: tid, WorkloadID: w.ID, Kind: workload.OpKindRestart,
		Status: workload.OpStatusAccepted, Reason: reason, Actor: actor, IdempotencyKey: idemKey,
		TargetToken: token, Replicas: w.Replicas,
		AcceptedAt: time.Now(), UpdatedAt: time.Now(),
	}
	s.ops = append(s.ops, op)
	return op, true, nil
}

// CreateRecreate 受理实例重建。
func (s *OpsStore) CreateRecreate(ctx context.Context, w workload.Workload, podName, podUID, reason, actor, idemKey string) (workload.Operation, bool, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return workload.Operation{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if op, ok := s.findByIdemLocked(tid, w.ID, idemKey); ok {
		return op, false, nil
	}
	if s.hasActiveLocked(w.ID) {
		return workload.Operation{}, false, workload.ErrOpConflict
	}
	op := workload.Operation{
		ID: s.nextID(), TenantID: tid, WorkloadID: w.ID, Kind: workload.OpKindRecreate,
		Status: workload.OpStatusAccepted, Reason: reason, Actor: actor, IdempotencyKey: idemKey,
		PodUID: podUID, PodName: podName, Replicas: w.Replicas,
		AcceptedAt: time.Now(), UpdatedAt: time.Now(),
	}
	s.ops = append(s.ops, op)
	return op, true, nil
}

func (s *OpsStore) findByIdemLocked(tid, workloadID, idemKey string) (workload.Operation, bool) {
	if idemKey == "" {
		return workload.Operation{}, false
	}
	for _, op := range s.ops {
		if op.TenantID == tid && op.WorkloadID == workloadID && op.IdempotencyKey == idemKey {
			return op, true
		}
	}
	return workload.Operation{}, false
}

func (s *OpsStore) hasActiveLocked(workloadID string) bool {
	for _, st := range workload.OpActiveStates {
		for _, op := range s.ops {
			if op.WorkloadID == workloadID && op.Status == st {
				return true
			}
		}
	}
	return false
}

// Get 按（租户过滤的）workload + operation ID 取操作。
func (s *OpsStore) Get(ctx context.Context, workloadID, operationID string) (workload.Operation, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return workload.Operation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, op := range s.ops {
		if op.TenantID == tid && op.WorkloadID == workloadID && op.ID == operationID {
			return op, nil
		}
	}
	return workload.Operation{}, fmt.Errorf("操作记录不存在: %s", operationID)
}

// ListByWorkload 按时间倒序分页。
func (s *OpsStore) ListByWorkload(ctx context.Context, workloadID string, limit, offset int) ([]workload.Operation, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]workload.Operation, 0)
	for i := len(s.ops) - 1; i >= 0; i-- {
		op := s.ops[i]
		if op.TenantID != tid || op.WorkloadID != workloadID {
			continue
		}
		out = append(out, op)
	}
	if offset < len(out) {
		out = out[offset:]
	} else {
		out = nil
	}
	if len(out) > limit {
		out = out[:limit]
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AcceptedAt.After(out[j].AcceptedAt) })
	return out, nil
}

// Active 返回当前活跃操作（无则零值）。
func (s *OpsStore) Active(ctx context.Context, workloadID string) (workload.Operation, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return workload.Operation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.ops) - 1; i >= 0; i-- {
		op := s.ops[i]
		if op.TenantID == tid && op.WorkloadID == workloadID {
			for _, st := range workload.OpActiveStates {
				if op.Status == st {
					return op, nil
				}
			}
		}
	}
	return workload.Operation{}, nil
}

// ListActive 跨租户列出非终态操作。
func (s *OpsStore) ListActive(ctx context.Context) ([]workload.Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]workload.Operation, 0)
	for _, op := range s.ops {
		for _, st := range workload.OpActiveStates {
			if strings.EqualFold(op.Status, st) {
				out = append(out, op)
				break
			}
		}
	}
	return out, nil
}

// UpdateStatus 推进状态（终态保护：非终态条件更新，0 行幂等忽略）。
func (s *OpsStore) UpdateStatus(ctx context.Context, op workload.Operation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.ops {
		if s.ops[i].ID != op.ID {
			continue
		}
		active := false
		for _, st := range workload.OpActiveStates {
			if s.ops[i].Status == st {
				active = true
				break
			}
		}
		if !active {
			return nil // 终态不可逆（与 PG UpdateStatus 同语义）
		}
		op.TenantID = s.ops[i].TenantID // 以库内为准
		s.ops[i] = op
		return nil
	}
	return nil
}
