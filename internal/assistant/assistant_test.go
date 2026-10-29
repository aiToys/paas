// 助手域测试（spec §12 验收）：租户隔离负向、幂等、取消幂等、报告校验证据引用制。
package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/diagnose/scoped"
	"github.com/aitoys/paas/pkg/tenant"
)

// fakeWL fake scoped.WorkloadReader（跨租户断言用：Get 返回带 TenantID 的投影）。
type fakeWL struct {
	items map[string]scoped.WorkloadInfo
}

func (f fakeWL) Get(_ context.Context, id string) (scoped.WorkloadInfo, error) {
	w, ok := f.items[id]
	if !ok {
		return scoped.WorkloadInfo{}, errors.New("not found")
	}
	return w, nil
}

func newTestService(t *testing.T, wl scoped.WorkloadReader) (*Service, Repository) {
	t.Helper()
	repo := NewMemoryRepo()
	svc := NewService(repo, &scoped.TargetResolver{Workloads: wl}, nil)
	overrideUserFn(svc)
	return svc, repo
}

func ctxOf(tid, uid string) context.Context {
	ctx := tenant.WithTenant(context.Background(), tid)
	return withTestUser(ctx, uid)
}

func TestSessionTenantIsolation(t *testing.T) {
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{
		"wl-acme": {ID: "wl-acme", TenantID: "acme", AppID: "app1", EnvID: "env1", Type: "service", Name: "wl-acme", Service: "web"},
	}}
	svc, _ := newTestService(t, wl)

	sess, err := svc.CreateSession(ctxOf("acme", "u1"), CreateSessionRequest{
		AgentKey: AgentKeyRuntimeDiagnosis, Target: TargetRef{Type: TargetTypeWorkload, ID: "wl-acme"},
	})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	// 跨租户读：统一 ErrNotFound（不泄漏存在性）。
	if _, err := svc.GetSession(ctxOf("globex", "u2"), sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant get = %v, want ErrNotFound", err)
	}
	// 跨用户读（同租户不同 owner）：ErrNotFound。
	if _, err := svc.GetSession(ctxOf("acme", "u2"), sess.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-user get = %v, want ErrNotFound", err)
	}
	// 伪造目标 ID：错误不泄漏（not found 语义）。
	if _, err := svc.CreateSession(ctxOf("globex", "u2"), CreateSessionRequest{
		AgentKey: AgentKeyRuntimeDiagnosis, Target: TargetRef{Type: TargetTypeWorkload, ID: "wl-acme"},
	}); err == nil || strings.Contains(err.Error(), "acme") {
		t.Fatalf("forged target cross-tenant create should fail without leaking: %v", err)
	}
}

func TestCreateRunValidationAndIdempotency(t *testing.T) {
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{
		"wl-1": {ID: "wl-1", TenantID: "acme", AppID: "a", EnvID: "e", Type: "service", Name: "wl-1"},
	}}
	svc, _ := newTestService(t, wl)
	ctx := ctxOf("acme", "u1")
	sess, err := svc.CreateSession(ctx, CreateSessionRequest{AgentKey: AgentKeyRuntimeDiagnosis, Target: TargetRef{Type: "workload", ID: "wl-1"}})
	if err != nil {
		t.Fatal(err)
	}
	// 空 question / 超长 / lookback 越界。
	if _, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "  "}, "test-key"); err == nil {
		t.Fatal("empty question should fail")
	}
	if _, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: strings.Repeat("长", 2001)}, "test-key"); err == nil {
		t.Fatal("oversized question should fail")
	}
	if _, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "q", LookbackMinutes: 9999}, "test-key"); err == nil {
		t.Fatal("lookback out of range should fail")
	}
	// 幂等：同 key 同请求返回原任务。
	r1, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "为什么重启"}, "idem-1")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "为什么重启"}, "idem-1")
	if err != nil {
		t.Fatal(err)
	}
	if r1.ID != r2.ID {
		t.Fatalf("idempotent replay got %s, want %s", r2.ID, r1.ID)
	}
	// 同 key 不同请求：拒绝。
	if _, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "换个问题"}, "idem-1"); err == nil {
		t.Fatal("same key different request should fail")
	}
	// 会话单活动任务（无 key 路径）。
	if _, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "第二个"}, "test-key"); !errors.Is(err, ErrConflict) {
		t.Fatalf("second active run = %v, want ErrConflict", err)
	}
}

func TestCancelIdempotentAndTerminalGuard(t *testing.T) {
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{"wl-1": {ID: "wl-1", TenantID: "acme", Type: "service", Name: "wl-1"}}}
	svc, repo := newTestService(t, wl)
	ctx := ctxOf("acme", "u1")
	sess, _ := svc.CreateSession(ctx, CreateSessionRequest{AgentKey: AgentKeyRuntimeDiagnosis, Target: TargetRef{Type: "workload", ID: "wl-1"}})
	run, _ := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "q"}, "test-key")

	// 取消 queued → CancelFlag 置位。
	got, err := svc.Cancel(ctx, run.ID)
	if err != nil || !got.CancelFlag {
		t.Fatalf("cancel: err=%v flag=%v", err, got.CancelFlag)
	}
	// 终态后再次取消：幂等返回当前状态（无冲突错误）。
	_ = repo.UpdateRunStatus(context.Background(), run.ID, nil, func(r *Run) {
		r.Status = StatusCancelled
	})
	got, err = svc.Cancel(ctx, run.ID)
	if err != nil || got.Status != StatusCancelled {
		t.Fatalf("cancel after terminal: err=%v status=%s", err, got.Status)
	}
	// 终态保护：迟到 Worker 不能覆盖终态。
	_ = repo.UpdateRunStatus(context.Background(), run.ID, []string{StatusRunning}, func(r *Run) {
		r.Status = StatusCompleted // 迟到完成
	})
	final, _ := repo.GetRun(ctx, run.ID)
	if final.Status != StatusCancelled {
		t.Fatalf("terminal guard failed: %s", final.Status)
	}
}

func TestValidateUserReportEvidenceRefs(t *testing.T) {
	ev := []Evidence{
		{ID: "ev-1", Tool: "get_workload_status", Status: "ok"},
		{ID: "ev-2", Tool: "get_pod_logs", Status: "ok"},
	}
	// 正常：两个工具源 → sufficient。
	llm := `前置分析文字。
{"verdict":{"severity":"warning","summary":"内存不足"},
 "hypotheses":[
   {"hypothesis":"OOM 频繁","confidence":0.9,"evidenceIds":["ev-1","ev-2"]},
   {"hypothesis":"虚构假设","confidence":0.8,"evidenceIds":["ev-999"]}],
 "suggestions":[{"text":"查看日志","destination":"workload_logs"},{"text":"危险","destination":"https://evil"}],
 "confidenceNote":"x"}`
	rep, quality := validateUserReport(llm, ev)
	if quality != QualitySufficient {
		t.Fatalf("quality=%s, want sufficient", quality)
	}
	if len(rep.Hypotheses) != 1 {
		t.Fatalf("hypotheses=%d, want 1（虚构引用剔除）", len(rep.Hypotheses))
	}
	if !strings.Contains(rep.ConfidenceNote, "剔除 1") {
		t.Fatalf("dropped note missing: %q", rep.ConfidenceNote)
	}
	for _, s := range rep.Suggestions {
		if s.Destination == "https://evil" {
			t.Fatal("non-whitelisted destination must be dropped")
		}
	}
	// severity 归一 + 无有效假设 → inconclusive。
	rep2, q2 := validateUserReport(`{"verdict":{"severity":"catastrophic","summary":"s"},"hypotheses":[{"hypothesis":"h","confidence":5,"evidenceIds":["ev-404"]}]}`, ev)
	if q2 != QualityInconclusive || rep2.Verdict.Severity != "info" || len(rep2.Hypotheses) != 0 {
		t.Fatalf("q2=%s sev=%s hyp=%d", q2, rep2.Verdict.Severity, len(rep2.Hypotheses))
	}
	// 不可解析输出 → invalid_output 降级报告（原文脱敏保留为 Summary，不吞内容）。
	rep3, q3 := validateUserReport("根据检查结果服务正常 sk-abcdef0123456789", ev)
	if q3 != QualityInvalidOutput {
		t.Fatalf("q3=%s, want invalid_output", q3)
	}
	if rep3.Verdict.Summary == "" || rep3.Verdict.Summary == "未能生成有效结论" {
		t.Fatalf("降级报告应保留原文: %q", rep3.Verdict.Summary)
	}
	if strings.Contains(rep3.Verdict.Summary, "sk-abcdef0123456789") {
		t.Fatal("降级原文必须脱敏")
	}
}

func TestRunViewOfHidesInternals(t *testing.T) {
	v := RunViewOf(Run{ID: "r1", Status: StatusCompleted, Report: []byte(`{"kind":"diagnosis_report"}`), IdempotencyKey: "secret"})
	if v.Report == nil || v.ID != "r1" {
		t.Fatal("RunView must expose report")
	}
	// 序列化产物不含内部字段（IdempotencyKey/RequestHash/CancelFlag 不出现在用户侧 JSON）。
	b, _ := json.Marshal(v)
	for _, leaked := range []string{"idempotencyKey", "requestHash", "cancelFlag"} {
		if strings.Contains(string(b), leaked) {
			t.Fatalf("RunView leaked internal field %s", leaked)
		}
	}
	if !IsTerminal(StatusTimedOut) || IsTerminal(StatusRunning) {
		t.Fatal("IsTerminal broken")
	}
}

// withTestUser 测试注入用户（生产由 svc.UserFn=gateway.UserIDFrom 提供）。
func withTestUser(ctx context.Context, uid string) context.Context {
	return context.WithValue(ctx, testUserKey{}, uid)
}

type testUserKey struct{}

// 覆写 UserFn 的辅助（TestSessionTenantIsolation 等依赖 owner 过滤）。
func overrideUserFn(svc *Service) {
	svc.UserFn = func(ctx context.Context) string {
		if v, ok := ctx.Value(testUserKey{}).(string); ok {
			return v
		}
		return ""
	}
}
