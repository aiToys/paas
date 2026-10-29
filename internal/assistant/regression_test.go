package assistant

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/diagnose/scoped"
)

func reviewSetup(t *testing.T) (*Service, Repository, Session) {
	wl := fakeWL{items: map[string]scoped.WorkloadInfo{"wl-review": {ID: "wl-review", TenantID: "acme", Type: "service", Name: "review"}}}
	svc, repo := newTestService(t, wl)
	sess, err := svc.CreateSession(ctxOf("acme", "u1"), CreateSessionRequest{AgentKey: AgentKeyRuntimeDiagnosis, Target: TargetRef{Type: "workload", ID: "wl-review"}})
	if err != nil {
		t.Fatal(err)
	}
	return svc, repo, sess
}

func TestRegressionCancelledQueuedRunMustNotExecute(t *testing.T) {
	svc, repo, sess := reviewSetup(t)
	ctx := ctxOf("acme", "u1")
	r, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "review"}, "cancel-review")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Cancel(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	w := NewWorker(svc, repo, Deps{})
	called := false
	w.execFn = func(context.Context, Deps, agentRunInput, func(string, bool)) (ExecuteResult, error) {
		called = true
		return ExecuteResult{}, nil
	}
	claim, err := repo.ClaimNext(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claim != nil {
		w.execute(context.Background(), *claim)
	}
	if called {
		t.Fatal("已取消的 queued 任务仍进入执行器，仍会消耗模型与工具调用")
	}
}

type reviewBarrierRepo struct {
	Repository
	gate *sync.WaitGroup
}

func (r reviewBarrierRepo) CountActive(ctx context.Context) (int, error) {
	n, e := r.Repository.CountActive(ctx)
	r.gate.Done()
	r.gate.Wait()
	return n, e
}

func TestRegressionConcurrentAdmissionMustBeAtomic(t *testing.T) {
	svc, repo, sess := reviewSetup(t)
	gate := &sync.WaitGroup{}
	gate.Add(2)
	svc.repo = reviewBarrierRepo{repo, gate}
	errs := make(chan error, 2)
	for _, key := range []string{"concurrent-1", "concurrent-2"} {
		go func(k string) {
			_, e := svc.CreateRun(ctxOf("acme", "u1"), sess.ID, CreateRunRequest{Question: "review"}, k)
			errs <- e
		}(key)
	}
	successes := 0
	for range 2 {
		if <-errs == nil {
			successes++
		}
	}
	if successes > 1 {
		t.Fatalf("同一会话/租户并发受理 %d 个任务，约束应为 1", successes)
	}
}

func TestRegressionNoUserMustNotReadOwnedSession(t *testing.T) {
	svc, _, sess := reviewSetup(t)
	if _, e := svc.GetSession(ctxOf("acme", ""), sess.ID); e == nil {
		t.Fatal("无 user ID 的调用可读 u1 私有会话")
	}
}

func TestRegressionQuestionMustBeRedactedBeforePersistence(t *testing.T) {
	svc, _, sess := reviewSetup(t)
	r, e := svc.CreateRun(ctxOf("acme", "u1"), sess.ID, CreateRunRequest{Question: "password=review-secret-value 请排查"}, "mask-review")
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(r.Question, "review-secret-value") {
		t.Fatal("用户问题中的密码未脱敏即持久化并进入模型输入")
	}
}

func TestRegressionErrorEvidenceMustNotBecomeSufficient(t *testing.T) {
	evidence := []Evidence{{ID: "ev-1", Tool: "get_pod_logs", Status: "error"}, {ID: "ev-2", Tool: "get_pod_metrics", Status: "missing"}}
	_, q := validateUserReport(`{"verdict":{"severity":"info","summary":"系统健康"},"hypotheses":[{"hypothesis":"无异常","confidence":0.9,"evidenceIds":["ev-1","ev-2"]}]}`, evidence)
	if q == QualitySufficient {
		t.Fatal("仅 error/missing 的两条证据被评为 sufficient")
	}
}

func TestRegressionInvalidHypothesesMustClearHealthyVerdict(t *testing.T) {
	r, q := validateUserReport(`{"verdict":{"severity":"info","summary":"系统健康，无异常"},"hypotheses":[{"hypothesis":"healthy","confidence":0.9,"evidenceIds":["invented"]}]}`, nil)
	if q == QualityInconclusive && strings.Contains(r.Verdict.Summary, "系统健康") {
		t.Fatal("假设全部剔除后，报告顶部仍显示系统健康")
	}
}
