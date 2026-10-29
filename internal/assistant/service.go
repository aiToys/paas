// 会话与任务生命周期（spec §5-§8）：目标授权解析、入参校验、幂等、限额、
// 预算准入、窗口固定、追问上下文、取消。执行归 worker；隔离双保险：
// service 强制 tenant+owner 比对（仓储层再过滤一次）。
package assistant

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/diagnose/scoped"
	"github.com/aitoys/paas/pkg/tenant"
)

// 并发与资源限额（spec §8）。
const (
	LimitTenantConcurrent = 1  // 每租户同时 1 个活动任务
	LimitGlobalConcurrent = 4  // 全局（跨租户）活动任务
	LimitRoundsPerRun     = 12 // 单任务 LLM 轮次
	LimitToolCallsPerRun  = 24 // 单任务工具调用
	RunTimeout            = 180 * time.Second
	QueueWaitTimeout      = 60 * time.Second // 排队超时（超时标 failed/queued_timeout）
	MaxQuestionRunes      = 2000
	MinLookbackMinutes    = 5
	MaxLookbackMinutes    = 360
	HistoryTurns          = 6 // 追问上下文轮数
)

// BudgetCheck 预算准入（cmd/core 桥接 billing budgetGate；nil=跳过）。
type BudgetCheck func(ctx context.Context, tenantID, appID string) error

// Service 助手领域服务。
type Service struct {
	Settings  Settings
	Authorize func(context.Context, Session) error
	Audit     func(context.Context, string, string)
	catalog   []AgentDesc
	executors map[string]Executor
	repo      Repository
	resolver  *scoped.TargetResolver
	budget    BudgetCheck
	// contexts 自然语言/锚点上下文解析器（spec 2026-10-29；装配层 SetContextResolver
	// 注入。缺装配时文本解析诚实降级为追问，不静默扫描）。
	contexts *ContextResolver
	// candMu/candidates 服务端候选缓存（绑定 tenant/user/session + TTL；客户端
	// 只能提交服务端生成的 candidateId，伪造一律 fail closed）。
	candMu     sync.Mutex
	candidates map[string]candidateEntry
	// UserFn 从 ctx 取当前用户 ID（装配层注入 gateway.UserIDFrom；方向倒置，
	// 领域包不依赖 gateway）。nil 或空身份拒绝访问私有会话。
	UserFn func(ctx context.Context) string
	now    func() time.Time
	newID  func() string
}

// candidateEntry 候选缓存条目（不落库：一次性消费 + TTL 过期即作废）。
type candidateEntry struct {
	candidate   ContextCandidate
	tenantID    string
	ownerUserID string
	sessionID   string
	expiresAt   time.Time
}

// SetContextResolver 注入上下文解析器（启动前装配；运行期不可变）。
func (s *Service) SetContextResolver(cr *ContextResolver) {
	s.contexts = cr
	if s.candidates == nil {
		s.candidates = map[string]candidateEntry{}
	}
}

// NewService 构造。
func NewService(repo Repository, resolver *scoped.TargetResolver, budget BudgetCheck) *Service {
	return &Service{
		Settings: Settings{HistoryDays: 30, EvidenceDays: 7}, repo: repo, resolver: resolver, budget: budget, catalog: ListAgents(), executors: map[string]Executor{},
		now: func() time.Time { return time.Now().UTC() },
		newID: func() string {
			var b [16]byte
			if _, err := rand.Read(b[:]); err != nil {
				panic(err)
			}
			return "as-" + hex.EncodeToString(b[:]) // 展示 ID；PG 版用 gen_random_uuid
		},
	}
}

// —— 会话 ——

// CreateSession 建会话（spec 2026-10-29 §6 三形态）：
//  1. 旧版 target（兼容，行为不变）；
//  2. 结构化 context 锚点（告警/Trace 详情页深链）：唯一匹配直接 resolved；
//     多匹配建 draft 会话并返回服务端候选（用户选择前不得建 run）；
//     无法解析 400 带原因（fail closed，不建会话）；
//  3. 都空 = draft 自然语言会话（首个问题时解析）。
func (s *Service) CreateSession(ctx context.Context, req CreateSessionRequest) (Session, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return Session{}, err
	}
	agent, ok := s.lookupAgent(req.AgentKey)
	if ok && agent.DisabledReason != "" {
		return Session{}, ErrUnavailable
	}
	if !ok {
		return Session{}, fmt.Errorf("unknown agent: %s", req.AgentKey)
	}
	if s.ownerFrom(ctx) == "" {
		return Session{}, ErrNotFound
	}
	uid := s.ownerFrom(ctx)

	// 形态 3：draft 自然语言会话。
	if req.Context == nil && req.Target.Type == "" && req.Target.ID == "" {
		now := s.now()
		sess := Session{
			ID: s.newID(), TenantID: tid, OwnerUserID: uid,
			AgentKey: agent.Key, AgentVersion: agent.Version,
			Context: &DiagnosticContext{
				ResolutionStatus: ResolutionNeedClarification,
				ResolutionReason: "尚未解析诊断目标，发送首个问题后自动解析",
			},
			CreatedAt: now, UpdatedAt: now,
		}
		if err := s.repo.CreateSession(ctx, sess); err != nil {
			return Session{}, err
		}
		return sess, nil
	}
	// 形态 2：结构化 context 锚点。
	if req.Context != nil {
		if req.Target.Type != "" || req.Target.ID != "" {
			return Session{}, fmt.Errorf("target 与 context 不能同时提供")
		}
		if s.contexts == nil {
			return Session{}, ErrUnavailable
		}
		out, err := s.contexts.Resolve(ctx, *req.Context)
		if err != nil {
			return Session{}, err
		}
		switch out.context.ResolutionStatus {
		case ResolutionResolved:
			nc := out.context
			return s.createResolvedSession(ctx, tid, uid, agent, &nc, deref(req.Focus))
		case ResolutionNeedChoice:
			// draft 会话承载候选（candidateId 绑定本会话；选择前不建 run 不扣预算）。
			sess, err := s.createDraftWithStatus(ctx, tid, uid, agent, out.context)
			if err != nil {
				return Session{}, err
			}
			sess.Candidates = s.registerCandidates(ctx, sess.ID, out.candidates)
			return sess, nil
		default: // unavailable / need_clarification：fail closed 不建会话，原因直说。
			return Session{}, fmt.Errorf("%s", out.context.ResolutionReason)
		}
	}
	// 形态 1：旧 target（兼容）。
	if req.Target.Type != TargetTypeWorkload {
		return Session{}, fmt.Errorf("%w: %s", scoped.ErrUnsupportedTarget, req.Target.Type)
	}
	t, err := s.resolver.Resolve(ctx, tid, req.Target.ID)
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	ws, we := ctxWindow(now.Add(-defaultLookback), now)
	nc := DiagnosticContext{
		Anchors: []DiagnosticAnchor{{Kind: ContextKindWorkload, ID: req.Target.ID}},
		Source:  ContextKindWorkload, Resolved: &t,
		WindowStart: ws, WindowEnd: we,
		ResolutionStatus: ResolutionResolved,
	}
	return s.createResolvedSession(ctx, tid, uid, agent, &nc, deref(req.Focus))
}

// createResolvedSession resolved 上下文统一落库（授权校验 + focus 校验 + 建档）。
func (s *Service) createResolvedSession(ctx context.Context, tid, uid string, agent AgentDesc, nc *DiagnosticContext, focus FocusRef) (Session, error) {
	now := s.now()
	sess := Session{
		ID: s.newID(), TenantID: tid, OwnerUserID: uid,
		AgentKey: agent.Key, AgentVersion: agent.Version,
		Target: *nc.Resolved, Context: nc, Focus: focus,
		CreatedAt: now, UpdatedAt: now,
	}
	if s.Authorize != nil {
		if err := s.Authorize(ctx, sess); err != nil {
			return Session{}, ErrNotFound
		}
	}
	if err := s.resolver.ValidateFocus(ctx, sess.Target, sess.Focus.PodName, sess.Focus.PodUID, sess.Focus.Container); err != nil {
		return Session{}, err
	}
	if err := s.repo.CreateSession(ctx, sess); err != nil {
		return Session{}, err
	}
	return sess, nil
}

// createDraftWithStatus draft 会话（need_choice 状态 + 来源锚点）。
func (s *Service) createDraftWithStatus(ctx context.Context, tid, uid string, agent AgentDesc, nc DiagnosticContext) (Session, error) {
	now := s.now()
	sess := Session{
		ID: s.newID(), TenantID: tid, OwnerUserID: uid,
		AgentKey: agent.Key, AgentVersion: agent.Version,
		Context: &nc, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.CreateSession(ctx, sess); err != nil {
		return Session{}, err
	}
	return sess, nil
}

// registerCandidates 服务端生成候选 ID 并绑定 tenant/user/session（TTL 一次性）。
func (s *Service) registerCandidates(ctx context.Context, sessionID string, cands []ContextCandidate) []ContextCandidate {
	if len(cands) == 0 || s.candidates == nil {
		return nil
	}
	tid, _ := tenant.IDOrErr(ctx)
	uid := s.ownerFrom(ctx)
	now := s.now()
	out := make([]ContextCandidate, 0, len(cands))
	s.candMu.Lock()
	defer s.candMu.Unlock()
	for _, c := range cands {
		c.ID = "cand-" + s.newID()[3:]
		s.candidates[c.ID] = candidateEntry{
			candidate: c, tenantID: tid, ownerUserID: uid,
			sessionID: sessionID, expiresAt: now.Add(candidateTTL),
		}
		out = append(out, c)
	}
	return out
}

// takeCandidate 消费候选（一次性）。不存在/绑定不匹配=伪造（EARS 7：拒绝+审计）；
// 过期=作废（正常流）。命中即删除，防重放。
func (s *Service) takeCandidate(ctx context.Context, sess Session, candidateID string) (ContextCandidate, bool) {
	if s.candidates == nil || candidateID == "" {
		return ContextCandidate{}, false
	}
	tid, _ := tenant.IDOrErr(ctx)
	uid := s.ownerFrom(ctx)
	s.candMu.Lock()
	defer s.candMu.Unlock()
	entry, ok := s.candidates[candidateID]
	if ok && entry.expiresAt.Before(s.now()) {
		delete(s.candidates, candidateID)
		ok = false
	}
	if !ok || entry.tenantID != tid || entry.ownerUserID != uid || entry.sessionID != sess.ID {
		return ContextCandidate{}, false
	}
	delete(s.candidates, candidateID)
	return entry.candidate, true
}

// ResolveContext 用户确认候选（spec §6：目标变更语义=本会话首次确认；已 resolved
// 的会话换目标走新建）。候选重新经 scoped 五步解析成 AuthorizedTarget（不信任缓存）。
func (s *Service) ResolveContext(ctx context.Context, sessionID, candidateID string) (Session, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return Session{}, err
	}
	sess, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	if sess.SessionResolved() {
		return Session{}, fmt.Errorf("%w: 会话已有确认目标，更换目标请新建会话", ErrConflict)
	}
	cand, ok := s.takeCandidate(ctx, sess, candidateID)
	if !ok {
		s.audit(ctx, "assistant.context.forged", sessionID) // EARS 7
		return Session{}, ErrNotFound
	}
	t, err := s.resolver.Resolve(ctx, tid, cand.WorkloadID)
	if err != nil {
		return Session{}, ErrNotFound
	}
	// 保留 draft 阶段的来源锚点（告警/Trace 来源可追溯），窗口沿用解析时对齐的窗。
	nc := DiagnosticContext{
		Anchors: append(anchorsOf(firstAnchor(sess)), DiagnosticAnchor{Kind: ContextKindWorkload, ID: cand.WorkloadID}),
		Source:  sess.contextSource(), Resolved: &t,
		WindowStart: sess.contextWindowStart(), WindowEnd: sess.contextWindowEnd(),
		ResolutionStatus: ResolutionResolved,
	}
	if nc.WindowStart == nil {
		nc.WindowStart, nc.WindowEnd = ctxWindow(s.now().Add(-defaultLookback), s.now())
	}
	sess.Target = t
	sess.Context = &nc
	sess.UpdatedAt = s.now()
	// PG 层 owner 过滤从 ctx 取（不信任 sess 字段），调用侧须注入 WithOwner——
	// 漏注入会 0 行命中归一 ErrNotFound（集群实测 404，内存 repo 不踩此坑）。
	if err := s.repo.UpdateSession(WithOwner(ctx, s.ownerFrom(ctx)), sess); err != nil {
		return Session{}, err
	}
	s.audit(ctx, "assistant.context.resolve", sess.ID)
	return sess, nil
}

func firstAnchor(sess Session) DiagnosticAnchor {
	if sess.Context == nil || len(sess.Context.Anchors) == 0 {
		return DiagnosticAnchor{}
	}
	return sess.Context.Anchors[0]
}

func (sess Session) contextSource() string {
	if sess.Context == nil {
		return ""
	}
	return sess.Context.Source
}

func (sess Session) contextWindowStart() *time.Time {
	if sess.Context == nil {
		return nil
	}
	return sess.Context.WindowStart
}

func (sess Session) contextWindowEnd() *time.Time {
	if sess.Context == nil {
		return nil
	}
	return sess.Context.WindowEnd
}

// GetSession 取会话（tenant+owner 强制，跨租户统一 ErrNotFound 不泄漏）。
func (s *Service) GetSession(ctx context.Context, id string) (Session, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return Session{}, err
	}
	sess, err := s.repo.GetSession(WithOwner(ctx, s.ownerFrom(ctx)), id)
	if err != nil {
		return Session{}, err
	}
	if sess.TenantID != tid || !sameOwner(s.ownerFrom(ctx), sess.OwnerUserID) {
		return Session{}, ErrNotFound
	}
	return sess, nil
}

// ListSessions 当前用户会话列表（时间倒序）。
func (s *Service) ListSessions(ctx context.Context, before time.Time, limit int) ([]Session, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	sessions, err := s.repo.ListSessions(WithOwner(ctx, s.ownerFrom(ctx)), before, limit)
	if err != nil {
		return nil, err
	}
	// 仓储按全局列出的（memory 版）：过滤 tenant+owner 再截断。
	out := make([]Session, 0, len(sessions))
	uid := s.ownerFrom(ctx)
	for _, s := range sessions {
		if s.TenantID == tid && sameOwner(uid, s.OwnerUserID) {
			out = append(out, s)
		}
	}
	return out, nil
}

// SessionRuns 会话任务列表（时间倒序；归属校验复用 GetSession）。
func (s *Service) SessionRuns(ctx context.Context, sessionID string, limit int) ([]Run, error) {
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	return s.repo.ConversationRuns(WithOwner(ctx, s.ownerFrom(ctx)), sessionID, time.Time{}, limit)
}

// —— 任务 ——

// CreateRun 受理任务（202 语义：入队即返；执行异步）。
// 幂等：同 Idempotency-Key + 同请求 hash 重放返回原任务。
func (s *Service) CreateRun(ctx context.Context, sessionID string, req CreateRunRequest, idemKey string) (Run, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return Run{}, err
	}
	sess, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return Run{}, err
	}
	// 入参校验（提前到解析之前：draft 会话先有合法问题才触发上下文解析）。
	q := scoped.MaskSensitive(strings.TrimSpace(req.Question))
	if req.Mode != "" && req.Mode != "investigate" && req.Mode != "explain" {
		return Run{}, fmt.Errorf("mode 无效")
	}
	if len(idemKey) > 128 {
		return Run{}, fmt.Errorf("Idempotency-Key 过长")
	}
	if idemKey == "" {
		return Run{}, fmt.Errorf("Idempotency-Key 必填")
	}
	if q == "" {
		return Run{}, errors.New("question 不能为空")
	}
	if len([]rune(q)) > MaxQuestionRunes {
		return Run{}, fmt.Errorf("question 超过 %d 字符上限", MaxQuestionRunes)
	}
	lookback := req.LookbackMinutes
	if lookback == 0 {
		lookback = 30
	}
	if lookback < MinLookbackMinutes || lookback > MaxLookbackMinutes {
		return Run{}, fmt.Errorf("lookbackMinutes 须在 %d-%d 之间", MinLookbackMinutes, MaxLookbackMinutes)
	}
	// draft 会话：先从问题文本解析上下文（spec §6：唯一匹配自动确认；多候选/
	// 无法解析 409 target_resolution_required，不建 run、不预留 token、不扣预算）。
	if !sess.SessionResolved() {
		sess, err = s.resolveQuestionContext(ctx, tid, sess, q)
		if err != nil {
			return Run{}, err
		}
	}
	if s.Authorize != nil {
		if err := s.Authorize(ctx, sess); err != nil {
			return Run{}, ErrNotFound
		}
	}
	// 幂等重放：同键同 hash 直接返回原任务（不论状态）。
	reqHash := hashRequest(req)
	if idemKey != "" {
		if prev, _ := s.repo.FindByIdempotencyKey(ctx, tid, s.ownerFrom(ctx), sessionID, idemKey); prev != nil {
			if prev.RequestHash == reqHash {
				return *prev, nil
			}
			return Run{}, fmt.Errorf("idempotency key 已用于不同请求")
		}
	}
	// 同会话单活动任务。
	if act, _ := s.repo.ActiveRunOfSession(ctx, sessionID); act != nil {
		return Run{}, fmt.Errorf("%w: 会话已有进行中的任务", ErrConflict)
	}
	// 并发限额：租户 1 / 全局 4。
	if n, _ := s.repo.CountTenantActive(ctx, tid); n >= LimitTenantConcurrent {
		return Run{}, fmt.Errorf("%w: 租户并发任务已达上限（%d）", ErrConflict, LimitTenantConcurrent)
	}
	if n, _ := s.repo.CountActive(ctx); n >= LimitGlobalConcurrent {
		return Run{}, fmt.Errorf("%w: 平台并发任务已达上限（%d），请稍后重试", ErrConflict, LimitGlobalConcurrent)
	}
	// 预算准入（首期检查型：超强制预算拒新任务；spec §8）。
	if s.budget != nil {
		if err := s.budget(ctx, tid, sess.Target.AppID); err != nil {
			return Run{}, fmt.Errorf("预算受限：%w", err)
		}
	}
	// 追问上下文：关联会话最近一个终态任务。
	parent := ""
	if runs, _ := s.repo.LatestRunsOfSession(ctx, sessionID, 1); len(runs) > 0 && IsTerminal(runs[0].Status) {
		parent = runs[0].ID
	}
	now := s.now()
	winStart, winEnd := now.Add(-time.Duration(lookback)*time.Minute), now
	// 缺省 lookback 且上下文解析自带观察窗（告警/Trace 对齐事件时间）时沿用解析窗。
	if req.LookbackMinutes == 0 && sess.Context != nil &&
		sess.Context.WindowStart != nil && sess.Context.WindowEnd != nil {
		winStart, winEnd = *sess.Context.WindowStart, *sess.Context.WindowEnd
	}
	run := Run{
		ID: s.newID(), SessionID: sessionID, TenantID: tid, OwnerUserID: s.ownerFrom(ctx),
		Question: q, Symptom: req.Symptom, ParentRunID: parent, Mode: req.Mode,
		WindowStart: winStart, // 创建时固定 UTC
		WindowEnd:   winEnd,
		Status:      StatusQueued, IdempotencyKey: idemKey, RequestHash: reqHash,
		CreatedAt: now,
	}
	if err := s.repo.CreateRun(ctx, run); err != nil {
		if errors.Is(err, ErrReplay) {
			prev, e := s.repo.FindByIdempotencyKey(ctx, tid, s.ownerFrom(ctx), sessionID, idemKey)
			if e == nil && prev != nil && prev.RequestHash == reqHash {
				return *prev, nil
			}
			return Run{}, ErrConflict
		}
		return Run{}, err
	}
	s.emit(ctx, run.ID, EventRunQueued, nil)
	s.audit(ctx, "assistant.run", run.ID)
	return run, nil
}

// resolveQuestionContext draft 会话首个问题解析（spec §4）：唯一匹配自动确认并
// 落库；多候选注册服务端候选后 409；无法解析最小追问 409。不建 run、不扣预算。
func (s *Service) resolveQuestionContext(ctx context.Context, tid string, sess Session, question string) (Session, error) {
	if s.contexts == nil {
		return Session{}, &ErrResolutionRequired{
			Status:   ResolutionNeedClarification,
			Question: "上下文解析器未装配，请从工作负载详情页发起诊断",
		}
	}
	out, err := s.contexts.ResolveText(ctx, question)
	if err != nil {
		return Session{}, err
	}
	switch out.context.ResolutionStatus {
	case ResolutionResolved:
		nc := out.context
		sess.Target = *nc.Resolved
		sess.Context = &nc
		sess.UpdatedAt = s.now()
		// 同上：PG UpdateSession 的 owner 过滤靠 ctx 注入，此处原 ctx 无 owner。
		if err := s.repo.UpdateSession(WithOwner(ctx, s.ownerFrom(ctx)), sess); err != nil {
			return Session{}, err
		}
		s.audit(ctx, "assistant.context", sess.ID)
		return sess, nil
	case ResolutionNeedChoice:
		return Session{}, &ErrResolutionRequired{
			Status:     ResolutionNeedChoice,
			Question:   out.context.ResolutionReason,
			Candidates: s.registerCandidates(ctx, sess.ID, out.candidates),
			Anchor:     firstAnchorOf(out.context),
		}
	default: // unavailable / need_clarification：统一最小追问（原因即问题）。
		return Session{}, &ErrResolutionRequired{
			Status:   ResolutionNeedClarification,
			Question: out.context.ResolutionReason,
			Anchor:   firstAnchorOf(out.context),
		}
	}
}

func firstAnchorOf(nc DiagnosticContext) DiagnosticAnchor {
	if len(nc.Anchors) == 0 {
		return DiagnosticAnchor{}
	}
	return nc.Anchors[0]
}

// GetRun 取任务（tenant+owner 强制）。
func (s *Service) GetRun(ctx context.Context, id string) (Run, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return Run{}, err
	}
	r, err := s.repo.GetRun(WithOwner(ctx, s.ownerFrom(ctx)), id)
	if err != nil {
		return Run{}, err
	}
	if r.TenantID != tid || !sameOwner(s.ownerFrom(ctx), r.OwnerUserID) {
		return Run{}, ErrNotFound
	}
	return r, nil
}

// ListEvents 任务事件（afterSeq 增量轮询）。
func (s *Service) ListEvents(ctx context.Context, runID string, afterSeq int) ([]Event, error) {
	if _, err := s.GetRun(ctx, runID); err != nil {
		return nil, err
	}
	return s.repo.ListEvents(ctx, runID, afterSeq)
}

// Cancel 取消：终态幂等返回；queued/running 置 CancelFlag（worker 检查后 CAS 终态）。
func (s *Service) Cancel(ctx context.Context, runID string) (Run, error) {
	if _, err := s.GetRun(ctx, runID); err != nil {
		return Run{}, err
	}
	s.audit(ctx, "assistant.cancel", runID)
	err := s.repo.UpdateRunStatus(ctx, runID, []string{StatusQueued, StatusRunning}, func(r *Run) {
		r.CancelFlag = true
	})
	if errors.Is(err, ErrConflict) {
		// 已终态：幂等返回当前状态。
		return s.GetRun(ctx, runID)
	}
	if err != nil {
		return Run{}, err
	}
	return s.GetRun(ctx, runID)
}

// HistoryRuns 追问上下文（最近 N 个完成任务摘要）。
func (s *Service) HistoryRuns(ctx context.Context, sessionID string, n int) []TurnSummary {
	runs, err := s.repo.LatestRunsOfSession(ctx, sessionID, n*2)
	if err != nil {
		return nil
	}
	var out []TurnSummary
	for _, r := range runs {
		if len(out) >= n {
			break
		}
		if r.Status != StatusCompleted {
			continue
		}
		var rep UserReport
		_ = json.Unmarshal(r.Report, &rep)
		if r.Reply != "" {
			rep.Verdict.Summary = r.Reply
		}
		var ids []string
		for _, h := range rep.Hypotheses {
			ids = append(ids, h.EvidenceIDs...)
		}
		out = append(out, TurnSummary{Question: r.Question, Summary: rep.Verdict.Summary, EvidenceIDs: ids, Report: string(r.Report)})
	}
	return out
}

// emit 事件追加（失败仅记日志语义——不阻塞主流程；payload 简单键值）。
func (s *Service) emit(ctx context.Context, runID, typ string, payload map[string]any) {
	var raw []byte
	if payload != nil {
		raw, _ = json.Marshal(payload)
	}
	_, _ = s.repo.AppendEvent(ctx, runID, typ, raw)
}

func hashRequest(req CreateRunRequest) string {
	h := sha256.Sum256([]byte(scoped.MaskSensitive(strings.TrimSpace(req.Question)) + "|" + req.Mode + "|" + req.Symptom + "|" + fmt.Sprint(req.LookbackMinutes)))
	return hex.EncodeToString(h[:8])
}

func deref(f *FocusRef) FocusRef {
	if f == nil {
		return FocusRef{}
	}
	return *f
}

// ownerFrom ctx 取用户 ID（API Key 通道也必须携带明确用户；空身份不授予会话访问权）。
func (s *Service) ownerFrom(ctx context.Context) string {
	if s.UserFn == nil {
		return ""
	}
	return s.UserFn(ctx)
}

// sameOwner 会话/任务 owner 比对（uid 空时拒绝，不以空身份扩展权限）。
func sameOwner(uid, owner string) bool { return uid != "" && uid == owner }

// HTTPStatus 领域错误→状态码（handler 统一出口，不裸 500）。
func HTTPStatus(err error) int {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrConflict):
		return http.StatusConflict
	case errors.Is(err, ErrUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, scoped.ErrUnsupportedTarget):
		return http.StatusBadRequest
	default:
		return http.StatusBadRequest
	}
}

// RegisterAgent 在服务启动前装配官方 Agent。目录与执行器一起注册，不接受租户动态覆盖。
func (s *Service) RegisterAgent(desc AgentDesc, executor Executor) error {
	if desc.Key == "" || executor == nil {
		return fmt.Errorf("agent key 与执行器必填")
	}
	for _, a := range s.catalog {
		if a.Key == desc.Key {
			return ErrConflict
		}
	}
	s.catalog = append(s.catalog, desc)
	s.executors[desc.Key] = executor
	return nil
}
func (s *Service) lookupAgent(key string) (AgentDesc, bool) {
	for _, a := range s.catalog {
		if a.Key == key {
			return a, true
		}
	}
	return AgentDesc{}, false
}
func (s *Service) Agents() []AgentDesc { return append([]AgentDesc(nil), s.catalog...) }

func (s *Service) audit(ctx context.Context, action, id string) {
	if s.Audit != nil {
		if OwnerFrom(ctx) == "" {
			ctx = WithOwner(ctx, s.ownerFrom(ctx))
		}
		s.Audit(ctx, action, id)
	}
}

// SetAgentAvailability 在启动前根据实际依赖设置可用性，客户端不能覆写。
func (s *Service) SetAgentAvailability(key, reason string) {
	for i := range s.catalog {
		if s.catalog[i].Key == key {
			s.catalog[i].DisabledReason = reason
		}
	}
}
