// 仓储契约与内存实现（spec §10）。所有读取与变更强制 tenant + owner user——
// 不能仅凭 sessionId/runId 查全局；PG 实现见 pg.go（migration 0060）。
package assistant

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/pkg/tenant"
)

// ErrNotFound 会话/run 不存在或不属于当前租户+用户（统一 404 不泄漏）。
var ErrNotFound = errors.New("not found")

// ErrConflict 状态冲突（CAS 失败/已有活动任务/幂等冲突语义由 service 细分）。
var ErrConflict = errors.New("conflict")
var ErrReplay = errors.New("idempotent replay")

// Repository 助手仓储。
type Repository interface {
	// —— 会话（tenant + owner 强制）——
	CreateSession(ctx context.Context, s Session) error
	GetSession(ctx context.Context, id string) (Session, error)
	ListSessions(ctx context.Context, before time.Time, limit int) ([]Session, error)
	// UpdateSession 更新会话可变字段（target/context/focus/updatedAt；draft→resolved
	// 的上下文落库走这里；ID/TenantID/OwnerUserID 不可变，spec 2026-10-29 §6）。
	UpdateSession(ctx context.Context, s Session) error

	// —— run ——
	CreateRun(ctx context.Context, r Run) error
	ConversationRuns(ctx context.Context, sessionID string, before time.Time, limit int) ([]Run, error)
	Finalize(ctx context.Context, r Run) error
	GetRun(ctx context.Context, id string) (Run, error)
	// ActiveRunOfSession 会话当前 queued/running 任务（无则 nil）。
	ActiveRunOfSession(ctx context.Context, sessionID string) (*Run, error)
	// LatestRunsOfSession 会话最近任务（时间倒序，limit ≤20；追问上下文与详情用）。
	LatestRunsOfSession(ctx context.Context, sessionID string, limit int) ([]Run, error)
	// FindByIdempotencyKey 幂等键查 run（tenant/user/session 作用域）。
	FindByIdempotencyKey(ctx context.Context, tenantID, userID, sessionID, key string) (*Run, error)
	// UpdateRunStatus CAS 状态更新：fromStatus 空集合=无条件（但终态不可被覆盖）。
	// 返回 ErrConflict 表示 CAS 失败（取消/完成竞争只产生一个终态）。
	UpdateRunStatus(ctx context.Context, id string, from []string, mutate func(*Run)) error
	// AppendEvent 追加进度事件（seq 由仓储保证单调；UNIQUE(run_id, seq)）。
	AppendEvent(ctx context.Context, runID string, typ string, payload []byte) (Event, error)
	ListEvents(ctx context.Context, runID string, afterSeq int) ([]Event, error)

	// —— worker 队列 ——
	// ClaimNext 领取一个 queued 任务（事务原子；租户/全局限额由调用方先查）。
	// 返回 nil=无任务。PG 用 UPDATE ... WHERE status='queued' ... RETURNING。
	ClaimNext(ctx context.Context, now time.Time) (*Run, error)
	// Heartbeat 续约（lease 过期回收判定用；PG 检查 worker/fencing）。
	Heartbeat(ctx context.Context, runID, workerID string, now time.Time) error
	// ReclaimExpired 将 lease 过期的 running 任务标 failed/worker_lost（不自动重跑付费模型）。
	ReclaimExpired(ctx context.Context, now time.Time) int

	// —— 并发限额（跨副本原子；memory 单进程）——
	CountActive(ctx context.Context) (int, error)
	CountTenantActive(ctx context.Context, tenantID string) (int, error)
}

// —— 内存实现（单进程；重启丢失会话——UI 明示限制，PG 失败不静默回退）——

type memRepo struct {
	mu       sync.Mutex
	sessions map[string]*Session
	runs     map[string]*Run
	events   map[string][]Event // runID → 有序
}

// NewMemoryRepo 内存仓储。
func NewMemoryRepo() Repository {
	return &memRepo{sessions: map[string]*Session{}, runs: map[string]*Run{}, events: map[string][]Event{}}
}

func (m *memRepo) CreateSession(_ context.Context, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, dup := m.sessions[s.ID]; dup {
		return ErrConflict
	}
	cp := s
	m.sessions[s.ID] = &cp
	return nil
}

func (m *memRepo) GetSession(ctx context.Context, id string) (Session, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return Session{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok || s.TenantID != tid {
		return Session{}, ErrNotFound
	}
	return *s, nil
}

func (m *memRepo) UpdateSession(ctx context.Context, s Session) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	// 与 PG 同语义：owner 从 ctx 取且必须精确匹配（空 owner 同样拒绝）——
	// 调用侧漏 WithOwner 时内存实现同样 404，测试即抓（集群实测漏注入在 PG 路径静默 404）。
	if OwnerFrom(ctx) != s.OwnerUserID {
		return ErrNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old, ok := m.sessions[s.ID]
	if !ok || old.TenantID != tid || old.OwnerUserID != s.OwnerUserID {
		return ErrNotFound
	}
	// 身份字段不可变（防 Update 通道偷换 owner/agent）。
	s.CreatedAt = old.CreatedAt
	s.AgentKey = old.AgentKey
	s.AgentVersion = old.AgentVersion
	cp := s
	m.sessions[s.ID] = &cp
	return nil
}

func (m *memRepo) ListSessions(ctx context.Context, before time.Time, limit int) ([]Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	var out []Session
	for _, s := range m.sessions {
		tid, _ := tenant.TenantFrom(ctx)
		if s.TenantID == tid && s.OwnerUserID == OwnerFrom(ctx) && (before.IsZero() || s.UpdatedAt.Before(before)) {
			out = append(out, *s)
		}
	}
	// 时间倒序取前 limit。
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].UpdatedAt.After(out[i].UpdatedAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memRepo) CreateRun(_ context.Context, r Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, old := range m.runs {
		if r.IdempotencyKey != "" && old.TenantID == r.TenantID && old.OwnerUserID == r.OwnerUserID && old.SessionID == r.SessionID && old.IdempotencyKey == r.IdempotencyKey {
			return ErrReplay
		}
	}
	active, total := 0, 0
	for _, old := range m.runs {
		if old.Active() {
			total++
			if old.SessionID == r.SessionID {
				return ErrConflict
			}
			if old.TenantID == r.TenantID {
				active++
			}
		}
	}
	if total >= LimitGlobalConcurrent || active >= LimitTenantConcurrent {
		return ErrConflict
	}
	if _, dup := m.runs[r.ID]; dup {
		return ErrConflict
	}
	cp := r
	m.runs[r.ID] = &cp
	if sess := m.sessions[r.SessionID]; sess != nil {
		sess.UpdatedAt = r.CreatedAt
	}
	return nil
}

func (m *memRepo) GetRun(ctx context.Context, id string) (Run, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return Run{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok || r.TenantID != tid {
		return Run{}, ErrNotFound
	}
	return *r, nil
}

func (m *memRepo) ActiveRunOfSession(_ context.Context, sessionID string) (*Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.runs {
		if r.SessionID == sessionID && r.Active() {
			cp := *r
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *memRepo) LatestRunsOfSession(_ context.Context, sessionID string, limit int) ([]Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if limit <= 0 {
		limit = 5
	}
	var out []Run
	for _, r := range m.runs {
		if r.SessionID == sessionID {
			out = append(out, *r)
		}
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].CreatedAt.After(out[i].CreatedAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memRepo) FindByIdempotencyKey(_ context.Context, tenantID, userID, sessionID, key string) (*Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if key == "" {
		return nil, nil
	}
	for _, r := range m.runs {
		if r.TenantID == tenantID && r.OwnerUserID == userID &&
			r.SessionID == sessionID && r.IdempotencyKey == key {
			cp := *r
			return &cp, nil
		}
	}
	return nil, nil
}

func (m *memRepo) UpdateRunStatus(_ context.Context, id string, from []string, mutate func(*Run)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[id]
	if !ok {
		return ErrNotFound
	}
	if len(from) > 0 {
		matched := false
		for _, f := range from {
			if r.Status == f {
				matched = true
				break
			}
		}
		if !matched {
			return ErrConflict
		}
	}
	// 终态保护：已完成报告不可被迟到 Worker 覆盖。
	if IsTerminal(r.Status) {
		cur := r.Status
		mutate(r)
		r.Status = cur // 状态不回退；允许补写用量等字段
		return nil
	}
	mutate(r)
	return nil
}

func (m *memRepo) AppendEvent(_ context.Context, runID string, typ string, payload []byte) (Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	evs := m.events[runID]
	e := Event{Seq: len(evs) + 1, RunID: runID, Type: typ, Payload: payload, CreatedAt: time.Now().UTC()}
	m.events[runID] = append(evs, e)
	return e, nil
}

func (m *memRepo) ListEvents(_ context.Context, runID string, afterSeq int) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Event
	for _, e := range m.events[runID] {
		if e.Seq > afterSeq {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *memRepo) ClaimNext(_ context.Context, _ time.Time) (*Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var oldest *Run
	for _, r := range m.runs {
		if r.Status != StatusQueued {
			continue
		}
		if oldest == nil || r.CreatedAt.Before(oldest.CreatedAt) {
			oldest = r
		}
	}
	if oldest == nil {
		return nil, nil
	}
	oldest.Status = StatusRunning
	oldest.LeaseOwner = fmt.Sprintf("mem-%d", time.Now().UnixNano())
	oldest.LeaseExpires = time.Now().Add(90 * time.Second)
	now := time.Now().UTC()
	oldest.StartedAt = &now
	cp := *oldest
	return &cp, nil
}

func (m *memRepo) Heartbeat(_ context.Context, id, owner string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := m.runs[id]
	if r == nil || r.Status != StatusRunning || r.LeaseOwner != owner {
		return ErrConflict
	}
	r.LeaseExpires = now.Add(90 * time.Second)
	return nil
}

func (m *memRepo) ReclaimExpired(_ context.Context, now time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.runs {
		if r.Status == StatusRunning && !r.LeaseExpires.IsZero() && r.LeaseExpires.Before(now) {
			r.Status = StatusFailed
			r.ErrorCode = "worker_lost"
			r.ErrorMsg = "执行进程失联，请重试（不会自动重复推理）"
			r.FinishedAt = &now
			n++
		}
	}
	return n
}

func (m *memRepo) CountActive(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.runs {
		if r.Active() {
			n++
		}
	}
	return n, nil
}

func (m *memRepo) CountTenantActive(_ context.Context, tenantID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.runs {
		if r.TenantID == tenantID && r.Active() {
			n++
		}
	}
	return n, nil
}

// ConversationRuns 按租户、用户、会话过滤后分页。
func (m *memRepo) ConversationRuns(ctx context.Context, id string, before time.Time, limit int) ([]Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	tid, _ := tenant.TenantFrom(ctx)
	out := []Run{}
	for _, r := range m.runs {
		if r.TenantID == tid && r.OwnerUserID == OwnerFrom(ctx) && r.SessionID == id && (before.IsZero() || r.CreatedAt.Before(before)) {
			out = append(out, *r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	limit = conversationLimit(limit)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Finalize 原子保存回答、终态和终态事件；租约过期或取消竞争不会覆盖结果。
func (m *memRepo) Finalize(ctx context.Context, r Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.runs[r.ID]
	tid, _ := tenant.TenantFrom(ctx)
	if old == nil || old.TenantID != tid {
		return ErrNotFound
	}
	if old.Status != StatusRunning || old.LeaseOwner != r.LeaseOwner {
		return ErrConflict
	}
	if old.CancelFlag {
		r.Status = StatusCancelled
		r.Report = nil
		r.Reply = "已停止本轮诊断。已完成的历史消息仍然保留。"
		r.ErrorCode = "cancelled"
		r.ErrorMsg = r.Reply
	}
	r.CancelFlag = old.CancelFlag
	cp := r
	m.runs[r.ID] = &cp
	typ := EventReportReady
	if r.Status != StatusCompleted {
		typ = eventOfStatus(r.Status)
	}
	evs := m.events[r.ID]
	m.events[r.ID] = append(evs, Event{Seq: len(evs) + 1, RunID: r.ID, Type: typ, CreatedAt: time.Now().UTC()})
	if sess := m.sessions[r.SessionID]; sess != nil {
		sess.UpdatedAt = time.Now().UTC()
	}
	return nil
}
