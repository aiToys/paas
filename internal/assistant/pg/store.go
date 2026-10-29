// Package pg 提供 assistant.Repository 的 PostgreSQL 实现（migration 0060）。
// 所有读写强制 tenant 过滤（SQL WHERE 显式）；ClaimNext 用 FOR UPDATE SKIP LOCKED
// 原子领取；lease/heartbeat 支撑多副本 Worker（ReclaimExpired 只标失败不自动重跑
// 付费模型）。owner 过滤在 service 层（GetSession/GetRun 后比对），SQL 层租户硬隔离。
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/assistant"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store assistant PG 仓储。
type Store struct {
	db *storagepg.DB
}

// New 创建。db 必须已完成迁移（0060）。
func New(db *storagepg.DB) *Store { return &Store{db: db} }

const sessCols = `id, tenant_id, owner_user_id, agent_key, agent_version, target, context, focus, created_at, updated_at`

func scanSession(r storagepg.RowScanner, s *assistant.Session) error {
	var target, focus, context []byte
	if err := r.Scan(&s.ID, &s.TenantID, &s.OwnerUserID, &s.AgentKey, &s.AgentVersion,
		&target, &context, &focus, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return err
	}
	if err := json.Unmarshal(target, &s.Target); err != nil {
		return fmt.Errorf("session target: %w", err)
	}
	if len(context) > 0 {
		_ = json.Unmarshal(context, &s.Context)
	}
	if len(focus) > 0 {
		_ = json.Unmarshal(focus, &s.Focus)
	}
	return nil
}

const runCols = `id, session_id, tenant_id, owner_user_id, question, symptom, parent_run_id,
	window_start, window_end, status, quality, report, evidence, model,
	usage_tokens, usage_source, idempotency_key, request_hash, cancel_flag,
	error_code, error_msg, lease_owner, created_at, started_at, finished_at, reply, mode, lease_expires`

func scanRun(r storagepg.RowScanner, run *assistant.Run) error {
	var evidence []byte

	var startedAt, finishedAt, leaseExpires *time.Time
	if err := r.Scan(&run.ID, &run.SessionID, &run.TenantID, &run.OwnerUserID,
		&run.Question, &run.Symptom, &run.ParentRunID,
		&run.WindowStart, &run.WindowEnd, &run.Status, &run.Quality, &run.Report, &evidence, &run.Model,
		&run.Usage.Tokens, &run.Usage.UsageSource, &run.IdempotencyKey, &run.RequestHash, &run.CancelFlag,
		&run.ErrorCode, &run.ErrorMsg, &run.LeaseOwner, &run.CreatedAt, &startedAt, &finishedAt, &run.Reply, &run.Mode, &leaseExpires); err != nil {
		return err
	}
	if len(evidence) > 0 {
		_ = json.Unmarshal(evidence, &run.Evidence)
	}
	run.StartedAt, run.FinishedAt = startedAt, finishedAt
	if leaseExpires != nil {
		run.LeaseExpires = *leaseExpires
	}
	return nil
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return assistant.ErrNotFound
	}
	return err
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// —— 会话 ——

func (s *Store) CreateSession(ctx context.Context, sess assistant.Session) error {
	target, err := json.Marshal(sess.Target)
	if err != nil {
		return err
	}
	var focus []byte
	if sess.Focus != (assistant.FocusRef{}) {
		if focus, err = json.Marshal(sess.Focus); err != nil {
			return err
		}
	}
	var context []byte
	if sess.Context != nil {
		if context, err = json.Marshal(sess.Context); err != nil {
			return err
		}
	}
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO assistant_sessions (id, tenant_id, owner_user_id, agent_key, agent_version, target, context, focus, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		sess.ID, sess.TenantID, sess.OwnerUserID, sess.AgentKey, sess.AgentVersion, target, context, focus, sess.CreatedAt, sess.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create assistant session: %w", err)
	}
	return nil
}

// UpdateSession 更新可变字段（draft→resolved 上下文落库；身份字段不可变）。
// WHERE 租户/owner 从 ctx 取（仓储强制过滤，不信任调用方传入的 sess 字段）；
// 影响行数 0 = 会话不存在/不属于当前租户+owner（统一 ErrNotFound 不泄漏）。
func (s *Store) UpdateSession(ctx context.Context, sess assistant.Session) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	target, err := json.Marshal(sess.Target)
	if err != nil {
		return err
	}
	var context []byte
	if sess.Context != nil {
		if context, err = json.Marshal(sess.Context); err != nil {
			return err
		}
	}
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE assistant_sessions SET target=$3, context=$4, focus=$5, updated_at=$6
		 WHERE id=$1 AND tenant_id=$2 AND owner_user_id=$7`,
		sess.ID, tid, target, context, focusJSON(sess.Focus), sess.UpdatedAt, assistant.OwnerFrom(ctx))
	if err != nil {
		return fmt.Errorf("update assistant session: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return assistant.ErrNotFound
	}
	return nil
}

func focusJSON(f assistant.FocusRef) []byte {
	if f == (assistant.FocusRef{}) {
		return nil
	}
	b, _ := json.Marshal(f)
	return b
}

func (s *Store) GetSession(ctx context.Context, id string) (assistant.Session, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return assistant.Session{}, err
	}
	var sess assistant.Session
	err = scanSession(s.db.Pool().QueryRow(ctx,
		`SELECT `+sessCols+` FROM assistant_sessions WHERE id=$1 AND tenant_id=$2 AND ($3='' OR owner_user_id=$3)`, id, tid, assistant.OwnerFrom(ctx)), &sess)
	if err != nil {
		return assistant.Session{}, notFound(err)
	}
	return sess, nil
}

func (s *Store) ListSessions(ctx context.Context, before time.Time, limit int) ([]assistant.Session, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+sessCols+` FROM assistant_sessions
		 WHERE tenant_id=$1 AND ($2::timestamptz IS NULL OR updated_at < $2)
		 AND owner_user_id=$4 ORDER BY updated_at DESC LIMIT $3`, tid, nullTime(before), limit, assistant.OwnerFrom(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:staticcheck
	var out []assistant.Session
	for rows.Next() {
		var sess assistant.Session
		if err := scanSession(rows, &sess); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// —— run ——

func (s *Store) CreateRun(ctx context.Context, run assistant.Run) error {
	// 全局短事务准入锁覆盖检查、幂等与入队，跨副本不超额；不在模型执行期间持锁。
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(2026102801)`); err != nil {
		return err
	}
	if run.IdempotencyKey != "" {
		var found int
		err = tx.QueryRow(ctx, `SELECT COUNT(*) FROM assistant_runs WHERE tenant_id=$1 AND owner_user_id=$2 AND session_id=$3 AND idempotency_key=$4`, run.TenantID, run.OwnerUserID, run.SessionID, run.IdempotencyKey).Scan(&found)
		if err != nil {
			return err
		}
		if found > 0 {
			return assistant.ErrReplay
		}
	}
	var total, local, session int
	err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE tenant_id=$1),count(*) FILTER(WHERE session_id=$2) FROM assistant_runs WHERE status IN ('queued','running')`, run.TenantID, run.SessionID).Scan(&total, &local, &session)
	if err != nil {
		return err
	}
	if total >= assistant.LimitGlobalConcurrent || local >= assistant.LimitTenantConcurrent || session > 0 {
		return assistant.ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO assistant_runs(id,session_id,tenant_id,owner_user_id,question,symptom,parent_run_id,window_start,window_end,status,idempotency_key,request_hash,created_at,mode) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, run.ID, run.SessionID, run.TenantID, run.OwnerUserID, run.Question, run.Symptom, run.ParentRunID, run.WindowStart, run.WindowEnd, run.Status, run.IdempotencyKey, run.RequestHash, run.CreatedAt, run.Mode)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE assistant_sessions SET updated_at=$2 WHERE id=$1 AND tenant_id=$3 AND owner_user_id=$4`, run.SessionID, run.CreatedAt, run.TenantID, run.OwnerUserID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) GetRun(ctx context.Context, id string) (assistant.Run, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return assistant.Run{}, err
	}
	var run assistant.Run
	if err := scanRun(s.db.Pool().QueryRow(ctx,
		`SELECT `+runCols+` FROM assistant_runs WHERE id=$1 AND tenant_id=$2 AND ($3='' OR owner_user_id=$3)`, id, tid, assistant.OwnerFrom(ctx)), &run); err != nil {
		return assistant.Run{}, notFound(err)
	}
	return run, nil
}

func (s *Store) ActiveRunOfSession(ctx context.Context, sessionID string) (*assistant.Run, error) {
	var run assistant.Run
	err := scanRun(s.db.Pool().QueryRow(ctx,
		`SELECT `+runCols+` FROM assistant_runs WHERE session_id=$1 AND tenant_id=$2 AND status IN ('queued','running') LIMIT 1`, sessionID, tenantID(ctx)), &run)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) LatestRunsOfSession(ctx context.Context, sessionID string, limit int) ([]assistant.Run, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+runCols+` FROM assistant_runs WHERE session_id=$1 AND tenant_id=$3 ORDER BY created_at DESC LIMIT $2`, sessionID, limit, tenantID(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:staticcheck
	var out []assistant.Run
	for rows.Next() {
		var run assistant.Run
		if err := scanRun(rows, &run); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *Store) FindByIdempotencyKey(ctx context.Context, tenantID, userID, sessionID, key string) (*assistant.Run, error) {
	if key == "" {
		return nil, nil
	}
	var run assistant.Run
	err := scanRun(s.db.Pool().QueryRow(ctx,
		`SELECT `+runCols+` FROM assistant_runs
		 WHERE tenant_id=$1 AND owner_user_id=$2 AND session_id=$3 AND idempotency_key=$4 LIMIT 1`,
		tenantID, userID, sessionID, key), &run)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) UpdateRunStatus(ctx context.Context, id string, from []string, mutate func(*assistant.Run)) error {
	// 事务 SELECT ... FOR UPDATE + 状态断言（CAS）；终态不可覆盖（迟到 Worker 丢弃）。
	ctx2 := ctx
	tx, err := s.db.Pool().Begin(ctx2)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx2) //nolint:errcheck
	var run assistant.Run
	if err := scanRun(tx.QueryRow(ctx2,
		`SELECT `+runCols+` FROM assistant_runs WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, id, tenantID(ctx)), &run); err != nil {
		return notFound(err)
	}
	if len(from) > 0 {
		ok := false
		for _, f := range from {
			if run.Status == f {
				ok = true
				break
			}
		}
		if !ok {
			return assistant.ErrConflict
		}
	}
	if assistant.IsTerminal(run.Status) {
		terminal := run.Status
		mutate(&run) // 终态保护：允许补写用量等字段，状态不回退
		run.Status = terminal
	} else {
		mutate(&run)
	}
	var evidence any
	if len(run.Evidence) > 0 {
		evidence = run.Evidence
	}
	var report any
	if len(run.Report) > 0 {
		report = run.Report
	}
	if _, err := tx.Exec(ctx2,
		`UPDATE assistant_runs SET status=$2, quality=$3, report=$4, evidence=$5, usage_tokens=$6,
		 usage_source=$7, cancel_flag=$8, error_code=$9, error_msg=$10, started_at=$11, finished_at=$12, reply=$13, mode=$14
		 WHERE id=$1`,
		run.ID, run.Status, run.Quality, report, evidence, run.Usage.Tokens,
		run.Usage.UsageSource, run.CancelFlag, run.ErrorCode, run.ErrorMsg, run.StartedAt, run.FinishedAt, run.Reply, run.Mode); err != nil {
		return err
	}
	return tx.Commit(ctx2)
}

func (s *Store) AppendEvent(ctx context.Context, runID, typ string, payload []byte) (assistant.Event, error) {
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return assistant.Event{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id string
	if err = tx.QueryRow(ctx, `SELECT id FROM assistant_runs WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, runID, tenantID(ctx)).Scan(&id); err != nil {
		return assistant.Event{}, notFound(err)
	}
	var ev assistant.Event
	err = tx.QueryRow(ctx, `INSERT INTO assistant_run_events(run_id,seq,type,payload,created_at,tenant_id) VALUES($1,(SELECT COALESCE(MAX(seq),0)+1 FROM assistant_run_events WHERE run_id=$1),$2,$3,$4,$5) RETURNING run_id,seq,type,payload,created_at`, runID, typ, payload, time.Now().UTC(), tenantID(ctx)).Scan(&ev.RunID, &ev.Seq, &ev.Type, &ev.Payload, &ev.CreatedAt)
	if err != nil {
		return ev, err
	}
	return ev, tx.Commit(ctx)
}

func (s *Store) ListEvents(ctx context.Context, runID string, afterSeq int) ([]assistant.Event, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT run_id, seq, type, payload, created_at FROM assistant_run_events
		 WHERE run_id=$1 AND seq > $2 AND tenant_id=$3 ORDER BY seq`, runID, afterSeq, tenantID(ctx))
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:staticcheck
	var out []assistant.Event
	for rows.Next() {
		var ev assistant.Event
		if err := rows.Scan(&ev.RunID, &ev.Seq, &ev.Type, &ev.Payload, &ev.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// —— worker 队列 ——

func (s *Store) ClaimNext(ctx context.Context, now time.Time) (*assistant.Run, error) {
	ctx2 := ctx
	tx, err := s.db.Pool().Begin(ctx2)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx2) //nolint:errcheck
	var run assistant.Run
	err = scanRun(tx.QueryRow(ctx2,
		`SELECT `+runCols+` FROM assistant_runs WHERE status='queued'
		 ORDER BY created_at LIMIT 1 FOR UPDATE SKIP LOCKED`), &run)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	worker := fmt.Sprintf("w-%d", now.UnixNano())
	lease := now.Add(90 * time.Second)
	if _, err := tx.Exec(ctx2,
		`UPDATE assistant_runs SET status='running', started_at=$2, lease_owner=$3, lease_expires=$4 WHERE id=$1`,
		run.ID, now, worker, lease); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx2); err != nil {
		return nil, err
	}
	run.Status = assistant.StatusRunning
	run.LeaseOwner = worker
	run.LeaseExpires = lease
	run.StartedAt = &now
	return &run, nil
}

func (s *Store) Heartbeat(ctx context.Context, runID, workerID string, now time.Time) error {
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE assistant_runs SET lease_expires=$3 WHERE id=$1 AND lease_owner=$2 AND status='running'`,
		runID, workerID, now.Add(90*time.Second))
	if err == nil && tag.RowsAffected() == 0 {
		return assistant.ErrConflict
	}
	return err
}

func (s *Store) ReclaimExpired(ctx context.Context, now time.Time) int {
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE assistant_runs SET status='failed', error_code='worker_lost',
		 error_msg='执行进程失联（租约过期），任务未完成；不会自动重跑', finished_at=$1
		 WHERE status='running' AND lease_expires < $2`, now, now)
	if err != nil {
		return 0
	}
	return int(tag.RowsAffected())
}

func (s *Store) CountActive(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM assistant_runs WHERE status IN ('queued','running')`).Scan(&n)
	return n, err
}

func (s *Store) CountTenantActive(ctx context.Context, tenantID string) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM assistant_runs WHERE tenant_id=$1 AND status IN ('queued','running')`, tenantID).Scan(&n)
	return n, err
}

func tenantID(ctx context.Context) string { v, _ := tenant.TenantFrom(ctx); return v }

// ConversationRuns 先验证租户/owner，再分页消息对应轮次。
func (s *Store) ConversationRuns(ctx context.Context, id string, before time.Time, limit int) ([]assistant.Run, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.Pool().Query(ctx, `SELECT `+runCols+` FROM assistant_runs WHERE session_id=$1 AND tenant_id=$2 AND owner_user_id=$3 AND ($4::timestamptz IS NULL OR created_at<$4) ORDER BY created_at DESC LIMIT $5`, id, tenantID(ctx), assistant.OwnerFrom(ctx), nullTime(before), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []assistant.Run{}
	for rows.Next() {
		var r assistant.Run
		if err = scanRun(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Finalize 以 lease owner 作 fencing，回答、报告、状态和事件一次提交。
func (s *Store) Finalize(ctx context.Context, r assistant.Run) error {
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var cancelled bool
	err = tx.QueryRow(ctx, `SELECT cancel_flag FROM assistant_runs WHERE id=$1 AND tenant_id=$2 AND status='running' AND lease_owner=$3 FOR UPDATE`, r.ID, r.TenantID, r.LeaseOwner).Scan(&cancelled)
	if isNoRows(err) {
		return assistant.ErrConflict
	}
	if err != nil {
		return err
	}
	if cancelled {
		r.Status = assistant.StatusCancelled
		r.Report = nil
		r.Reply = "已停止本轮诊断。历史消息仍然保留。"
		r.ErrorCode = "cancelled"
		r.ErrorMsg = r.Reply
	}
	evidence, err := json.Marshal(r.Evidence)
	if err != nil {
		return err
	}
	var report any
	if len(r.Report) > 0 {
		report = r.Report
	}
	_, err = tx.Exec(ctx, `UPDATE assistant_runs SET status=$2,quality=$3,report=$4,evidence=$5,reply=$6,usage_tokens=$7,usage_source=$8,error_code=$9,error_msg=$10,finished_at=$11,model=$12 WHERE id=$1`, r.ID, r.Status, r.Quality, report, evidence, r.Reply, r.Usage.Tokens, r.Usage.UsageSource, r.ErrorCode, r.ErrorMsg, r.FinishedAt, r.Model)
	if err != nil {
		return err
	}
	typ := assistant.EventReportReady
	switch r.Status {
	case assistant.StatusFailed:
		typ = assistant.EventRunFailed
	case assistant.StatusTimedOut:
		typ = assistant.EventRunTimedOut
	case assistant.StatusCancelled:
		typ = assistant.EventRunCancelled
	}
	_, err = tx.Exec(ctx, `INSERT INTO assistant_run_events(run_id,seq,type,created_at,tenant_id) VALUES($1,(SELECT COALESCE(MAX(seq),0)+1 FROM assistant_run_events WHERE run_id=$1),$2,$3,$4)`, r.ID, typ, time.Now().UTC(), r.TenantID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE assistant_sessions SET updated_at=$2 WHERE id=$1 AND tenant_id=$3`, r.SessionID, time.Now().UTC(), r.TenantID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Sweep 移除过期片段、保留对话；活动任务不受历史清理影响。
func (s *Store) Sweep(ctx context.Context, historyBefore, evidenceBefore time.Time) error {
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `UPDATE assistant_runs SET evidence=(SELECT COALESCE(jsonb_agg((e-'snippet')||jsonb_build_object('reasonCode','expired')),'[]'::jsonb) FROM jsonb_array_elements(CASE WHEN jsonb_typeof(evidence)='array' THEN evidence ELSE '[]'::jsonb END) e) WHERE finished_at<$1 AND jsonb_typeof(evidence)='array'`, evidenceBefore)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM assistant_sessions s WHERE updated_at<$1 AND NOT EXISTS(SELECT 1 FROM assistant_runs r WHERE r.session_id=s.id AND status IN ('queued','running'))`, historyBefore)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
