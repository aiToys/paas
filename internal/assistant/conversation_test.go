package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestCancelRunningInterruptsExecutor(t *testing.T) {
	svc, repo, sess := reviewSetup(t)
	ctx := ctxOf("acme", "u1")
	run, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "检查"}, "running-cancel")
	if err != nil {
		t.Fatal(err)
	}
	claimed, _ := repo.ClaimNext(context.Background(), time.Now())
	started, done := make(chan struct{}), make(chan struct{})
	w := NewWorker(svc, repo, Deps{})
	w.execFn = func(c context.Context, _ Deps, _ RunInput, _ func(string, bool)) (ExecuteResult, error) {
		close(started)
		<-c.Done()
		return ExecuteResult{}, c.Err()
	}
	go func() { defer close(done); w.execute(context.Background(), *claimed) }()
	<-started
	if _, err = svc.Cancel(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("停止未中断模型/工具 ctx")
	}
	result, _ := repo.GetRun(ctx, run.ID)
	if result.Status != StatusCancelled {
		t.Fatalf("停止后终态错误 %s", result.Status)
	}
}

func TestWorkerPanicDoesNotEscape(t *testing.T) {
	svc, repo, sess := reviewSetup(t)
	ctx := ctxOf("acme", "u1")
	run, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "检查"}, "panic")
	if err != nil {
		t.Fatal(err)
	}
	claimed, _ := repo.ClaimNext(context.Background(), time.Now())
	w := NewWorker(svc, repo, Deps{})
	w.execFn = func(context.Context, Deps, RunInput, func(string, bool)) (ExecuteResult, error) { panic("test") }
	w.execute(context.Background(), *claimed)
	result, _ := repo.GetRun(ctx, run.ID)
	if result.Status != StatusFailed || result.ErrorCode != "executor_panic" {
		t.Fatalf("panic 未收敛 %+v", result)
	}
}

func TestConversationRestoresThreeTurns(t *testing.T) {
	svc, repo, sess := reviewSetup(t)
	ctx := ctxOf("acme", "u1")
	worker := NewWorker(svc, repo, Deps{})
	worker.execFn = func(_ context.Context, _ Deps, in RunInput, _ func(string, bool)) (ExecuteResult, error) {
		return ExecuteResult{Reply: "回答：" + in.Run.Question}, nil
	}
	for i := 0; i < 3; i++ {
		_, err := svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: fmt.Sprintf("问题%d", i), Mode: "explain"}, fmt.Sprintf("turn-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		r, err := repo.ClaimNext(context.Background(), time.Now())
		if err != nil || r == nil {
			t.Fatal(err)
		}
		worker.execute(context.Background(), *r)
	}
	for i := 0; i < 2; i++ {
		c, err := svc.Messages(ctx, sess.ID, time.Time{}, 20)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Messages) != 6 {
			t.Fatalf("刷新后应恢复三轮六条消息，got %d", len(c.Messages))
		}
		for n := 0; n < 3; n++ {
			u, a := c.Messages[n*2], c.Messages[n*2+1]
			if u.Role != "user" || a.Role != "assistant" || a.Text != "回答："+u.Text || a.Status != StatusCompleted {
				t.Fatalf("消息错序/丢失 %+v %+v", u, a)
			}
		}
	}
	c, _ := svc.Messages(ctx, sess.ID, time.Time{}, 2)
	if len(c.Messages) != 4 || !c.HasMore {
		t.Fatalf("分页必须成对且给出续读游标 %+v", c)
	}
	before, err := time.Parse(time.RFC3339Nano, c.Before)
	if err != nil {
		t.Fatal(err)
	}
	old, err := svc.Messages(ctx, sess.ID, before, 2)
	if err != nil || len(old.Messages) != 2 || old.HasMore {
		t.Fatalf("历史续读失败 %+v %v", old, err)
	}
	if _, err = svc.Messages(ctxOf("acme", "u2"), sess.ID, time.Time{}, 20); err == nil {
		t.Fatal("其他用户不能恢复此对话")
	}
}

func TestOfficialAgentUsesSharedConversationRuntime(t *testing.T) {
	svc, repo, _ := reviewSetup(t)
	ctx := ctxOf("acme", "u1")
	err := svc.RegisterAgent(AgentDesc{Key: "builtin:test-check", Name: "测试检查", TargetType: "workload", ReadOnly: true}, ExecutorFunc(func(_ context.Context, in RunInput, _ func(string, bool)) (ExecuteResult, error) {
		return ExecuteResult{Reply: "另一种 Agent 的回答", Artifact: json.RawMessage(`{"kind":"test_result","version":"1","summary":"检查摘要"}`)}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := svc.CreateSession(ctx, CreateSessionRequest{AgentKey: "builtin:test-check", Target: TargetRef{Type: "workload", ID: "wl-review"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.CreateRun(ctx, sess.ID, CreateRunRequest{Question: "检查"}, "other-agent"); err != nil {
		t.Fatal(err)
	}
	r, _ := repo.ClaimNext(context.Background(), time.Now())
	NewWorker(svc, repo, Deps{}).execute(context.Background(), *r)
	c, err := svc.Messages(ctx, sess.ID, time.Time{}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Messages) != 2 || c.Messages[1].Text != "另一种 Agent 的回答" || c.Messages[1].ResultKind != "test_result" {
		t.Fatalf("扩展 Agent 未复用统一协议 %+v", c)
	}
}

func TestHistoryFiltersOwnerBeforePagination(t *testing.T) {
	svc, _, sess := reviewSetup(t)
	for i := 0; i < 30; i++ {
		_, err := svc.CreateSession(ctxOf("acme", "u2"), CreateSessionRequest{AgentKey: AgentKeyRuntimeDiagnosis, Target: TargetRef{Type: "workload", ID: "wl-review"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	list, err := svc.ListSessions(ctxOf("acme", "u1"), time.Time{}, 20)
	if err != nil || len(list) != 1 || list[0].ID != sess.ID {
		t.Fatalf("其他用户活跃不应挤掉自己的历史 %+v %v", list, err)
	}
}
