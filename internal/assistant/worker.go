package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/ai/agent"
	"github.com/aitoys/paas/pkg/provider"
	"github.com/aitoys/paas/pkg/tenant"
)

// Worker 管理持久化队列。执行器按官方 Agent key 分派，不与诊断 UI 绑定。
type Worker struct {
	svc    *Service
	deps   Deps
	repo   Repository
	execFn func(context.Context, Deps, agentRunInput, func(string, bool)) (ExecuteResult, error)
}

func NewWorker(svc *Service, repo Repository, deps Deps) *Worker {
	return &Worker{svc: svc, repo: repo, deps: deps}
}
func (w *Worker) Start(ctx context.Context, concurrency int) func() {
	if concurrency < 1 {
		concurrency = 2
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					w.tick(ctx)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		lastSweep := time.Time{}
		for {
			if time.Since(lastSweep) > time.Hour {
				if err := w.svc.SweepHistory(ctx); err != nil {
					log.Printf("[assistant] history cleanup: %v", err)
				}
				lastSweep = time.Now()
			}
			w.repo.ReclaimExpired(ctx, time.Now().UTC())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); wg.Wait() }
}
func (w *Worker) tick(ctx context.Context) {
	r, err := w.repo.ClaimNext(ctx, time.Now().UTC())
	if err != nil {
		log.Printf("[assistant] claim: %v", err)
		return
	}
	if r != nil {
		w.execute(ctx, *r)
	}
}

func (w *Worker) execute(ctx context.Context, claimed Run) {
	tctx := WithOwner(tenant.WithTenant(ctx, claimed.TenantID), claimed.OwnerUserID)
	final := claimed
	final.Model = w.deps.Model
	var usageMu sync.Mutex
	usage := RunUsage{UsageSource: "provider"}
	finish := func(status, code, msg string) {
		final.Status = status
		final.ErrorCode = code
		final.ErrorMsg = msg
		if msg != "" {
			final.Reply = msg
		}
		now := time.Now().UTC()
		final.FinishedAt = &now
		usageMu.Lock()
		final.Usage = usage
		usageMu.Unlock()
		if final.Usage.Tokens == 0 && len(final.Evidence) > 0 {
			final.Usage = estimateUsage(final.Question, final.Evidence)
		}
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(tctx), 5*time.Second)
		defer cancel()
		if err := w.repo.Finalize(saveCtx, final); err != nil && !errors.Is(err, ErrConflict) {
			log.Printf("[assistant] finalize run=%s: %v", claimed.ID, err)
		} else if err == nil {
			w.svc.audit(saveCtx, "assistant."+final.Status, claimed.ID)
		}
	}
	defer func() {
		if v := recover(); v != nil {
			log.Printf("[assistant] executor panic run=%s type=%T", claimed.ID, v)
			finish(StatusFailed, "executor_panic", "本轮执行异常，历史消息已保留，请重试。")
		}
	}()
	if claimed.CancelFlag {
		finish(StatusCancelled, "cancelled", "本轮已停止。")
		return
	}
	if time.Since(claimed.CreatedAt) > QueueWaitTimeout {
		finish(StatusFailed, "queued_timeout", "排队已超时，请重试。")
		return
	}
	sess, err := w.repo.GetSession(tctx, claimed.SessionID)
	if err != nil {
		finish(StatusFailed, "session_lost", "会话暂不可用。")
		return
	}
	if w.deps.Authorize != nil {
		if err = w.deps.Authorize(tctx, sess); err != nil {
			finish(StatusFailed, "forbidden", "当前用户已无权继续访问目标。")
			return
		}
	}
	runCtx, cancel := context.WithTimeout(tctx, RunTimeout)
	defer cancel()
	done := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		lastHeartbeat := time.Now()
		for {
			select {
			case <-done:
				return
			case <-runCtx.Done():
				return
			case <-ticker.C:
				current, e := w.repo.GetRun(tctx, claimed.ID)
				if e != nil || current.CancelFlag || IsTerminal(current.Status) {
					cancel()
					return
				}
				if time.Since(lastHeartbeat) >= 10*time.Second {
					if e = w.repo.Heartbeat(tctx, claimed.ID, claimed.LeaseOwner, time.Now().UTC()); e != nil {
						cancel()
						return
					}
					if w.deps.Authorize != nil {
						if e = w.deps.Authorize(tctx, sess); e != nil {
							cancel()
							return
						}
					}
					lastHeartbeat = time.Now()
				}
			}
		}
	}()
	w.emit(tctx, claimed.ID, EventRunStarted, nil)
	deps := w.deps
	originalRecord := deps.RecordUsage
	deps.RecordUsage = func(c context.Context, u RunUsage) {
		usageMu.Lock()
		usage.Tokens += u.Tokens
		if u.UsageSource != "provider" {
			usage.UsageSource = "estimated"
		}
		usageMu.Unlock()
		if originalRecord != nil {
			originalRecord(c, u)
		}
	}
	input := agentRunInput{Session: sess, Run: &claimed, History: w.svc.HistoryRuns(tctx, claimed.SessionID, HistoryTurns)}
	if claimed.Mode == "explain" {
		rows, e := w.repo.LatestRunsOfSession(tctx, claimed.SessionID, 20)
		if e == nil {
			for _, r := range rows {
				if r.Status == StatusCompleted && len(r.Report) > 0 {
					copy := r
					input.Previous = &copy
					break
				}
			}
		}
	}
	onTool := func(name string, start bool) {
		typ := EventToolCompleted
		if start {
			typ = EventToolStarted
		}
		w.emit(tctx, claimed.ID, typ, map[string]any{"tool": name})
	}
	if deps.Runner != nil {
		input.Invoke = func(c context.Context, preset agent.Agent, messages []provider.Message, tools agent.DiagnosticToolset) (string, error) {
			preset.Model = deps.Model
			hooks := modelHooks(c, deps, sess)
			hooks.OnToolStart = func(name string) { onTool(name, true) }
			hooks.OnToolEnd = func(name string, _ error) { onTool(name, false) }
			if tools == nil {
				tools = emptyTools{}
				hooks.NoTools = true
			}
			return deps.Runner.RunScoped(c, preset, messages, tools, hooks)
		}
	}
	exec := w.execFn
	if exec == nil {
		exec = Execute
		if registered := w.svc.executors[sess.AgentKey]; registered != nil {
			exec = func(c context.Context, _ Deps, in agentRunInput, emit func(string, bool)) (ExecuteResult, error) {
				return registered.Execute(c, in, emit)
			}
		}
	}
	res, execErr := exec(runCtx, deps, input, onTool)
	close(done)
	<-watchDone
	if current, e := w.repo.GetRun(tctx, claimed.ID); e == nil && current.CancelFlag {
		finish(StatusCancelled, "cancelled", "本轮已停止。可以继续追问或发起新的检查。")
		return
	}
	if runCtx.Err() != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			finish(StatusTimedOut, "timeout", "本轮检查超时，可以缩小范围后重试。")
		} else {
			finish(StatusFailed, "interrupted", "本轮执行被中断，未完成的推理不会自动重复。")
		}
		return
	}
	if execErr != nil {
		code, msg := classifyExecError(execErr)
		finish(StatusFailed, code, msg)
		return
	}
	final.Report = res.Artifact
	if len(final.Report) == 0 && res.Report.Kind != "" {
		final.Report, err = json.Marshal(res.Report)
		if err != nil {
			finish(StatusFailed, "report_marshal", "报告保存失败。")
			return
		}
	}
	final.Reply = res.Reply
	if final.Reply == "" {
		final.Reply = res.Report.Verdict.Summary
	}
	final.Evidence = res.Evidence
	final.Quality = res.Quality
	finish(StatusCompleted, "", "")
}
func (w *Worker) emit(ctx context.Context, id, typ string, p map[string]any) {
	var data []byte
	if p != nil {
		data, _ = json.Marshal(p)
	}
	if _, err := w.repo.AppendEvent(ctx, id, typ, data); err != nil {
		log.Printf("[assistant] event run=%s: %v", id, err)
	}
}
func eventOfStatus(s string) string {
	switch s {
	case StatusCancelled:
		return EventRunCancelled
	case StatusTimedOut:
		return EventRunTimedOut
	default:
		return EventRunFailed
	}
}
func classifyExecError(err error) (string, string) {
	if strings.HasPrefix(err.Error(), "target_replaced") {
		return "target_replaced", "目标或实例已被重新创建，请重新选择。"
	}
	return "exec_failed", "本轮分析未完成，请稍后重试或补充问题。"
}
func estimateUsage(q string, ev []Evidence) RunUsage {
	n := len(q)
	for _, e := range ev {
		n += len(e.Summary) + len(e.Snippet)
	}
	return RunUsage{Tokens: max(1, n/4), UsageSource: "estimated"}
}

// Executor 是后续官方 Agent 的接入契约，消息、队列、身份、取消、历史无需重复实现。
type Executor interface {
	Execute(context.Context, RunInput, func(string, bool)) (ExecuteResult, error)
}

// ExecutorFunc 允许将业务执行器以函数方式装配。
type ExecutorFunc func(context.Context, RunInput, func(string, bool)) (ExecuteResult, error)

func (f ExecutorFunc) Execute(c context.Context, in RunInput, emit func(string, bool)) (ExecuteResult, error) {
	return f(c, in, emit)
}
