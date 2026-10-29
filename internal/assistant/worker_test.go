package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/diagnose/scoped"
)

// 集群事故回归锁（2026-10-28：queued 永不消费）：Worker 状态机全链
// claim→执行→completed 落库 + 事件序列。execFn 注入假执行器（真 LLM 链路
// 在集群 e2e 验证，这里锁队列语义）。
func TestWorkerConsumesQueuedRunToEnd(t *testing.T) {
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{"wl-1": {ID: "wl-1", TenantID: "acme", Type: "service", Name: "wl-1"}}}
	svc, repo := newTestService(t, wl)
	ctx := ctxOf("acme", "u1")
	sess, err := svc.CreateSession(ctx, CreateSessionRequest{AgentKey: AgentKeyRuntimeDiagnosis, Target: TargetRef{Type: "workload", ID: "wl-1"}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "服务健康吗"}, "test-key")
	if err != nil {
		t.Fatal(err)
	}

	w := NewWorker(svc, repo, Deps{})
	var sawTools []string
	w.execFn = func(ctx context.Context, _ Deps, in agentRunInput, onTool func(name string, start bool)) (ExecuteResult, error) {
		if in.Session.ID != sess.ID || in.Run.ID != run.ID {
			t.Fatalf("executor 拿错任务: %+v", in.Run)
		}
		onTool("k8s.spec", true)
		onTool("k8s.spec", false)
		sawTools = append(sawTools, "k8s.spec")
		return ExecuteResult{
			Report:   UserReport{Kind: "diagnosis_report", Version: "v1"},
			Quality:  QualitySufficient,
			Evidence: []Evidence{{ID: "ev-1", Tool: "k8s.spec", Status: "ok", Summary: "副本 2/2"}},
		}, nil
	}

	claimed, err := repo.ClaimNext(context.Background(), time.Now().UTC())
	if err != nil || claimed == nil || claimed.ID != run.ID {
		t.Fatalf("claim: %+v err=%v", claimed, err)
	}
	w.execute(context.Background(), *claimed)

	final, err := repo.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != StatusCompleted || final.Quality != QualitySufficient {
		t.Fatalf("终态应 completed+sufficient: %s/%s", final.Status, final.Quality)
	}
	if len(final.Evidence) != 1 || final.Evidence[0].ID != "ev-1" {
		t.Fatalf("证据未落库: %+v", final.Evidence)
	}
	if final.Usage.UsageSource != "estimated" || final.Usage.Tokens <= 0 {
		t.Fatalf("用量口径应 estimated 且非零: %+v", final.Usage)
	}
	if len(sawTools) != 1 {
		t.Fatalf("工具钩子应触发一次: %v", sawTools)
	}

	evs, err := repo.ListEvents(ctx, run.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
	}
	want := []string{EventRunQueued, EventRunStarted, EventToolStarted, EventToolCompleted, EventReportReady}
	if len(types) != len(want) {
		t.Fatalf("事件序列 %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("事件序列 %v, want %v", types, want)
		}
	}
}

// 排队超时直接终态（不消耗模型调用）。
func TestWorkerQueueTimeoutShortCircuit(t *testing.T) {
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{"wl-1": {ID: "wl-1", TenantID: "acme", Type: "service", Name: "wl-1"}}}
	svc, repo := newTestService(t, wl)
	ctx := ctxOf("acme", "u1")
	sess, _ := svc.CreateSession(ctx, CreateSessionRequest{AgentKey: AgentKeyRuntimeDiagnosis, Target: TargetRef{Type: "workload", ID: "wl-1"}})
	run, _ := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "q"}, "test-key")
	// 手动把创建时间拨回 1 小时前（超过 QueueWaitTimeout+RunTimeout）。
	stale := time.Now().UTC().Add(-time.Hour)
	_ = repo.UpdateRunStatus(ctx, run.ID, []string{StatusQueued}, func(r *Run) { r.CreatedAt = stale; r.Status = StatusQueued })

	w := NewWorker(svc, repo, Deps{})
	called := false
	w.execFn = func(context.Context, Deps, agentRunInput, func(string, bool)) (ExecuteResult, error) {
		called = true
		return ExecuteResult{}, nil
	}
	claimed, err := repo.ClaimNext(context.Background(), time.Now().UTC())
	if err != nil || claimed == nil {
		t.Fatalf("claim: %+v err=%v", claimed, err)
	}
	w.execute(context.Background(), *claimed)

	final, _ := repo.GetRun(ctx, run.ID)
	if final.Status != StatusFailed || final.ErrorCode != "queued_timeout" {
		t.Fatalf("应 queued_timeout 终态: %s/%s", final.Status, final.ErrorCode)
	}
	if called {
		t.Fatal("排队超时不应消耗模型调用")
	}
}
