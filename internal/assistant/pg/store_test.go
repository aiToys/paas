//go:build integration

package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/assistant"
	"github.com/aitoys/paas/internal/diagnose"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// TestAssistantMigrationPreservesRLS 防止对话升级关闭原有数据库租户防线。
func TestAssistantMigrationPreservesRLS(t *testing.T) {
	s, cleanup := open(t)
	defer cleanup()
	ctx := context.Background()
	var enabled int
	if err := s.db.Pool().QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relnamespace='public'::regnamespace AND relname IN ('assistant_sessions','assistant_runs','assistant_run_events') AND relrowsecurity`).Scan(&enabled); err != nil || enabled != 3 {
		t.Fatalf("助手三表必须保留 RLS: count=%d err=%v", enabled, err)
	}
	if err := s.CreateSession(ctx, mkSession("rls-probe", "t-acme", time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// 角色与授权在测试事务回滚时一并撤销，不改变现有 Core 运行角色。
	for _, sql := range []string{
		`CREATE ROLE assistant_rls_probe NOLOGIN NOSUPERUSER NOBYPASSRLS`,
		`GRANT USAGE ON SCHEMA public TO assistant_rls_probe`,
		`GRANT SELECT ON assistant_sessions TO assistant_rls_probe`,
		`SET LOCAL ROLE assistant_rls_probe`,
	} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM assistant_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("缺少租户参数必须拒绝全部行: count=%d err=%v", count, err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id','t-acme',true)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM assistant_sessions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("同租户行应可见: count=%d err=%v", count, err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id','t-globex',true)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM assistant_sessions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("跨租户行必须不可见: count=%d err=%v", count, err)
	}
}

// TestConversationPersistenceAndFencing 验证回答/证据与终态原子保存，重建 Store 后仍可恢复。
func TestConversationPersistenceAndFencing(t *testing.T) {
	s, cleanup := open(t)
	defer cleanup()
	ctx := assistant.WithOwner(tenant.WithTenant(context.Background(), "t-acme"), "u1")
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := s.CreateSession(ctx, mkSession("chat", "t-acme", now)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("turn-%d", i)
		r := mkRun(id, "chat", "t-acme", now.Add(time.Duration(i)*time.Millisecond))
		r.IdempotencyKey = id
		if err := s.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
		claimed, err := s.ClaimNext(context.Background(), now)
		if err != nil || claimed == nil {
			t.Fatalf("claim %v", err)
		}
		wrong := *claimed
		wrong.LeaseOwner = "stale-worker"
		wrong.Status = assistant.StatusCompleted
		if err = s.Finalize(ctx, wrong); !errors.Is(err, assistant.ErrConflict) {
			t.Fatalf("fencing 必须拒绝旧 Worker: %v", err)
		}
		claimed.Status = assistant.StatusCompleted
		claimed.Evidence = []assistant.Evidence{{ID: "ev-1", Status: "ok", Snippet: "有界日志片段", ContentHash: "hash"}}
		claimed.Reply = "已保存的自然语言回答"
		claimed.Report = json.RawMessage(`{"kind":"diagnosis_report","version":"1"}`)
		claimed.FinishedAt = &now
		if err = s.Finalize(ctx, *claimed); err != nil {
			t.Fatal(err)
		}
	}
	reopened := New(s.db)
	rows, err := reopened.ConversationRuns(ctx, "chat", time.Time{}, 20)
	if err != nil || len(rows) != 3 {
		t.Fatalf("重建后历史丢失 %d %v", len(rows), err)
	}
	for _, r := range rows {
		if r.Reply != "已保存的自然语言回答" || r.Status != assistant.StatusCompleted {
			t.Fatalf("回答未持久化 %+v", r)
		}
	}
	other := assistant.WithOwner(ctx, "u2")
	rows, err = reopened.ConversationRuns(other, "chat", time.Time{}, 20)
	if err != nil || len(rows) != 0 {
		t.Fatalf("跨用户可见 %v %v", rows, err)
	}
	if err := reopened.Sweep(ctx, now.Add(-time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	after, err := reopened.GetRun(ctx, "turn-0")
	if err != nil || len(after.Evidence) != 1 || after.Evidence[0].Snippet != "" || after.Reply == "" {
		t.Fatalf("片段过期不得删除对话 %+v %v", after, err)
	}
	events, err := reopened.ListEvents(ctx, "turn-0", 0)
	if err != nil || len(events) != 1 || events[0].Type != assistant.EventReportReady {
		t.Fatalf("结果与终态事件未一并保存 %v %v", events, err)
	}
}

// open 创建测试 DB 连接并跑迁移；结束清 assistant 三表 + schema_migrations
// （与其他模块 pg 测试同款模式；必须连 schema_migrations 一起清）。
func open(t *testing.T) (*Store, func()) {
	t.Helper()
	dsn := os.Getenv("PAAS_TEST_PG_URL")
	if dsn == "" {
		t.Skip("PAAS_TEST_PG_URL 未设置，跳过 PG 集成测试")
	}
	db, err := storagepg.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("打开测试 DB 失败: %v", err)
	}
	if _, err := db.Pool().Exec(context.Background(), `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := storagepg.RunMigrations(context.Background(), db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	cleanup := func() {
		db.Pool().Exec(context.Background(), `DROP TABLE IF EXISTS assistant_run_events CASCADE;
			DROP TABLE IF EXISTS assistant_runs CASCADE; DROP TABLE IF EXISTS assistant_sessions CASCADE;
			DROP TABLE IF EXISTS schema_migrations CASCADE`)
		db.Close()
	}
	return New(db), cleanup
}

func mkSession(id, tid string, at time.Time) assistant.Session {
	return assistant.Session{
		ID: id, TenantID: tid, OwnerUserID: "u1",
		AgentKey: "builtin:runtime-diagnosis", AgentVersion: "v1",
		Target: diagnose.AuthorizedTarget{
			Type: "workload", WorkloadID: "wl-1", WorkloadName: "wl-1",
			Namespace: tenant.Namespace(tid), ResolvedAt: at,
		},
		CreatedAt: at, UpdatedAt: at,
	}
}

func mkRun(id, sessID, tid string, at time.Time) assistant.Run {
	return assistant.Run{
		ID: id, SessionID: sessID, TenantID: tid, OwnerUserID: "u1",
		Question: "服务健康吗", Status: assistant.StatusQueued,
		WindowStart: at.Add(-30 * time.Minute), WindowEnd: at,
		CreatedAt: at,
	}
}

// TestClaimNextFlow Worker 队列核心链（集群曾发 queued 永不消费，此处锁 SQL/scan 对齐）：
// 领取→租约→CAS 终态→事件 seq 单调→幂等键查→owner 过滤。
func TestClaimNextFlow(t *testing.T) {
	s, cleanup := open(t)
	defer cleanup()

	ctx := tenant.WithTenant(context.Background(), "t-acme")
	base := time.Now().UTC().Truncate(time.Microsecond)
	if err := s.CreateSession(ctx, mkSession("sess-1", "t-acme", base)); err != nil {
		t.Fatalf("建会话: %v", err)
	}
	// 两条 queued（created_at 错开 1ms，ClaimNext 应取最早）。
	if err := s.CreateRun(ctx, mkRun("run-1", "sess-1", "t-acme", base)); err != nil {
		t.Fatalf("建 run-1: %v", err)
	}

	// 事件先落一条，验证 seq 从 1 单调（worker emit 与前端 afterSeq 增量的前提）。
	if _, err := s.AppendEvent(ctx, "run-1", assistant.EventRunQueued, nil); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	now := base.Add(2 * time.Second)
	got, err := s.ClaimNext(context.Background(), now) // worker 无租户 ctx
	if err != nil {
		t.Fatalf("ClaimNext 报错（集群 queued 卡死的嫌疑面）: %v", err)
	}
	if got == nil || got.ID != "run-1" {
		t.Fatalf("ClaimNext 应取最早 run-1, got %+v", got)
	}
	if got.Status != assistant.StatusRunning || got.StartedAt == nil {
		t.Fatalf("领取后应为 running+startedAt: %+v", got)
	}
	persist, err := s.GetRun(ctx, "run-1")
	if err != nil || persist.Status != assistant.StatusRunning {
		t.Fatalf("领取未落库: %+v err=%v", persist, err)
	}

	// CAS 终态：from=queued/running 允许；再次 CAS（from 含 running 但已 completed）→ 冲突。
	fin := now.Add(30 * time.Second)
	err = s.UpdateRunStatus(ctx, "run-1", []string{assistant.StatusQueued, assistant.StatusRunning}, func(r *assistant.Run) {
		r.Status = assistant.StatusCompleted
		r.Quality = assistant.QualitySufficient
		r.FinishedAt = &fin
	})
	if err != nil {
		t.Fatalf("CAS completed: %v", err)
	}
	if err := s.UpdateRunStatus(ctx, "run-1", []string{assistant.StatusRunning}, func(r *assistant.Run) {
		r.Status = assistant.StatusFailed
	}); !errors.Is(err, assistant.ErrConflict) {
		t.Fatalf("终态后 CAS 应 ErrConflict, got %v", err)
	}

	// 第一轮完成释放名额后，第二轮可领取；心跳续约后回收不误伤。
	if err := s.CreateRun(ctx, mkRun("run-2", "sess-1", "t-acme", base.Add(time.Millisecond))); err != nil {
		t.Fatalf("建 run-2: %v", err)
	}

	got2, err := s.ClaimNext(context.Background(), now)
	if err != nil || got2 == nil || got2.ID != "run-2" {
		t.Fatalf("ClaimNext run-2: %+v err=%v", got2, err)
	}
	if err := s.Heartbeat(context.Background(), "run-2", got2.LeaseOwner, now); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	// 续约后 lease=now+90s：以 now+5s 视角回收应零命中；以 now+10min 视角应回收 run-2。
	if n := s.ReclaimExpired(context.Background(), now.Add(5*time.Second)); n != 0 {
		t.Fatalf("心跳未过期不应回收, got %d", n)
	}
	if n := s.ReclaimExpired(context.Background(), now.Add(10*time.Minute)); n != 1 {
		t.Fatalf("lease 过期应回收 run-2, got %d", n)
	}

	// 队列空：nil 不报错。
	if next, err := s.ClaimNext(context.Background(), now); err != nil || next != nil {
		t.Fatalf("空队列应 nil, got %+v err=%v", next, err)
	}

	// 事件 seq 单调 + afterSeq 增量。
	for i := 0; i < 3; i++ {
		if _, err := s.AppendEvent(ctx, "run-1", assistant.EventToolStarted, []byte(`{"tool":"k8s.spec"}`)); err != nil {
			t.Fatalf("AppendEvent×3: %v", err)
		}
	}
	evs, err := s.ListEvents(ctx, "run-1", 0)
	if err != nil || len(evs) != 4 {
		t.Fatalf("事件应 4 条, got %d err=%v", len(evs), err)
	}
	for i, e := range evs {
		if e.Seq != i+1 {
			t.Fatalf("seq 应单调 1..4, got[%d]=%d", i, e.Seq)
		}
	}
	tail, _ := s.ListEvents(ctx, "run-1", 2)
	if len(tail) != 2 || tail[0].Seq != 3 {
		t.Fatalf("afterSeq=2 应返 seq3,4, got %v", tail)
	}
}

// TestRunOwnerAndIdempotencyScope 隔离与幂等键作用域（tenant+user+session）。
func TestRunOwnerAndIdempotencyScope(t *testing.T) {
	s, cleanup := open(t)
	defer cleanup()

	acme := tenant.WithTenant(context.Background(), "t-acme")
	globex := tenant.WithTenant(context.Background(), "t-globex")
	base := time.Now().UTC().Truncate(time.Microsecond)
	_ = s.CreateSession(acme, mkSession("sess-a", "t-acme", base))
	r := mkRun("run-x", "sess-a", "t-acme", base)
	r.IdempotencyKey = "key-1"
	if err := s.CreateRun(acme, r); err != nil {
		t.Fatalf("建 run: %v", err)
	}

	// 跨租户读 run/session 统一 ErrNotFound（404 不泄漏）。
	if _, err := s.GetRun(globex, "run-x"); !errors.Is(err, assistant.ErrNotFound) {
		t.Fatalf("跨租户 GetRun 应 NotFound, got %v", err)
	}
	if _, err := s.GetSession(globex, "sess-a"); !errors.Is(err, assistant.ErrNotFound) {
		t.Fatalf("跨租户 GetSession 应 NotFound, got %v", err)
	}

	// 幂等键命中。
	found, err := s.FindByIdempotencyKey(acme, "t-acme", "u1", "sess-a", "key-1")
	if err != nil || found == nil || found.ID != "run-x" {
		t.Fatalf("幂等键查失败: %+v err=%v", found, err)
	}
	// 错 owner / 错 session 不命中。
	if found, _ := s.FindByIdempotencyKey(acme, "t-acme", "u2", "sess-a", "key-1"); found != nil {
		t.Fatalf("错 owner 不应命中: %+v", found)
	}
	if found, _ := s.FindByIdempotencyKey(acme, "t-acme", "u1", "sess-b", "key-1"); found != nil {
		t.Fatalf("错 session 不应命中: %+v", found)
	}
}

// TestSessionContextRoundtrip 上下文列（migration 0062）读写 + draft→resolved 更新：
// draft 会话（零值 target + need_choice context）建档、UpdateSession 落 resolved、
// 回读 target/context 同构；旧会话 context NULL 兼容（nil 不炸）。
func TestSessionContextRoundtrip(t *testing.T) {
	s, cleanup := open(t)
	defer cleanup()
	ctx := assistant.WithOwner(tenant.WithTenant(context.Background(), "t-acme"), "u1")
	at := time.Now().UTC().Truncate(time.Second)

	// draft 会话：target 零值 + need_choice 上下文（告警 app 级多候选形态）。
	ws, we := at.Add(-15*time.Minute), at
	draft := mkSession("sess-draft", "t-acme", at)
	draft.Target = diagnose.AuthorizedTarget{}
	draft.Context = &assistant.DiagnosticContext{
		Anchors:          []assistant.DiagnosticAnchor{{Kind: "alert", ID: "rule-abc123"}},
		Source:           "alert",
		WindowStart:      &ws, WindowEnd:      &we,
		ResolutionStatus: assistant.ResolutionNeedChoice, ResolutionReason: "匹配到多个服务",
	}
	if err := s.CreateSession(ctx, draft); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionResolved() {
		t.Fatal("draft 会话不得被读成 resolved")
	}
	if got.Context == nil || got.Context.ResolutionStatus != assistant.ResolutionNeedChoice || len(got.Context.Anchors) != 1 {
		t.Fatalf("draft context roundtrip 失败: %+v", got.Context)
	}

	// draft→resolved：UpdateSession 落 target+resolved context。
	now := time.Now().UTC().Truncate(time.Second)
	got.Target = diagnose.AuthorizedTarget{
		Type: "workload", WorkloadID: "wl-9", WorkloadName: "shop-web",
		AppID: "app-1", Namespace: tenant.Namespace("t-acme"), ResolvedAt: now,
	}
	got.Context = &assistant.DiagnosticContext{
		Anchors:          []assistant.DiagnosticAnchor{{Kind: "alert", ID: "rule-abc123"}, {Kind: "workload", ID: "wl-9"}},
		Source:           "alert", Resolved: &got.Target,
		WindowStart:      &ws, WindowEnd:      &we,
		ResolutionStatus: assistant.ResolutionResolved,
	}
	got.UpdatedAt = now
	if err := s.UpdateSession(ctx, got); err != nil {
		t.Fatal(err)
	}
	final, err := s.GetSession(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !final.SessionResolved() || final.Target.WorkloadID != "wl-9" || final.Context == nil || final.Context.Source != "alert" {
		t.Fatalf("resolved roundtrip 失败: target=%+v context=%+v", final.Target, final.Context)
	}

	// 旧会话（无 context，0062 之前建档形态）：context NULL → nil，不炸。
	legacy := mkSession("sess-legacy", "t-acme", at)
	if err := s.CreateSession(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	lg, err := s.GetSession(ctx, legacy.ID)
	if err != nil || lg.Context != nil || !lg.SessionResolved() {
		t.Fatalf("legacy session should be resolved with nil context: err=%v ctx=%+v", err, lg.Context)
	}

	// 跨租户/owner 更新拒绝（影响行 0 → ErrNotFound）。
	other := tenant.WithTenant(context.Background(), "t-globex")
	if err := s.UpdateSession(other, final); !errors.Is(err, assistant.ErrNotFound) {
		t.Fatalf("cross-tenant update must be ErrNotFound, got %v", err)
	}
	_ = json.Marshal // 保持与既有 import 集一致
}
