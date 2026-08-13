// store_pg.go Pipeline 三仓储（Repository/RunRepository/TemplateRepository）的 PostgreSQL 实现。
//
// 与 memoryStore 同构：同一 pgStore 实例实现三接口；所有方法强制按 ctx 租户过滤；
// Create 以 ctx 租户为准忽略请求体；跨租户访问统一 NotFound 不泄漏存在性。
//
// JSONB 字段（Pipeline.ParamOverrides/Trigger、PipelineTemplate.Stages、StageRun.Input/Output）用
// []byte marshal/unmarshal；多值字段不另起子表（stage_runs 除外，因其随 UpdateRun 全量重写）。
//
// 单实例串行靠 `ux_pipeline_runs_active` 部分唯一索引（同 pipeline 仅允许一个 running/paused），
// CreateRun 在 INSERT 失败时按约束名区分：ux_pipeline_runs_active → ErrActiveRunExists，
// pipeline_runs_pkey → ErrRunExists。
//
// UpdateRun 事务内重写 stage_runs（DELETE WHERE pipeline_run_id + 批量 INSERT），
// 与 application_bindings 保序模式同构（全量替换）。
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// pgStore 三仓储的 PostgreSQL 实现。db 必须已完成迁移（含 0018_pipeline）。
type pgStore struct {
	db *pgxpool.Pool
}

// NewPGStore 创建 Pipeline PG 仓储。db 必须已完成迁移。
func NewPGStore(db *pgxpool.Pool) *pgStore { return &pgStore{db: db} }

// 列常量与 struct 字段顺序严格对齐（scan 列序必须一致）。
// runCols 含 repo_id（build stage 解析 app 绑定的 internal CodeRepo 用，与内存版字段对齐）。
const (
	pipeCols = `id, tenant_id, app_id, name, kind, template_id, param_overrides, trigger, disabled, created_at`
	runCols  = `id, tenant_id, app_id, pipeline_id, branch, commit, repo_id, trigger, trigger_ref, status, current_stage, version, created_at, finished_at`
	tplCols  = `id, tenant_id, name, kind, description, stages, builtin, version`
)

// stageRunCols 不含 BIGSERIAL 的 id（重写时按 (pipeline_run_id, stage_index) 排序读回）。
const stageRunCols = `stage_index, type, name, status, input, output, started_at, finished_at, error, log`

// 约束名常量（用于错误分类）。与 migration 0018 定义对齐。
const (
	constraintActiveRun = "ux_pipeline_runs_active" // 单实例串行部分唯一索引
	constraintRunPK     = "pipeline_runs_pkey"      // pipeline_runs 主键
	constraintPipeName  = "ux_pipelines_name_tenant_app"
	constraintPipePK    = "pipelines_pkey"
	constraintTplName   = "ux_pipeline_templates_name_tenant"
	constraintTplPK     = "pipeline_templates_pkey"
)

// ---------- Repository: Pipeline ----------

// ListPipelines 按租户 + 可选 appID 过滤；created_at 升序（与内存版 sort.Slice ID 升序语义近似）。
func (s *pgStore) ListPipelines(ctx context.Context, appID string) ([]Pipeline, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + pipeCols + ` FROM pipelines WHERE tenant_id=$1`
	args := []any{tid}
	if appID != "" {
		args = append(args, appID)
		q += fmt.Sprintf(` AND app_id=$%d`, len(args))
	}
	q += ` ORDER BY created_at, id`
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Pipeline, 0)
	for rows.Next() {
		var p Pipeline
		var overridesB, trigB []byte
		if err = rows.Scan(&p.ID, &p.TenantID, &p.AppID, &p.Name, &p.Kind, &p.TemplateID,
			&overridesB, &trigB, &p.Disabled, &p.CreatedAt); err != nil {
			return nil, err
		}
		if len(overridesB) > 0 {
			_ = json.Unmarshal(overridesB, &p.ParamOverrides)
		}
		if len(trigB) > 0 {
			_ = json.Unmarshal(trigB, &p.Trigger)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetPipeline 取单个；跨租户访问 NotFound 不泄漏。
func (s *pgStore) GetPipeline(ctx context.Context, id string) (Pipeline, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return Pipeline{}, err
	}
	var p Pipeline
	var overridesB, trigB []byte
	err = s.db.QueryRow(ctx,
		`SELECT `+pipeCols+` FROM pipelines WHERE id=$1 AND tenant_id=$2`, id, tid).
		Scan(&p.ID, &p.TenantID, &p.AppID, &p.Name, &p.Kind, &p.TemplateID,
			&overridesB, &trigB, &p.Disabled, &p.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Pipeline{}, ErrPipelineNotFound
		}
		return Pipeline{}, err
	}
	if len(overridesB) > 0 {
		_ = json.Unmarshal(overridesB, &p.ParamOverrides)
	}
	if len(trigB) > 0 {
		_ = json.Unmarshal(trigB, &p.Trigger)
	}
	return p, nil
}

// GetPipelineAny 跨租户按 ID 查（webhook 触发用，无 tenant 过滤；token 鉴权在 handler 层）。
func (s *pgStore) GetPipelineAny(ctx context.Context, id string) (Pipeline, error) {
	var p Pipeline
	var overridesB, trigB []byte
	err := s.db.QueryRow(ctx,
		`SELECT `+pipeCols+` FROM pipelines WHERE id=$1`, id).
		Scan(&p.ID, &p.TenantID, &p.AppID, &p.Name, &p.Kind, &p.TemplateID,
			&overridesB, &trigB, &p.Disabled, &p.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Pipeline{}, ErrPipelineNotFound
		}
		return Pipeline{}, err
	}
	if len(overridesB) > 0 {
		_ = json.Unmarshal(overridesB, &p.ParamOverrides)
	}
	if len(trigB) > 0 {
		_ = json.Unmarshal(trigB, &p.Trigger)
	}
	return p, nil
}

// CreatePipeline 创建。同 (tenant,app,name) 唯一；ID 冲突 → ErrPipelineExists。
func (s *pgStore) CreatePipeline(ctx context.Context, in Pipeline) (Pipeline, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return Pipeline{}, err
	}
	in.TenantID = tid // 以 ctx 为准，忽略请求体
	if err := in.Validate(); err != nil {
		return Pipeline{}, err
	}
	if in.ID == "" {
		in.ID = newID("pipe")
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now()
	}
	overridesB, err := json.Marshal(in.ParamOverrides)
	if err != nil {
		return Pipeline{}, fmt.Errorf("序列化 param_overrides 失败: %w", err)
	}
	trigB, err := json.Marshal(in.Trigger)
	if err != nil {
		return Pipeline{}, fmt.Errorf("序列化 trigger 失败: %w", err)
	}
	_, err = s.db.Exec(ctx,
		`INSERT INTO pipelines (`+pipeCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		in.ID, in.TenantID, in.AppID, in.Name, in.Kind, in.TemplateID, overridesB, trigB, in.Disabled, in.CreatedAt)
	if err != nil {
		if classifyPipelineErr(err) == ErrPipelineExists {
			return Pipeline{}, ErrPipelineExists
		}
		return Pipeline{}, err
	}
	return in, nil
}

// UpdatePipeline 更新。WHERE tenant_id 防越权；改名冲突按约束名分类。
func (s *pgStore) UpdatePipeline(ctx context.Context, in Pipeline) (Pipeline, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return Pipeline{}, err
	}
	if err := in.Validate(); err != nil {
		return Pipeline{}, err
	}
	in.TenantID = tid
	overridesB, err := json.Marshal(in.ParamOverrides)
	if err != nil {
		return Pipeline{}, fmt.Errorf("序列化 param_overrides 失败: %w", err)
	}
	trigB, err := json.Marshal(in.Trigger)
	if err != nil {
		return Pipeline{}, fmt.Errorf("序列化 trigger 失败: %w", err)
	}
	tag, err := s.db.Exec(ctx,
		`UPDATE pipelines SET app_id=$1, name=$2, kind=$3, template_id=$4, param_overrides=$5, trigger=$6, disabled=$7
		 WHERE id=$8 AND tenant_id=$9`,
		in.AppID, in.Name, in.Kind, in.TemplateID, overridesB, trigB, in.Disabled, in.ID, tid)
	if err != nil {
		if classifyPipelineErr(err) == ErrPipelineExists {
			return Pipeline{}, ErrPipelineExists
		}
		return Pipeline{}, err
	}
	if tag.RowsAffected() == 0 {
		return Pipeline{}, ErrPipelineNotFound
	}
	// 回读 created_at（UPDATE 不改 created_at）
	return s.GetPipeline(ctx, in.ID)
}

// DeletePipeline 级联删 pipeline + 其下 runs（stage_runs 经 FK CASCADE 自动清）。
// 跨租户访问 NotFound 不泄漏。
func (s *pgStore) DeletePipeline(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// 先校验归属（防越权删他人 pipeline 时连带清 runs）
	var hit bool
	if err = tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pipelines WHERE id=$1 AND tenant_id=$2)`, id, tid).Scan(&hit); err != nil {
		return err
	}
	if !hit {
		return ErrPipelineNotFound
	}
	// pipeline_runs 删除时 stage_runs 经 ON DELETE CASCADE 自动清；pipeline_runs 无 CASCADE 到 pipelines 需显式删。
	if _, err = tx.Exec(ctx, `DELETE FROM pipeline_runs WHERE pipeline_id=$1`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM pipelines WHERE id=$1 AND tenant_id=$2`, id, tid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------- RunRepository: PipelineRun ----------

// ListRuns 按租户 + 可选 appID/pipelineID/status 过滤；created_at 倒序（最新优先，与内存版一致）。
func (s *pgStore) ListRuns(ctx context.Context, appID, pipelineID, status string) ([]PipelineRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + runCols + ` FROM pipeline_runs WHERE tenant_id=$1`
	args := []any{tid}
	if appID != "" {
		args = append(args, appID)
		q += fmt.Sprintf(` AND app_id=$%d`, len(args))
	}
	if pipelineID != "" {
		args = append(args, pipelineID)
		q += fmt.Sprintf(` AND pipeline_id=$%d`, len(args))
	}
	if status != "" {
		args = append(args, status)
		q += fmt.Sprintf(` AND status=$%d`, len(args))
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT 1000`
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PipelineRun, 0)
	for rows.Next() {
		var r PipelineRun
		var fp *time.Time // finished_at 可空（run 未完成）
		if err = rows.Scan(&r.ID, &r.TenantID, &r.AppID, &r.PipelineID, &r.Branch, &r.Commit, &r.RepoID,
			&r.Trigger, &r.TriggerRef, &r.Status, &r.CurrentStage, &r.Version, &r.CreatedAt, &fp); err != nil {
			return nil, err
		}
		if fp != nil {
			r.FinishedAt = *fp
		}
		if err = loadStageRuns(ctx, s.db, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListAllRuns 跨租户列表（admin 总览用，返回对象带 TenantID）；可选 status 过滤；created_at 倒序。
// 与 ListRuns 区别：不按 ctx tenant 过滤（admin 跨租户视图）。LIMIT 1000 防御上界（与审计日志同款）。
func (s *pgStore) ListAllRuns(ctx context.Context, status string) ([]PipelineRun, error) {
	q := `SELECT ` + runCols + ` FROM pipeline_runs`
	args := []any{}
	if status != "" {
		args = append(args, status)
		q += fmt.Sprintf(` WHERE status=$%d`, len(args))
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT 1000`
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PipelineRun, 0)
	for rows.Next() {
		var r PipelineRun
		var fp *time.Time
		if err = rows.Scan(&r.ID, &r.TenantID, &r.AppID, &r.PipelineID, &r.Branch, &r.Commit, &r.RepoID,
			&r.Trigger, &r.TriggerRef, &r.Status, &r.CurrentStage, &r.Version, &r.CreatedAt, &fp); err != nil {
			return nil, err
		}
		if fp != nil {
			r.FinishedAt = *fp
		}
		if err = loadStageRuns(ctx, s.db, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRun 取单个（含 stageRuns）；跨租户 NotFound 不泄漏。
func (s *pgStore) GetRun(ctx context.Context, id string) (PipelineRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return PipelineRun{}, err
	}
	var r PipelineRun
	var fp *time.Time // finished_at 可空（run 未完成）
	err = s.db.QueryRow(ctx,
		`SELECT `+runCols+` FROM pipeline_runs WHERE id=$1 AND tenant_id=$2`, id, tid).
		Scan(&r.ID, &r.TenantID, &r.AppID, &r.PipelineID, &r.Branch, &r.Commit, &r.RepoID,
			&r.Trigger, &r.TriggerRef, &r.Status, &r.CurrentStage, &r.Version, &r.CreatedAt, &fp)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PipelineRun{}, ErrRunNotFound
		}
		return PipelineRun{}, err
	}
	if fp != nil {
		r.FinishedAt = *fp
	}
	if err = loadStageRuns(ctx, s.db, &r); err != nil {
		return PipelineRun{}, err
	}
	return r, nil
}

// CreateRun 单实例串行：ux_pipeline_runs_active 部分唯一索引拦截。
// pipeline 必须存在且归属本租户（NotFound）；冲突按约束名区分 ErrActiveRunExists / ErrRunExists。
func (s *pgStore) CreateRun(ctx context.Context, in PipelineRun) (PipelineRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return PipelineRun{}, err
	}
	in.TenantID = tid
	if in.ID == "" {
		in.ID = newID("run")
	}
	if in.CreatedAt.IsZero() {
		in.CreatedAt = time.Now()
	}
	// 事务包住 pipeline 校验（FOR UPDATE 锁行防并发 DeletePipeline 致孤儿 run）+
	// pipeline_runs INSERT + stage_runs 写入，保证原子（与 UpdateRun 事务内重写模式一致）。
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PipelineRun{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// 校验 pipeline 存在 + 归属 + 锁行（与内存版锁内原子校验+INSERT 语义一致）
	var hit bool
	if err = tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pipelines WHERE id=$1 AND tenant_id=$2 FOR UPDATE)`, in.PipelineID, tid).Scan(&hit); err != nil {
		return PipelineRun{}, err
	}
	if !hit {
		return PipelineRun{}, ErrPipelineNotFound
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO pipeline_runs (id, tenant_id, app_id, pipeline_id, branch, commit, repo_id, trigger, trigger_ref, status, current_stage, version, created_at, finished_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		in.ID, in.TenantID, in.AppID, in.PipelineID, in.Branch, in.Commit, in.RepoID,
		in.Trigger, in.TriggerRef, in.Status, in.CurrentStage, in.Version, in.CreatedAt, nullTime(in.FinishedAt))
	if err != nil {
		switch classifyRunErr(err) {
		case ErrActiveRunExists:
			return PipelineRun{}, ErrActiveRunExists
		case ErrRunExists:
			return PipelineRun{}, ErrRunExists
		}
		return PipelineRun{}, err
	}
	// 写入初始 stage_runs（若调用方传入）；marshal 错误显式检查（与 CreatePipeline 一致）。
	for _, sr := range in.StageRuns {
		inB, mErr := json.Marshal(sr.Input)
		if mErr != nil {
			return PipelineRun{}, fmt.Errorf("序列化 stage input 失败: %w", mErr)
		}
		outB, mErr := json.Marshal(sr.Output)
		if mErr != nil {
			return PipelineRun{}, fmt.Errorf("序列化 stage output 失败: %w", mErr)
		}
		if _, err = tx.Exec(ctx,
			`INSERT INTO stage_runs (pipeline_run_id, `+stageRunCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			in.ID, sr.Index, sr.Type, sr.Name, sr.Status, inB, outB, nullTime(sr.StartedAt), nullTime(sr.FinishedAt), sr.Error, sr.Log); err != nil {
			return PipelineRun{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return PipelineRun{}, err
	}
	return in, nil
}

// UpdateRun 全量写回（engine 推进时调）：UPDATE pipeline_runs + 事务内重写 stage_runs。
// WHERE tenant_id 防越权；RowsAffected==0 → ErrRunNotFound。
func (s *pgStore) UpdateRun(ctx context.Context, in PipelineRun) (PipelineRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return PipelineRun{}, err
	}
	in.TenantID = tid
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PipelineRun{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx,
		`UPDATE pipeline_runs SET branch=$1, commit=$2, trigger=$3, trigger_ref=$4, status=$5, current_stage=$6, version=$7, finished_at=$8
		 WHERE id=$9 AND tenant_id=$10`,
		in.Branch, in.Commit, in.Trigger, in.TriggerRef, in.Status, in.CurrentStage, in.Version, nullTime(in.FinishedAt),
		in.ID, tid)
	if err != nil {
		return PipelineRun{}, err
	}
	if tag.RowsAffected() == 0 {
		return PipelineRun{}, ErrRunNotFound
	}
	// 全量重写 stage_runs
	if _, err = tx.Exec(ctx, `DELETE FROM stage_runs WHERE pipeline_run_id=$1`, in.ID); err != nil {
		return PipelineRun{}, err
	}
	for _, sr := range in.StageRuns {
		inB, mErr := json.Marshal(sr.Input)
		if mErr != nil {
			return PipelineRun{}, fmt.Errorf("序列化 stage input 失败: %w", mErr)
		}
		outB, mErr := json.Marshal(sr.Output)
		if mErr != nil {
			return PipelineRun{}, fmt.Errorf("序列化 stage output 失败: %w", mErr)
		}
		if _, err = tx.Exec(ctx,
			`INSERT INTO stage_runs (pipeline_run_id, `+stageRunCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			in.ID, sr.Index, sr.Type, sr.Name, sr.Status, inB, outB, nullTime(sr.StartedAt), nullTime(sr.FinishedAt), sr.Error, sr.Log); err != nil {
			return PipelineRun{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return PipelineRun{}, err
	}
	return s.GetRun(ctx, in.ID)
}

// HasActiveRun 同 pipeline 是否已有 running/paused run（与 ux_pipeline_runs_active 索引对齐）。
func (s *pgStore) HasActiveRun(ctx context.Context, pipelineID string) (bool, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return false, err
	}
	var exists bool
	err = s.db.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pipeline_runs WHERE pipeline_id=$1 AND tenant_id=$2 AND status IN ('running','paused'))`,
		pipelineID, tid).Scan(&exists)
	return exists, err
}

// ---------- TemplateRepository: PipelineTemplate ----------

// ListTemplates 返平台预置（tenant_id IS NULL）+ 本租户自定义。
// 排序：平台预置在前（NULLS FIRST），再按 id 升序（与内存版一致）。
func (s *pgStore) ListTemplates(ctx context.Context) ([]PipelineTemplate, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx,
		`SELECT `+tplCols+` FROM pipeline_templates WHERE tenant_id IS NULL OR tenant_id=$1
		 ORDER BY (tenant_id IS NOT NULL), id`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]PipelineTemplate, 0)
	for rows.Next() {
		var t PipelineTemplate
		if err = scanTemplate(rows, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetTemplate 取单个。平台预置（tenant_id IS NULL）跨租户可见；租户自定义跨租户 NotFound。
// 无租户 ctx（平台级 seed 升级路径）仅可访问平台预置（tid="" 时 SQL tenant_id='' 不匹配租户自定义）。
func (s *pgStore) GetTemplate(ctx context.Context, id string) (PipelineTemplate, error) {
	tid, _ := tenant.TenantFrom(ctx)
	var t PipelineTemplate
	row := s.db.QueryRow(ctx,
		`SELECT `+tplCols+` FROM pipeline_templates WHERE id=$1 AND (tenant_id IS NULL OR tenant_id=$2)`, id, tid)
	if err := scanTemplate(row, &t); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PipelineTemplate{}, ErrPipelineNotFound
		}
		return PipelineTemplate{}, err
	}
	return t, nil
}

// CreateTemplate 模板特判：平台预置（tenant_id=""）不要求 ctx 租户（admin 平台级 seed）。
// 跨租户注入（!="" && !=tid）锁定为本租户（防越权写）。
func (s *pgStore) CreateTemplate(ctx context.Context, in PipelineTemplate) (PipelineTemplate, error) {
	// 平台预置（in.TenantID=""）不要求 ctx 租户；租户自定义需 ctx 租户。
	tid, _ := tenant.TenantFrom(ctx)
	if in.TenantID != "" && in.TenantID != tid {
		in.TenantID = tid // 锁定为本租户
	}
	if in.ID == "" {
		in.ID = newID("tpl")
	}
	// tenant_id=""（平台预置）→ 写 NULL；pgx 用 *string 或 nil
	var tenantArg any
	if in.TenantID == "" {
		tenantArg = nil
	} else {
		tenantArg = in.TenantID
	}
	stagesB, err := json.Marshal(in.Stages)
	if err != nil {
		return PipelineTemplate{}, fmt.Errorf("序列化 stages 失败: %w", err)
	}
	_, err = s.db.Exec(ctx,
		`INSERT INTO pipeline_templates (`+tplCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		in.ID, tenantArg, in.Name, in.Kind, in.Description, stagesB, in.Builtin, in.Version)
	if err != nil {
		if classifyTemplateErr(err) == ErrTemplateExists {
			return PipelineTemplate{}, ErrTemplateExists
		}
		return PipelineTemplate{}, err
	}
	return in, nil
}

// UpdateTemplate 更新自定义模板。builtin 拒（防误改致新应用默认 binding 漂移，builtin 升级走代码发版经 ReplaceBuiltinTemplate）。
// 平台预置（tenant_id NULL）super_admin 可改；租户自定义仅本租户可改--super_admin 亦不跨租户管租户自定义
// （跨租户写越权风险高，资源运维仍在租户内；GetTemplate 已过滤跨租户访问返 NotFound 不泄漏）。
func (s *pgStore) UpdateTemplate(ctx context.Context, t PipelineTemplate) (PipelineTemplate, error) {
	ex, err := s.GetTemplate(ctx, t.ID) // 复用：存在 + 本租户/平台预置可见校验
	if err != nil {
		return PipelineTemplate{}, err
	}
	if ex.Builtin {
		return PipelineTemplate{}, ErrTemplateBuiltin
	}
	stagesB, err := json.Marshal(t.Stages)
	if err != nil {
		return PipelineTemplate{}, fmt.Errorf("序列化 stages 失败: %w", err)
	}
	tag, err := s.db.Exec(ctx,
		`UPDATE pipeline_templates SET name=$1, kind=$2, description=$3, stages=$4 WHERE id=$5 AND builtin=false`,
		t.Name, t.Kind, t.Description, stagesB, t.ID)
	if err != nil {
		if classifyTemplateErr(err) == ErrTemplateExists {
			return PipelineTemplate{}, ErrTemplateExists
		}
		return PipelineTemplate{}, err
	}
	if tag.RowsAffected() == 0 {
		return PipelineTemplate{}, ErrPipelineNotFound // builtin=true 被 WHERE 拦或不存在
	}
	return s.GetTemplate(ctx, t.ID)
}

// DeleteTemplate 删除自定义模板。builtin 拒。
func (s *pgStore) DeleteTemplate(ctx context.Context, id string) error {
	ex, err := s.GetTemplate(ctx, id)
	if err != nil {
		return err
	}
	if ex.Builtin {
		return ErrTemplateBuiltin
	}
	tag, err := s.db.Exec(ctx,
		`DELETE FROM pipeline_templates WHERE id=$1 AND builtin=false`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPipelineNotFound
	}
	return nil
}

// ReplaceBuiltinTemplate 平台级 seed 专用：覆盖 builtin 模板的 stages/name/description/version。
// 绕过 UpdateTemplate 的 builtin 拒改保护（builtin 升级走代码发版，非 admin UI）。
// 仅 builtin=true 生效（WHERE builtin=true 拦截租户自定义模板）；不存在或非 builtin RowsAffected=0 返 ErrPipelineNotFound。
// 注意：不更新 kind（CI/CD 不应跨类变更）与 tenant_id（平台预置恒 NULL）。
func (s *pgStore) ReplaceBuiltinTemplate(ctx context.Context, t PipelineTemplate) error {
	stagesB, err := json.Marshal(t.Stages)
	if err != nil {
		return fmt.Errorf("序列化 stages 失败: %w", err)
	}
	tag, err := s.db.Exec(ctx,
		`UPDATE pipeline_templates
		 SET name=$1, description=$2, stages=$3, version=$4
		 WHERE id=$5 AND builtin=true`,
		t.Name, t.Description, stagesB, t.Version, t.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPipelineNotFound
	}
	return nil
}

// ---------- 辅助 ----------

// loadStageRuns 读回某 run 的 stage_runs（按 stage_index 升序），填入 r.StageRuns。
func loadStageRuns(ctx context.Context, db *pgxpool.Pool, r *PipelineRun) error {
	rows, err := db.Query(ctx,
		`SELECT `+stageRunCols+` FROM stage_runs WHERE pipeline_run_id=$1 ORDER BY stage_index`, r.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var sr StageRun
		var inB, outB []byte
		// started_at/finished_at 可空（stage 未开始/未结束），用 *time.Time 接 NULL
		var sp, fp *time.Time
		if err = rows.Scan(&sr.Index, &sr.Type, &sr.Name, &sr.Status, &inB, &outB, &sp, &fp, &sr.Error, &sr.Log); err != nil {
			return err
		}
		if sp != nil {
			sr.StartedAt = *sp
		}
		if fp != nil {
			sr.FinishedAt = *fp
		}
		if len(inB) > 0 {
			_ = json.Unmarshal(inB, &sr.Input)
		}
		if len(outB) > 0 {
			_ = json.Unmarshal(outB, &sr.Output)
		}
		r.StageRuns = append(r.StageRuns, sr)
	}
	return rows.Err()
}

// scanTemplate 扫描 PipelineTemplate 行；tenant_id 为 NULL 时保持空字符串。
func scanTemplate(r pg.RowScanner, t *PipelineTemplate) error {
	var stagesB []byte
	var tenantID *string
	if err := r.Scan(&t.ID, &tenantID, &t.Name, &t.Kind, &t.Description, &stagesB, &t.Builtin, &t.Version); err != nil {
		return err
	}
	if tenantID != nil {
		t.TenantID = *tenantID
	}
	if len(stagesB) > 0 {
		_ = json.Unmarshal(stagesB, &t.Stages)
	}
	return nil
}

// nullTime 把零值 time.Time 转 NULL，否则转 t。
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// classifyPipelineErr 按 PG 约束名分类 Pipeline 相关唯一冲突。
func classifyPipelineErr(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return nil
	}
	switch pgErr.ConstraintName {
	case constraintPipeName, constraintPipePK:
		return ErrPipelineExists
	}
	return nil
}

// classifyRunErr 按 PG 约束名分类 PipelineRun 相关唯一冲突。
func classifyRunErr(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return nil
	}
	switch pgErr.ConstraintName {
	case constraintActiveRun:
		return ErrActiveRunExists
	case constraintRunPK:
		return ErrRunExists
	}
	return nil
}

// classifyTemplateErr 按 PG 约束名分类 PipelineTemplate 相关唯一冲突。
func classifyTemplateErr(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return nil
	}
	switch pgErr.ConstraintName {
	case constraintTplName, constraintTplPK:
		return ErrTemplateExists
	}
	return nil
}

// idCounter 进程级计数器，保证同纳秒内多次调用 ID 不冲突（memory 场景纯内存极快）。
var idCounter uint64

// newID 生成带前缀的短 ID（sha256 前 12 hex）。
// 时间纳秒 + 原子计数器 + 前缀 三者哈希，保证唯一（与 internal/devops/pg/store.go 同款思路）。
func newID(prefix string) string {
	n := atomic.AddUint64(&idCounter, 1)
	h := sha256.Sum256([]byte(fmt.Sprintf("%d-%d-%s", time.Now().UnixNano(), n, prefix)))
	return prefix + "-" + hex.EncodeToString(h[:6])
}
