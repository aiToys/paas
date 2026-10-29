// 上下文解析回归（spec 2026-10-29 §10 EARS）：旧会话兼容、唯一匹配自动确认、
// 多候选 409 零消耗、伪造候选 fail closed、告警/Trace 租户隔离、第二 Agent 复用。
package assistant

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/diagnose/scoped"
	"github.com/aitoys/paas/internal/observability"
)

// —— fake 依赖 ——

type fakeLister struct {
	items []scoped.WorkloadInfo
	err   error
}

func (f fakeLister) ListWorkloads(context.Context) ([]scoped.WorkloadInfo, error) {
	return f.items, f.err
}

type fakeAlerts struct {
	events map[string]observability.AlertEvent
}

func (f fakeAlerts) GetAlertEvent(_ context.Context, id string) (observability.AlertEvent, error) {
	ev, ok := f.events[id]
	if !ok {
		return observability.AlertEvent{}, errors.New("not found")
	}
	return ev, nil
}

type fakeTraces struct {
	traces map[string]observability.Trace
	deny   map[string]bool // 模拟跨租户/归属不可证明（GetTrace fail-closed 语义）
}

func (f fakeTraces) GetTrace(_ context.Context, id string) (observability.Trace, error) {
	if f.deny[id] {
		return observability.Trace{}, observability.ErrTraceNotFound
	}
	tr, ok := f.traces[id]
	if !ok {
		return observability.Trace{}, observability.ErrTraceNotFound
	}
	return tr, nil
}

// newContextService 带完整上下文解析链的测试服务。
func newContextService(t *testing.T, wl scoped.WorkloadReader, lister fakeLister, alerts fakeAlerts, traces fakeTraces) (*Service, Repository) {
	t.Helper()
	repo := NewMemoryRepo()
	svc := NewService(repo, &scoped.TargetResolver{Workloads: wl}, nil)
	overrideUserFn(svc)
	svc.SetContextResolver(&ContextResolver{
		Targets: &scoped.TargetResolver{Workloads: wl}, Workloads: lister,
		Alerts: alerts, Traces: traces,
	})
	return svc, repo
}

func mustCreateDraft(t *testing.T, svc *Service, ctx context.Context) Session {
	t.Helper()
	sess, err := svc.CreateSession(ctx, CreateSessionRequest{AgentKey: AgentKeyRuntimeDiagnosis})
	if err != nil {
		t.Fatalf("create draft session: %v", err)
	}
	if sess.SessionResolved() {
		t.Fatal("draft session must not carry target")
	}
	return sess
}

// EARS-1：唯一可解析服务名 → 展示解析目标并直接开始诊断（自动确认）。
func TestResolveTextUniqueNameAutoConfirms(t *testing.T) {
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{
		"wl-1": {ID: "wl-1", TenantID: "acme", AppID: "a", EnvID: "e", Type: "service", Name: "shop-web"},
	}}
	lister := fakeLister{items: []scoped.WorkloadInfo{
		{ID: "wl-1", TenantID: "acme", AppID: "a", EnvID: "e", Type: "service", Name: "shop-web"},
	}}
	svc, repo := newContextService(t, wl, lister, fakeAlerts{}, fakeTraces{})
	ctx := ctxOf("acme", "u1")
	sess := mustCreateDraft(t, svc, ctx)

	run, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "shop-web 一直重启，帮我查一下"}, "k1")
	if err != nil {
		t.Fatalf("create run with unique name: %v", err)
	}
	if run.Status != StatusQueued {
		t.Fatalf("run status=%s", run.Status)
	}
	// 会话已落库 resolved（旧 target 语义同构，执行链路无特判）。
	got, _ := svc.GetSession(ctx, sess.ID)
	if !got.SessionResolved() || got.Target.WorkloadID != "wl-1" {
		t.Fatalf("session should be resolved to wl-1, got %+v", got.Target)
	}
	if got.Context == nil || got.Context.Source != ContextSourceText || got.Context.ResolutionStatus != ResolutionResolved {
		t.Fatalf("context source/text status wrong: %+v", got.Context)
	}
	if _, err := repo.GetRun(ctx, run.ID); err != nil {
		t.Fatalf("run should be persisted: %v", err)
	}
}

// EARS-1（ID 形态）：文本中的 wl- ID 直接解析。
func TestResolveTextWorkloadID(t *testing.T) {
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{
		"wl-9": {ID: "wl-9", TenantID: "acme", AppID: "a", EnvID: "e", Type: "service", Name: "x"},
	}}
	svc, _ := newContextService(t, wl, fakeLister{}, fakeAlerts{}, fakeTraces{})
	ctx := ctxOf("acme", "u1")
	sess := mustCreateDraft(t, svc, ctx)
	if _, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "看下 wl-9 的内存"}, "k1"); err != nil {
		t.Fatalf("run by workload id: %v", err)
	}
	got, _ := svc.GetSession(ctx, sess.ID)
	if got.Target.WorkloadID != "wl-9" {
		t.Fatalf("target=%s want wl-9", got.Target.WorkloadID)
	}
}

// EARS-3：多候选 409 need_choice；不建 run、不扣预算（budget 调用计数为 0）。
func TestResolveTextMultipleCandidates(t *testing.T) {
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{
		"wl-t": {ID: "wl-t", TenantID: "acme", AppID: "a", EnvID: "env-test", Type: "service", Name: "shop-web"},
		"wl-p": {ID: "wl-p", TenantID: "acme", AppID: "a", EnvID: "env-prod", Type: "service", Name: "shop-web"},
	}}
	lister := fakeLister{items: []scoped.WorkloadInfo{
		{ID: "wl-t", TenantID: "acme", AppID: "a", EnvID: "env-test", Type: "service", Name: "shop-web"},
		{ID: "wl-p", TenantID: "acme", AppID: "a", EnvID: "env-prod", Type: "service", Name: "shop-web"},
	}}
	repo := NewMemoryRepo()
	svc := NewService(repo, &scoped.TargetResolver{Workloads: wl}, nil)
	overrideUserFn(svc)
	budgetCalls := 0
	svc.budget = func(context.Context, string, string) error { budgetCalls++; return nil }
	svc.SetContextResolver(&ContextResolver{
		Targets: &scoped.TargetResolver{Workloads: wl}, Workloads: lister,
	})
	ctx := ctxOf("acme", "u1")
	sess := mustCreateDraft(t, svc, ctx)

	_, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "shop-web 响应变慢"}, "k1")
	var req *ErrResolutionRequired
	if !errors.As(err, &req) || req.Status != ResolutionNeedChoice || len(req.Candidates) != 2 {
		t.Fatalf("want 409 need_choice with 2 candidates, got %v", err)
	}
	if budgetCalls != 0 {
		t.Fatalf("budget must not be called on unresolved run（got %d）", budgetCalls)
	}
	// 未建 run：会话无任务。
	if act, _ := repo.ActiveRunOfSession(ctx, sess.ID); act != nil {
		t.Fatal("no run should exist for unresolved session")
	}
	// 选择候选：resolved 落库。
	var candID string
	for _, c := range req.Candidates {
		if c.WorkloadID == "wl-t" {
			candID = c.ID
		}
	}
	resolved, err := svc.ResolveContext(ctx, sess.ID, candID)
	if err != nil || !resolved.SessionResolved() || resolved.Target.WorkloadID != "wl-t" {
		t.Fatalf("resolve candidate: err=%v target=%+v", err, resolved.Target)
	}
	// 选择后重发：run 创建成功。
	if _, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "shop-web 响应变慢"}, "k1"); err != nil {
		t.Fatalf("run after resolve: %v", err)
	}
}

// EARS-7：伪造 candidateId → 404 + 审计事件。
func TestForgedCandidateRejectedAndAudited(t *testing.T) {
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{
		"wl-1": {ID: "wl-1", TenantID: "acme", Type: "service", Name: "x"},
	}}
	svc, _ := newContextService(t, wl, fakeLister{}, fakeAlerts{}, fakeTraces{})
	audits := []string{}
	svc.Audit = func(_ context.Context, action, _ string) { audits = append(audits, action) }
	ctx := ctxOf("acme", "u1")
	sess := mustCreateDraft(t, svc, ctx)

	if _, err := svc.ResolveContext(ctx, sess.ID, "cand-forged"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forged candidate = %v, want ErrNotFound", err)
	}
	found := false
	for _, a := range audits {
		if a == "assistant.context.forged" {
			found = true
		}
	}
	if !found {
		t.Fatalf("forged candidate must be audited, got %v", audits)
	}
}

// EARS-2（告警）：workload 级告警直接 resolved，窗口对齐 FiredAt。
func TestAlertAnchorWorkloadResolved(t *testing.T) {
	fired := time.Now().UTC().Add(-2 * time.Hour)
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{
		"wl-1": {ID: "wl-1", TenantID: "acme", AppID: "a", EnvID: "e", Type: "service", Name: "x"},
	}}
	alerts := fakeAlerts{events: map[string]observability.AlertEvent{
		"rule-abc123": {ID: "rule-abc123", TenantID: "acme", TargetType: "workload", TargetID: "wl-1", FiredAt: fired, OccurredAt: fired.Add(5 * time.Minute)},
	}}
	svc, _ := newContextService(t, wl, fakeLister{}, alerts, fakeTraces{})
	ctx := ctxOf("acme", "u1")

	sess, err := svc.CreateSession(ctx, CreateSessionRequest{
		AgentKey: AgentKeyRuntimeDiagnosis, Context: &ContextInput{Kind: ContextKindAlert, ID: "rule-abc123"},
	})
	if err != nil {
		t.Fatalf("create session with alert anchor: %v", err)
	}
	if !sess.SessionResolved() || sess.Target.WorkloadID != "wl-1" {
		t.Fatalf("alert should resolve to wl-1: %+v", sess.Target)
	}
	if sess.Context == nil || sess.Context.Source != ContextKindAlert {
		t.Fatalf("context source should be alert: %+v", sess.Context)
	}
	// 窗口对齐告警时间（fired-15m 起，而非 now-30m）。
	if sess.Context.WindowStart.After(fired) {
		t.Fatalf("window should cover FiredAt: %v vs %v", sess.Context.WindowStart, fired)
	}
}

// 告警 app 级：展开候选（多服务应用 need_choice；EARS-3 同构）。
func TestAlertAnchorAppCandidates(t *testing.T) {
	alerts := fakeAlerts{events: map[string]observability.AlertEvent{
		"rule-app1": {ID: "rule-app1", TenantID: "acme", TargetType: "app", TargetID: "app-1", FiredAt: time.Now().UTC()},
	}}
	lister := fakeLister{items: []scoped.WorkloadInfo{
		{ID: "wl-a", TenantID: "acme", AppID: "app-1", EnvID: "e", Type: "service", Name: "svc-a"},
		{ID: "wl-b", TenantID: "acme", AppID: "app-1", EnvID: "e", Type: "service", Name: "svc-b"},
	}}
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{
		"wl-a": {ID: "wl-a", TenantID: "acme", AppID: "app-1", EnvID: "e", Type: "service", Name: "svc-a"},
		"wl-b": {ID: "wl-b", TenantID: "acme", AppID: "app-1", EnvID: "e", Type: "service", Name: "svc-b"},
	}}
	svc, _ := newContextService(t, wl, lister, alerts, fakeTraces{})
	ctx := ctxOf("acme", "u1")
	sess, err := svc.CreateSession(ctx, CreateSessionRequest{
		AgentKey: AgentKeyRuntimeDiagnosis, Context: &ContextInput{Kind: ContextKindAlert, ID: "rule-app1"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sess.SessionResolved() {
		t.Fatal("multi-service app alert should stay draft")
	}
	if len(sess.Candidates) != 2 {
		t.Fatalf("candidates=%d want 2", len(sess.Candidates))
	}
	// 候选确认走通。
	resolved, err := svc.ResolveContext(ctx, sess.ID, sess.Candidates[0].ID)
	if err != nil || !resolved.SessionResolved() {
		t.Fatalf("resolve: err=%v", err)
	}
}

// 告警跨租户：事件 TenantID 不匹配 → 不可用（不泄漏存在性）。
func TestAlertAnchorCrossTenantFailClosed(t *testing.T) {
	alerts := fakeAlerts{events: map[string]observability.AlertEvent{
		"rule-x": {ID: "rule-x", TenantID: "globex", TargetType: "workload", TargetID: "wl-1", FiredAt: time.Now().UTC()},
	}}
	svc, _ := newContextService(t, fakeWL{}, fakeLister{}, alerts, fakeTraces{})
	_, err := svc.CreateSession(ctxOf("acme", "u1"), CreateSessionRequest{
		AgentKey: AgentKeyRuntimeDiagnosis, Context: &ContextInput{Kind: ContextKindAlert, ID: "rule-x"},
	})
	if err == nil || strings.Contains(err.Error(), "globex") {
		t.Fatalf("cross-tenant alert must fail closed without leaking: %v", err)
	}
}

// EARS-2（Trace）：归属校验失败（GetTrace fail-closed）→ 不可用。
func TestTraceAnchorUnprovenTenantRejected(t *testing.T) {
	traces := fakeTraces{deny: map[string]bool{"4bf92f3577b34da6a3ce929d0e0e4736": true}}
	svc, _ := newContextService(t, fakeWL{}, fakeLister{}, fakeAlerts{}, traces)
	_, err := svc.CreateSession(ctxOf("acme", "u1"), CreateSessionRequest{
		AgentKey: AgentKeyRuntimeDiagnosis, Context: &ContextInput{Kind: ContextKindTrace, ID: "4bf92f3577b34da6a3ce929d0e0e4736"},
	})
	if err == nil {
		t.Fatal("unproven trace must be rejected")
	}
}

// Trace 唯一 service → resolved + 窗口对齐 StartedAt。
func TestTraceAnchorUniqueServiceResolved(t *testing.T) {
	started := time.Now().UTC().Add(-1 * time.Hour)
	traces := fakeTraces{traces: map[string]observability.Trace{
		"4bf92f3577b34da6": {ID: "4bf92f3577b34da6", StartedAt: started, DurationMs: 3000,
			Spans: []observability.Span{{Service: "shop-api", Operation: "GET /x"}}},
	}}
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{
		"wl-api": {ID: "wl-api", TenantID: "acme", AppID: "a", EnvID: "e", Type: "service", Name: "shop-api"},
	}}
	lister := fakeLister{items: []scoped.WorkloadInfo{
		{ID: "wl-api", TenantID: "acme", AppID: "a", EnvID: "e", Type: "service", Name: "shop-api"},
	}}
	svc, _ := newContextService(t, wl, lister, fakeAlerts{}, traces)
	ctx := ctxOf("acme", "u1")
	sess, err := svc.CreateSession(ctx, CreateSessionRequest{
		AgentKey: AgentKeyRuntimeDiagnosis, Context: &ContextInput{Kind: ContextKindTrace, ID: "4bf92f3577b34da6"},
	})
	if err != nil {
		t.Fatalf("trace anchor: %v", err)
	}
	if !sess.SessionResolved() || sess.Target.WorkloadID != "wl-api" {
		t.Fatalf("trace should resolve to wl-api: %+v", sess.Target)
	}
	if sess.Context.WindowStart.After(started) {
		t.Fatalf("trace window must cover StartedAt: %v", sess.Context.WindowStart)
	}
}

// Trace 跨多服务 → need_choice 候选。
func TestTraceAnchorMultiServiceCandidates(t *testing.T) {
	traces := fakeTraces{traces: map[string]observability.Trace{
		"4bf92f3577b34da7": {ID: "4bf92f3577b34da7", StartedAt: time.Now().UTC(),
			Spans: []observability.Span{{Service: "shop-api"}, {Service: "shop-bff"}}},
	}}
	lister := fakeLister{items: []scoped.WorkloadInfo{
		{ID: "wl-api", TenantID: "acme", AppID: "a", EnvID: "e", Type: "service", Name: "shop-api"},
		{ID: "wl-bff", TenantID: "acme", AppID: "a", EnvID: "e", Type: "service", Name: "shop-bff"},
	}}
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{
		"wl-api": {ID: "wl-api", TenantID: "acme", AppID: "a", EnvID: "e", Type: "service", Name: "shop-api"},
		"wl-bff": {ID: "wl-bff", TenantID: "acme", AppID: "a", EnvID: "e", Type: "service", Name: "shop-bff"},
	}}
	svc, _ := newContextService(t, wl, lister, fakeAlerts{}, traces)
	sess, err := svc.CreateSession(ctxOf("acme", "u1"), CreateSessionRequest{
		AgentKey: AgentKeyRuntimeDiagnosis, Context: &ContextInput{Kind: ContextKindTrace, ID: "4bf92f3577b34da7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sess.SessionResolved() || len(sess.Candidates) != 2 {
		t.Fatalf("multi-service trace should yield 2 candidates, resolved=%v cands=%d", sess.SessionResolved(), len(sess.Candidates))
	}
}

// EARS-4：无法解析 → 最小追问（409 need_clarification；不建 run）。
func TestResolveTextNoClueAsksMinimalQuestion(t *testing.T) {
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{
		"wl-1": {ID: "wl-1", TenantID: "acme", Type: "service", Name: "x"},
	}}
	svc, repo := newContextService(t, wl, fakeLister{items: []scoped.WorkloadInfo{
		{ID: "wl-1", TenantID: "acme", Type: "service", Name: "x"},
	}}, fakeAlerts{}, fakeTraces{})
	ctx := ctxOf("acme", "u1")
	sess := mustCreateDraft(t, svc, ctx)

	_, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "服务挂了怎么办"}, "k1")
	var req *ErrResolutionRequired
	if !errors.As(err, &req) || req.Status != ResolutionNeedClarification || req.Question == "" {
		t.Fatalf("want need_clarification with question, got %v", err)
	}
	if act, _ := repo.ActiveRunOfSession(ctx, sess.ID); act != nil {
		t.Fatal("no run should be created")
	}
}

// EARS-5：旧 workload 会话（target 直建）在解析体系下行为不变。
func TestLegacyTargetSessionUnchanged(t *testing.T) {
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{
		"wl-1": {ID: "wl-1", TenantID: "acme", AppID: "a", EnvID: "e", Type: "service", Name: "x"},
	}}
	svc, _ := newContextService(t, wl, fakeLister{}, fakeAlerts{}, fakeTraces{})
	ctx := ctxOf("acme", "u1")
	sess, err := svc.CreateSession(ctx, CreateSessionRequest{
		AgentKey: AgentKeyRuntimeDiagnosis, Target: TargetRef{Type: TargetTypeWorkload, ID: "wl-1"},
	})
	if err != nil || !sess.SessionResolved() {
		t.Fatalf("legacy target session: err=%v resolved=%v", err, sess.SessionResolved())
	}
	if sess.Context == nil || sess.Context.ResolutionStatus != ResolutionResolved {
		t.Fatalf("legacy session context should be resolved: %+v", sess.Context)
	}
	run, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "查一下"}, "k1")
	if err != nil || run.SessionID != sess.ID {
		t.Fatalf("legacy run: %v", err)
	}
}

// EARS-8：第二 Agent 注册即复用（目录带 inputKinds/supportedContextKinds；解析链同源）。
func TestSecondAgentReusesInfrastructure(t *testing.T) {
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{
		"wl-1": {ID: "wl-1", TenantID: "acme", AppID: "a", EnvID: "e", Type: "service", Name: "shop-web"},
	}}
	lister := fakeLister{items: []scoped.WorkloadInfo{
		{ID: "wl-1", TenantID: "acme", AppID: "a", EnvID: "e", Type: "service", Name: "shop-web"},
	}}
	svc, _ := newContextService(t, wl, lister, fakeAlerts{}, fakeTraces{})
	err := svc.RegisterAgent(AgentDesc{
		Key: "builtin:triage", Name: "分诊", Version: "1", Executor: "triage",
		InputKinds: []string{"natural_language", "workload"}, SupportedContextKinds: []string{"workload"},
	}, ExecutorFunc(func(context.Context, RunInput, func(string, bool)) (ExecuteResult, error) {
		return ExecuteResult{}, nil
	}))
	if err != nil {
		t.Fatalf("register second agent: %v", err)
	}
	ctx := ctxOf("acme", "u1")
	sess, err := svc.CreateSession(ctx, CreateSessionRequest{AgentKey: "builtin:triage"})
	if err != nil {
		t.Fatalf("second agent draft session: %v", err)
	}
	if _, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "shop-web 如何"}, "k1"); err != nil {
		t.Fatalf("second agent run via shared resolver: %v", err)
	}
	found := false
	for _, a := range svc.Agents() {
		if a.Key == "builtin:triage" && len(a.InputKinds) > 0 && len(a.SupportedContextKinds) > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("catalog must expose inputKinds/supportedContextKinds")
	}
}

// 候选 TTL 过期：作废（不误报伪造）。
func TestCandidateExpiry(t *testing.T) {
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{
		"wl-1": {ID: "wl-1", TenantID: "acme", Type: "service", Name: "x"},
	}}
	lister := fakeLister{items: []scoped.WorkloadInfo{
		{ID: "wl-1", TenantID: "acme", Type: "service", Name: "x"},
		{ID: "wl-2", TenantID: "acme", Type: "service", Name: "x"},
	}}
	svc, _ := newContextService(t, wl, lister, fakeAlerts{}, fakeTraces{})
	ctx := ctxOf("acme", "u1")
	sess := mustCreateDraft(t, svc, ctx)
	_, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "x 怎么样"}, "k1")
	var req *ErrResolutionRequired
	if !errors.As(err, &req) || len(req.Candidates) != 2 {
		t.Fatalf("want candidates: %v", err)
	}
	// 快进过期。
	svc.candMu.Lock()
	for k := range svc.candidates {
		e := svc.candidates[k]
		e.expiresAt = time.Now().UTC().Add(-time.Second)
		svc.candidates[k] = e
	}
	svc.candMu.Unlock()
	if _, err := svc.ResolveContext(ctx, sess.ID, req.Candidates[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired candidate = %v, want ErrNotFound", err)
	}
}
