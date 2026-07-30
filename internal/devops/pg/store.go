// Package pg 提供 devops 四仓储（CodeRepo/BuildRun/Image/Release）的 PostgreSQL 实现。
//
// 一个 Store 同时实现四个仓储接口（与内存版同构）。显式 WHERE tenant_id 强制多租户过滤；
// Create 以 ctx 租户为准忽略请求体；跨租户访问统一 not found（不泄漏存在性）。
//
// Release 编排逻辑（CreateRelease/RollbackRelease）逐行对齐内存版：调用注入的
// workload.Repository 接口完成 Workload 找/建/更新镜像，**不直接读写 workloads 表**——
// 因此对 workload 存储后端完全透明（workload 是 PG 还是内存，devops PG 都照常工作）。
// 事务边界限于 releases 表自身；workload 侧失败由编排逻辑回滚（与内存版同款语义）。
//
// BuildRun mock CI runner（goroutine 异步流转 pending→running→success + 产出 Image）
// 与内存版同款步进；流转用 UPDATE build_runs + INSERT images。
package pg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/internal/devops/builder"
	devopsmemory "github.com/aitoys/paas/internal/devops/memory"
	"github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/internal/workload"
)

// Store 实现 devops 全部四个仓储接口。workload 仓储由 cmd/core 注入，供 Release 编排。
type Store struct {
	db       *pg.DB
	workload workload.Repository // 注入；Release 编排找/建/更新基线 Workload
	pipeline builder.Pipeline    // 构建流水线（nil=Mock）；cmd/core 按 PAAS_DEVOPS_REAL 注入 Real
}

// NewStore 创建 devops PG 仓储。db 必须已完成迁移；wlRepo 为 Release 编排提供 Workload 能力。
// 与内存版 memory.NewStore(wlRepo) 同构——对 workload 存储后端完全透明。
func NewStore(db *pg.DB, wlRepo workload.Repository) *Store {
	return &Store{db: db, workload: wlRepo}
}

// SetPipeline 注入构建流水线（cmd/core 按 PAAS_DEVOPS_REAL 注入 Real）；nil=Mock。
func (s *Store) SetPipeline(p builder.Pipeline) { s.pipeline = p }

// 列常量与各 struct 字段顺序严格对齐（scan 列序必须一致）。
const (
	repoCols    = `id, tenant_id, app_id, git_url, branch, dockerfile, build_context, status, created_at`
	buildCols   = `id, tenant_id, app_id, repo_id, trigger, commit, branch, message, status, image_id, log, started_at, finished_at`
	imageCols   = `id, tenant_id, app_id, registry, tag, digest, source, branch, build_run_id, built_at, status`
	releaseCols = `id, tenant_id, app_id, env_id, image_id, image_digest, strategy, status, workload_id, previous_image_id, is_rollback, created_at, created_by`
)

// ---------- CodeRepoRepository ----------

// ListRepos 按租户 + 可选 appID 过滤；ID 升序（与内存版 sort.Slice ID 升序一致）。
func (s *Store) ListRepos(ctx context.Context, appID string) ([]devops.CodeRepo, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + repoCols + ` FROM code_repos WHERE tenant_id=$1`
	args := []any{tid}
	if appID != "" {
		args = append(args, appID)
		q += fmt.Sprintf(` AND app_id=$%d`, len(args))
	}
	q += ` ORDER BY id`
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]devops.CodeRepo, 0)
	for rows.Next() {
		var r devops.CodeRepo
		if err = rows.Scan(&r.ID, &r.TenantID, &r.AppID, &r.GitURL, &r.Branch,
			&r.Dockerfile, &r.BuildContext, &r.Status, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRepo 取单个仓库；跨租户访问返回 not found（不泄漏）。
func (s *Store) GetRepo(ctx context.Context, id string) (devops.CodeRepo, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return devops.CodeRepo{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+repoCols+` FROM code_repos WHERE id=$1 AND tenant_id=$2`, id, tid)
	var r devops.CodeRepo
	if err = row.Scan(&r.ID, &r.TenantID, &r.AppID, &r.GitURL, &r.Branch,
		&r.Dockerfile, &r.BuildContext, &r.Status, &r.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return devops.CodeRepo{}, fmt.Errorf("仓库不存在: %s", id)
		}
		return devops.CodeRepo{}, err
	}
	return r, nil
}

// CreateRepo 写入仓库；以 ctx 租户为准忽略请求体 TenantID（防越权写）；
// Dockerfile/BuildContext/Status 空则补默认值（与内存版一致）；空 ID 自动生成。
// 主键冲突返回「仓库已存在」（沿用内存版领域文本，不用 FormatExists 哨兵）。
func (s *Store) CreateRepo(ctx context.Context, r devops.CodeRepo) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if r.Dockerfile == "" {
		r.Dockerfile = devops.DefaultDockerfile
	}
	if r.BuildContext == "" {
		r.BuildContext = devops.DefaultBuildContext
	}
	if r.Status == "" {
		r.Status = devops.RepoStatusActive
	}
	if r.ID == "" {
		r.ID = newID("repo")
	}
	r.TenantID = tid // 以 ctx 为准
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO code_repos (`+repoCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		r.ID, r.TenantID, r.AppID, r.GitURL, r.Branch, r.Dockerfile, r.BuildContext, r.Status, r.CreatedAt)
	if pg.IsUniqueViolation(err) {
		return fmt.Errorf("仓库已存在: %s", r.ID)
	}
	return err
}

// DeleteRepo 删除指定仓库；跨租户访问返回 not found（不泄漏）。
func (s *Store) DeleteRepo(ctx context.Context, id string) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM code_repos WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("仓库不存在: %s", id)
	}
	return nil
}

// ---------- BuildRunRepository ----------

// ListBuildRuns 按租户 + 可选 appID 过滤；started_at 倒序（与内存版一致，最新在前）。
func (s *Store) ListBuildRuns(ctx context.Context, appID string) ([]devops.BuildRun, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + buildCols + ` FROM build_runs WHERE tenant_id=$1`
	args := []any{tid}
	if appID != "" {
		args = append(args, appID)
		q += fmt.Sprintf(` AND app_id=$%d`, len(args))
	}
	q += ` ORDER BY started_at DESC`
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]devops.BuildRun, 0)
	for rows.Next() {
		var b devops.BuildRun
		if err = scanBuild(rows, &b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetBuildRun 取单个构建；跨租户访问返回 not found（不泄漏）。
func (s *Store) GetBuildRun(ctx context.Context, id string) (devops.BuildRun, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return devops.BuildRun{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+buildCols+` FROM build_runs WHERE id=$1 AND tenant_id=$2`, id, tid)
	var b devops.BuildRun
	if err = scanBuild(row, &b); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return devops.BuildRun{}, fmt.Errorf("构建不存在: %s", id)
		}
		return devops.BuildRun{}, err
	}
	return b, nil
}

// scanBuild 通过 pg.RowScanner 抽象 QueryRow 与 Row 两种来源。
func scanBuild(r pg.RowScanner, b *devops.BuildRun) error {
	return r.Scan(
		&b.ID, &b.TenantID, &b.AppID, &b.RepoID, &b.Trigger, &b.Commit, &b.Branch,
		&b.Message, &b.Status, &b.ImageID, &b.Log, &b.StartedAt, &b.FinishedAt)
}

// CreateBuildRun 触发一次构建。校验仓库归属后置 pending，启动 mock CI runner 异步流转并产出 Image。
// 校验逻辑与内存版一致：仓库须存在 + 本租户 + AppID 匹配。
func (s *Store) CreateBuildRun(ctx context.Context, b devops.BuildRun) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	// 校验仓库归属（与内存版同款语义）
	var repo devops.CodeRepo
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+repoCols+` FROM code_repos WHERE id=$1 AND tenant_id=$2`, b.RepoID, tid)
	if err = row.Scan(&repo.ID, &repo.TenantID, &repo.AppID, &repo.GitURL, &repo.Branch,
		&repo.Dockerfile, &repo.BuildContext, &repo.Status, &repo.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("仓库不存在或不属于本应用: %s", b.RepoID)
		}
		return err
	}
	if repo.AppID != b.AppID {
		return fmt.Errorf("仓库不存在或不属于本应用: %s", b.RepoID)
	}

	b.ID = newID("build")
	b.TenantID = tid
	b.Status = devops.BuildPending
	b.StartedAt = time.Now()
	b.Branch = repo.Branch
	if b.Commit == "" {
		b.Commit = mockCommit()
	}
	if b.Message == "" {
		b.Message = "mock: update " + repo.Branch
	}
	if b.Trigger == "" {
		b.Trigger = devops.TriggerManual
	}
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO build_runs (`+buildCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		b.ID, b.TenantID, b.AppID, b.RepoID, b.Trigger, b.Commit, b.Branch, b.Message,
		b.Status, b.ImageID, b.Log, b.StartedAt, b.FinishedAt)
	if err != nil {
		return err
	}

	// CI runner：pending -> running -> success/failed（产出 Image）。
	// goroutine 持 *Store 引用 + 派生 ctx（不持请求 ctx，避免 cancel 影响，与内存版一致）。
	go s.runBuild(context.Background(), builder.Params{
		TenantID: tid, AppID: b.AppID, BuildID: b.ID, Commit: b.Commit, Branch: b.Branch,
		GitURL: repo.GitURL, Dockerfile: repo.Dockerfile, BuildContext: repo.BuildContext,
	}) //nolint:gosec // G118 误报：后台构建任务须脱离请求生命周期，不持 request ctx 是有意为之
	return nil
}

// runBuild 执行构建流水线并持久化状态。pipeline nil 时用 Mock。
// 流转用 UPDATE build_runs + INSERT images；panic/错误兜底标 failed。
func (s *Store) runBuild(ctx context.Context, p builder.Params) {
	pipe := s.pipeline
	if pipe == nil {
		pipe = builder.Mock{}
	}
	defer func() {
		if rec := recover(); rec != nil {
			_, _ = s.db.Pool().Exec(ctx,
				`UPDATE build_runs SET status=$2, finished_at=$3, log=$4 WHERE id=$1`,
				p.BuildID, devops.BuildFailed, time.Now(), fmt.Sprintf("构建异常: %v", rec))
		}
	}()
	// pending -> running
	if _, err := s.db.Pool().Exec(ctx,
		`UPDATE build_runs SET status=$2 WHERE id=$1`, p.BuildID, devops.BuildRunning); err != nil {
		return
	}

	// 执行流水线（clone→build→push 或 mock 派生）。
	res, err := pipe.Build(ctx, p)
	if err != nil {
		_, _ = s.db.Pool().Exec(ctx,
			`UPDATE build_runs SET status=$2, finished_at=$3, log=$4 WHERE id=$1`,
			p.BuildID, devops.BuildFailed, time.Now(), err.Error()+"\n"+res.Log)
		return
	}
	img := devops.Image{
		ID:         newID("img"),
		TenantID:   p.TenantID,
		AppID:      p.AppID,
		Registry:   builder.RegistryOrDefault(p),
		Tag:        res.Tag,
		Digest:     res.Digest,
		Source:     p.Commit,
		Branch:     p.Branch,
		BuildRunID: p.BuildID,
		BuiltAt:    time.Now(),
		Status:     devops.ImageReady,
	}
	if _, err := s.db.Pool().Exec(ctx,
		`INSERT INTO images (`+imageCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		img.ID, img.TenantID, img.AppID, img.Registry, img.Tag, img.Digest, img.Source,
		img.Branch, img.BuildRunID, img.BuiltAt, img.Status); err != nil {
		return
	}
	_, _ = s.db.Pool().Exec(ctx,
		`UPDATE build_runs SET status=$2, image_id=$3, finished_at=$4, log=$5 WHERE id=$1`,
		p.BuildID, devops.BuildSuccess, img.ID, time.Now(), res.Log)
}

// ---------- ImageRepository ----------

// ListImages 按租户 + 可选 appID 过滤；built_at 倒序（与内存版一致，最新在前）。
func (s *Store) ListImages(ctx context.Context, appID string) ([]devops.Image, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + imageCols + ` FROM images WHERE tenant_id=$1`
	args := []any{tid}
	if appID != "" {
		args = append(args, appID)
		q += fmt.Sprintf(` AND app_id=$%d`, len(args))
	}
	q += ` ORDER BY built_at DESC`
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]devops.Image, 0)
	for rows.Next() {
		var im devops.Image
		if err = scanImage(rows, &im); err != nil {
			return nil, err
		}
		out = append(out, im)
	}
	return out, rows.Err()
}

// GetImage 取单个镜像；跨租户访问返回 not found（不泄漏）。
func (s *Store) GetImage(ctx context.Context, id string) (devops.Image, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return devops.Image{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+imageCols+` FROM images WHERE id=$1 AND tenant_id=$2`, id, tid)
	var im devops.Image
	if err = scanImage(row, &im); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return devops.Image{}, fmt.Errorf("镜像不存在: %s", id)
		}
		return devops.Image{}, err
	}
	return im, nil
}

func scanImage(r pg.RowScanner, im *devops.Image) error {
	return r.Scan(
		&im.ID, &im.TenantID, &im.AppID, &im.Registry, &im.Tag, &im.Digest,
		&im.Source, &im.Branch, &im.BuildRunID, &im.BuiltAt, &im.Status)
}

// findImageIDByDigest 在本租户镜像中按 digest 反查 ID（Release 编排取回滚指针用）。
// 与内存版 findImageIDByDigest 同构；未找到返回空串。
func (s *Store) findImageIDByDigest(ctx context.Context, tid, digest string) string {
	var id string
	err := s.db.Pool().QueryRow(ctx,
		`SELECT id FROM images WHERE tenant_id=$1 AND digest=$2 LIMIT 1`, tid, digest).Scan(&id)
	if err != nil {
		return ""
	}
	return id
}

// ---------- ReleaseRepository ----------

// ListReleases 按租户 + 可选 appID 过滤；created_at 倒序（与内存版一致，最新在前）。
func (s *Store) ListReleases(ctx context.Context, appID string) ([]devops.Release, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + releaseCols + ` FROM releases WHERE tenant_id=$1`
	args := []any{tid}
	if appID != "" {
		args = append(args, appID)
		q += fmt.Sprintf(` AND app_id=$%d`, len(args))
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]devops.Release, 0)
	for rows.Next() {
		var r devops.Release
		if err = scanRelease(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRelease 取单个发布；跨租户访问返回 not found（不泄漏）。
func (s *Store) GetRelease(ctx context.Context, id string) (devops.Release, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return devops.Release{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+releaseCols+` FROM releases WHERE id=$1 AND tenant_id=$2`, id, tid)
	var r devops.Release
	if err = scanRelease(row, &r); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return devops.Release{}, fmt.Errorf("发布不存在: %s", id)
		}
		return devops.Release{}, err
	}
	return r, nil
}

func scanRelease(r pg.RowScanner, rel *devops.Release) error {
	return r.Scan(
		&rel.ID, &rel.TenantID, &rel.AppID, &rel.EnvID, &rel.ImageID, &rel.ImageDigest,
		&rel.Strategy, &rel.Status, &rel.WorkloadID, &rel.PreviousImageID, &rel.IsRollback,
		&rel.CreatedAt, &rel.CreatedBy)
}

// CreateRelease 编排发布：取镜像 -> 找/建目标环境基线 Workload -> 更新镜像 -> 记录回滚指针 -> 存发布单。
// **逻辑逐行对齐内存版**（internal/devops/memory/store.go CreateRelease），仅把内存 map 操作换成 SQL。
// 事务仅覆盖 releases 表写入；workload 侧操作经接口，失败按内存版同款回滚语义（不在 DB 层做跨 store 事务）。
func (s *Store) CreateRelease(ctx context.Context, input devops.ReleaseInput) (devops.Release, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return devops.Release{}, err
	}

	// 1. 取镜像（校验租户 + 归属本应用）—— 对齐内存版 step 1
	var img devops.Image
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+imageCols+` FROM images WHERE id=$1 AND tenant_id=$2`, input.ImageID, tid)
	if err = scanImage(row, &img); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return devops.Release{}, fmt.Errorf("镜像不存在或不属于本应用: %s", input.ImageID)
		}
		return devops.Release{}, err
	}
	if img.AppID != input.AppID {
		return devops.Release{}, fmt.Errorf("镜像不存在或不属于本应用: %s", input.ImageID)
	}

	display := img.Registry + ":" + img.Tag

	// 2. 找目标环境基线 Workload（锁外调 workload 仓储，避免跨仓储持锁）—— 对齐内存版 step 2
	wls, err := s.workload.List(ctx, input.EnvID, input.AppID, workload.TypeService)
	if err != nil {
		return devops.Release{}, err
	}

	var wl workload.Workload
	var previousImageID string
	if len(wls) > 0 {
		wl = wls[0]
		if wl.ImageRef != "" {
			previousImageID = s.findImageIDByDigest(ctx, tid, wl.ImageRef)
		}
		if _, err := s.workload.UpdateImage(ctx, wl.ID, display, img.Digest); err != nil {
			return devops.Release{}, err
		}
	} else {
		// 无基线 Workload -> 创建（基线 service，Replicas=1）—— 对齐内存版
		wl = workload.Workload{
			ID:        newID("wl"),
			AppID:     input.AppID,
			EnvID:     input.EnvID,
			LaneID:    workload.LaneDefault,
			Type:      workload.TypeService,
			Name:      input.AppID + "-svc",
			Image:     display,
			ImageRef:  img.Digest,
			Replicas:  1,
			Status:    workload.StatusDeploying,
			CreatedAt: time.Now(),
		}
		if err := s.workload.Create(ctx, wl); err != nil {
			return devops.Release{}, err
		}
	}

	// 3. 存发布单 —— 对齐内存版 step 3
	strategy := input.Strategy
	if strategy == "" {
		strategy = devops.StrategyRolling
	}
	rel := devops.Release{
		ID:              newID("rel"),
		TenantID:        tid,
		AppID:           input.AppID,
		EnvID:           input.EnvID,
		ImageID:         img.ID,
		ImageDigest:     img.Digest,
		Strategy:        strategy,
		Status:          devops.ReleaseSucceeded,
		WorkloadID:      wl.ID,
		PreviousImageID: previousImageID,
		CreatedBy:       input.CreatedBy,
		CreatedAt:       time.Now(),
	}
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO releases (`+releaseCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		rel.ID, rel.TenantID, rel.AppID, rel.EnvID, rel.ImageID, rel.ImageDigest,
		rel.Strategy, rel.Status, rel.WorkloadID, rel.PreviousImageID, rel.IsRollback,
		rel.CreatedAt, rel.CreatedBy)
	if err != nil {
		return devops.Release{}, err
	}
	return rel, nil
}

// RollbackRelease 回滚到上一镜像：更新 Workload 回退镜像 + 原发布标记 rolled-back + 新建回滚发布单。
// **逻辑逐行对齐内存版**（internal/devops/memory/store.go RollbackRelease）。
// 事务覆盖 releases 表（标 rolled-back + 新建回滚单两步用 tx，确保原子）；
// workload 侧 UpdateImage 在事务外经接口调用（与内存版同款语义，失败不隐藏）。
func (s *Store) RollbackRelease(ctx context.Context, releaseID string) (devops.Release, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return devops.Release{}, err
	}

	// 1. 取原发布 + 校验 + 取上一镜像 —— 对齐内存版 step 1
	var orig devops.Release
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+releaseCols+` FROM releases WHERE id=$1 AND tenant_id=$2`, releaseID, tid)
	if err = scanRelease(row, &orig); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return devops.Release{}, fmt.Errorf("发布不存在: %s", releaseID)
		}
		return devops.Release{}, err
	}
	if orig.PreviousImageID == "" {
		return devops.Release{}, fmt.Errorf("发布 %s 无上一镜像，无法回滚", releaseID)
	}
	var prevImg devops.Image
	row2 := s.db.Pool().QueryRow(ctx,
		`SELECT `+imageCols+` FROM images WHERE id=$1 AND tenant_id=$2`, orig.PreviousImageID, tid)
	if err = scanImage(row2, &prevImg); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return devops.Release{}, fmt.Errorf("上一镜像不存在: %s", orig.PreviousImageID)
		}
		return devops.Release{}, err
	}

	// 2. 更新 Workload 回退镜像（经接口） —— 对齐内存版 step 2
	display := prevImg.Registry + ":" + prevImg.Tag
	if _, err := s.workload.UpdateImage(ctx, orig.WorkloadID, display, prevImg.Digest); err != nil {
		return devops.Release{}, err
	}

	// 3. 原 release 标 rolled-back + 新建 is_rollback=true release —— 对齐内存版 step 3
	// 用事务保证 releases 表两步原子（与内存版同持锁语义对齐）。
	rb := devops.Release{
		ID:              newID("rel"),
		TenantID:        tid,
		AppID:           orig.AppID,
		EnvID:           orig.EnvID,
		ImageID:         prevImg.ID,
		ImageDigest:     prevImg.Digest,
		Strategy:        orig.Strategy,
		Status:          devops.ReleaseSucceeded,
		WorkloadID:      orig.WorkloadID,
		PreviousImageID: orig.ImageID,
		IsRollback:      true,
		CreatedAt:       time.Now(),
	}
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return devops.Release{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // 已提交或失败均无害
	if _, err = tx.Exec(ctx,
		`UPDATE releases SET status=$2 WHERE id=$1`, releaseID, devops.ReleaseRolledBack); err != nil {
		return devops.Release{}, err
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO releases (`+releaseCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		rb.ID, rb.TenantID, rb.AppID, rb.EnvID, rb.ImageID, rb.ImageDigest,
		rb.Strategy, rb.Status, rb.WorkloadID, rb.PreviousImageID, rb.IsRollback,
		rb.CreatedAt, rb.CreatedBy); err != nil {
		return devops.Release{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return devops.Release{}, err
	}
	return rb, nil
}

// ---------- Count 方法（供 PG seed 判空，表空才灌，幂等；不经租户过滤，仅启动期用） ----------

// ReposCount 返回 code_repos 全表行数。
func (s *Store) ReposCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM code_repos`).Scan(&n)
	return n, err
}

// BuildRunsCount 返回 build_runs 全表行数。
func (s *Store) BuildRunsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM build_runs`).Scan(&n)
	return n, err
}

// ImagesCount 返回 images 全表行数。
func (s *Store) ImagesCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM images`).Scan(&n)
	return n, err
}

// ReleasesCount 返回 releases 全表行数。
func (s *Store) ReleasesCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM releases`).Scan(&n)
	return n, err
}

// SeedIfEmpty 在表空时灌入 seed 真源（启动期用，幂等）。
//
// 直接 SQL INSERT 绕过 CreateBuildRun（goroutine 异步流转）和 CreateRelease（workload 编排），
// 保留 seed 的已完成状态（BuildSuccess/ImageReady/ReleaseSucceeded）。
// repos/builds/images/releases 全表为空才灌（任一表已灌过即跳过）；主键冲突 DO NOTHING。
// 不经租户过滤，全表一次灌完所有租户的 seed 数据（与 billing/cc SeedIfEmpty 同款）。
func (s *Store) SeedIfEmpty(ctx context.Context) error {
	n, err := s.ReposCount(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	repos, builds, images, releases := devopsmemory.SeedDevOps()
	for _, r := range repos {
		if _, err = s.db.Pool().Exec(ctx,
			`INSERT INTO code_repos (`+repoCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			 ON CONFLICT (id) DO NOTHING`,
			r.ID, r.TenantID, r.AppID, r.GitURL, r.Branch, r.Dockerfile, r.BuildContext, r.Status, r.CreatedAt); err != nil {
			return err
		}
	}
	for _, b := range builds {
		if _, err = s.db.Pool().Exec(ctx,
			`INSERT INTO build_runs (`+buildCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			 ON CONFLICT (id) DO NOTHING`,
			b.ID, b.TenantID, b.AppID, b.RepoID, b.Trigger, b.Commit, b.Branch, b.Message,
			b.Status, b.ImageID, b.Log, b.StartedAt, b.FinishedAt); err != nil {
			return err
		}
	}
	for _, im := range images {
		if _, err = s.db.Pool().Exec(ctx,
			`INSERT INTO images (`+imageCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			 ON CONFLICT (id) DO NOTHING`,
			im.ID, im.TenantID, im.AppID, im.Registry, im.Tag, im.Digest, im.Source,
			im.Branch, im.BuildRunID, im.BuiltAt, im.Status); err != nil {
			return err
		}
	}
	for _, rl := range releases {
		if _, err = s.db.Pool().Exec(ctx,
			`INSERT INTO releases (`+releaseCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
			 ON CONFLICT (id) DO NOTHING`,
			rl.ID, rl.TenantID, rl.AppID, rl.EnvID, rl.ImageID, rl.ImageDigest, rl.Strategy,
			rl.Status, rl.WorkloadID, rl.PreviousImageID, rl.IsRollback, rl.CreatedAt, rl.CreatedBy); err != nil {
			return err
		}
	}
	return nil
}

// ---------- 辅助（与内存版同款，供 PG store 内部复用） ----------

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// newID 生成带前缀的短 ID（sha256 前 12 hex）。mock 期保证基本唯一，与内存版同款。
func newID(prefix string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d-%s", time.Now().UnixNano(), prefix)))
	return prefix + "-" + hex.EncodeToString(h[:6])
}

func mockCommit() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("commit-%d", time.Now().UnixNano())))
	return hex.EncodeToString(h[:20]) // 40 hex chars
}

func safeShort(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func mockBuildLog(tag string) string {
	return fmt.Sprintf(`Step 1/5: FROM golang:1.22 AS build
Step 2/5: WORKDIR /src && COPY . .
Step 3/5: RUN CGO_ENABLED=0 go build -o /out/app ./cmd/app
Step 4/5: FROM gcr.io/distroless/static
Step 5/5: COPY /out/app /app && ENTRYPOINT ["/app"]
=> 推送镜像: %s`, tag)
}
