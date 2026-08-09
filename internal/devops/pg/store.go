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
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/internal/devops/builder"
	"github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/internal/workload"
)

// Store 实现 devops 全部四个仓储接口。workload 仓储由 cmd/core 注入，供 Release 编排。
type Store struct {
	db       *pg.DB
	workload workload.Repository // 注入；Release 编排找/建/更新基线 Workload
	pipeline builder.Pipeline    // 构建流水线（nil=Mock）；cmd/core 按 PAAS_DEVOPS_REAL 注入 Real
	pipeMu   sync.RWMutex        // 专管 pipeline 字段读写（防 SetPipeline/runBuild race）
	baseCtx  context.Context     // 进程级 ctx（cmd/core 注入）；构建 goroutine 派生之，nil=Background 兼容
}

// NewStore 创建 devops PG 仓储。db 必须已完成迁移；wlRepo 为 Release 编排提供 Workload 能力。
// 与内存版 memory.NewStore(wlRepo) 同构——对 workload 存储后端完全透明。
func NewStore(db *pg.DB, wlRepo workload.Repository) *Store {
	return &Store{db: db, workload: wlRepo}
}

// SetPipeline 注入构建流水线（cmd/core 按 PAAS_DEVOPS_REAL 注入 Real）；nil=Mock。
// SetPipeline 注入构建流水线（cmd/core 按 PAAS_DEVOPS_REAL 注入 Real）；nil=Mock。
// 加锁写：防与 runBuild 异步读 s.pipeline 的 race。
func (s *Store) SetPipeline(p builder.Pipeline) {
	s.pipeMu.Lock()
	s.pipeline = p
	s.pipeMu.Unlock()
}

// SetBaseCtx 注入进程级 ctx（cmd/core 在 run() 注入）；构建 goroutine 派生之感知 shutdown。
func (s *Store) SetBaseCtx(ctx context.Context) { s.baseCtx = ctx }

// SweepInterrupted 把进程重启中断的构建（pending/running）标记为 failed。启动时调用一次。
// 复现：INSERT(pending) 后 goroutine 启动前崩溃 → 永卡 pending；构建中途 kill -9/OOM/Pod 强删 → 永卡 running。
// 正常 SIGTERM 有 baseCtx cancel 兜底标 failed，但强杀不覆盖，故启动 sweep 兜底（无超时/回收机制时的最简恢复）。
func (s *Store) SweepInterrupted(ctx context.Context) error {
	_, err := s.db.Pool().Exec(ctx,
		`UPDATE build_runs SET status=$1, finished_at=NOW(), log=$2 WHERE status IN ('pending','running')`,
		devops.BuildFailed, "进程重启中断构建")
	return err
}

// baseCtxOrBg 返回 baseCtx（空则 Background，兼容测试/未注入场景）。
func (s *Store) baseCtxOrBg() context.Context {
	if s.baseCtx != nil {
		return s.baseCtx
	}
	return context.Background()
}

// 列常量与各 struct 字段顺序严格对齐（scan 列序必须一致）。
const (
	repoCols    = `id, tenant_id, app_id, git_url, branch, dockerfile, build_context, status, created_at, source, gitea_owner, gitea_repo, clone_url`
	buildCols   = `id, tenant_id, app_id, repo_id, trigger, commit, branch, message, status, image_id, log, build_args, started_at, finished_at`
	imageCols   = `id, tenant_id, app_id, registry, tag, digest, source, branch, build_run_id, built_at, status, version`
	releaseCols = `id, tenant_id, app_id, env_id, image_id, image_digest, strategy, status, workload_id, previous_image_id, is_rollback, created_at, created_by, promoted_from, version, lane_id, source_run_id`
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
			&r.Dockerfile, &r.BuildContext, &r.Status, &r.CreatedAt,
			&r.Source, &r.GiteaOwner, &r.GiteaRepo, &r.CloneURL); err != nil {
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
		&r.Dockerfile, &r.BuildContext, &r.Status, &r.CreatedAt,
		&r.Source, &r.GiteaOwner, &r.GiteaRepo, &r.CloneURL); err != nil {
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
	if r.Source == "" {
		r.Source = devops.RepoSourceExternal
	}
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO code_repos (`+repoCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		r.ID, r.TenantID, r.AppID, r.GitURL, r.Branch, r.Dockerfile, r.BuildContext, r.Status, r.CreatedAt,
		r.Source, r.GiteaOwner, r.GiteaRepo, r.CloneURL)
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

// ListAllBuildRuns 跨租户列出全部构建（admin 平台总览，不过滤 tenant；按 tenant_id, started_at DESC 排序）。
func (s *Store) ListAllBuildRuns(ctx context.Context) ([]devops.BuildRun, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+buildCols+` FROM build_runs ORDER BY tenant_id, started_at DESC`)
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
	var buildArgs []byte
	if err := r.Scan(
		&b.ID, &b.TenantID, &b.AppID, &b.RepoID, &b.Trigger, &b.Commit, &b.Branch,
		&b.Message, &b.Status, &b.ImageID, &b.Log, &buildArgs, &b.StartedAt, &b.FinishedAt); err != nil {
		return err
	}
	b.BuildArgs = unmarshalStrMap(buildArgs)
	return nil
}

// marshalStrMap 把 map[string]string 序列化为 JSONB 字节；nil/空 -> '{}'（与列 DEFAULT 一致）。
func marshalStrMap(m map[string]string) []byte {
	if len(m) == 0 {
		return []byte("{}")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return []byte("{}")
	}
	return b
}

// unmarshalStrMap 反序列化 JSONB 为 map[string]string；nil/空/null/无效 -> 空 map（非 nil，nil 安全）。
func unmarshalStrMap(raw []byte) map[string]string {
	m := map[string]string{}
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "{}" {
		return m
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]string{} // 容错：单行坏数据不阻塞整个 List
	}
	return m
}

// CreateBuildRun 触发一次构建。校验仓库归属后置 pending，启动 mock CI runner 异步流转并产出 Image。
// 校验逻辑与内存版一致：仓库须存在 + 本租户 + AppID 匹配。
func (s *Store) CreateBuildRun(ctx context.Context, b devops.BuildRun) (devops.BuildRun, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return devops.BuildRun{}, err
	}
	// 校验仓库归属（与内存版同款语义）
	var repo devops.CodeRepo
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+repoCols+` FROM code_repos WHERE id=$1 AND tenant_id=$2`, b.RepoID, tid)
	if err = row.Scan(&repo.ID, &repo.TenantID, &repo.AppID, &repo.GitURL, &repo.Branch,
		&repo.Dockerfile, &repo.BuildContext, &repo.Status, &repo.CreatedAt,
		&repo.Source, &repo.GiteaOwner, &repo.GiteaRepo, &repo.CloneURL); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return devops.BuildRun{}, fmt.Errorf("仓库不存在或不属于本应用: %s", b.RepoID)
		}
		return devops.BuildRun{}, err
	}
	if repo.AppID != b.AppID {
		return devops.BuildRun{}, fmt.Errorf("仓库不存在或不属于本应用: %s", b.RepoID)
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
		`INSERT INTO build_runs (`+buildCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		b.ID, b.TenantID, b.AppID, b.RepoID, b.Trigger, b.Commit, b.Branch, b.Message,
		b.Status, b.ImageID, b.Log, marshalStrMap(b.BuildArgs), b.StartedAt, b.FinishedAt)
	if err != nil {
		return devops.BuildRun{}, err
	}

	// CI runner：pending -> running -> success/failed（产出 Image）。
	// goroutine 持 *Store 引用 + 派生 baseCtx（进程级，不持请求 ctx；进程退出 cancel 构建中断）。
	// internal 仓库用 CloneURL（含 Gitea basic auth）；external 用 GitURL（+ injectToken 注 PAAS_GIT_TOKEN）。
	gitURL := repo.CloneURL
	if gitURL == "" {
		gitURL = repo.GitURL
	}
	go s.runBuild(s.baseCtxOrBg(), builder.Params{
		TenantID: tid, AppID: b.AppID, BuildID: b.ID, Commit: b.Commit, Branch: b.Branch,
		GitURL: gitURL, Dockerfile: repo.Dockerfile, BuildContext: repo.BuildContext, BuildArgs: b.BuildArgs,
	}) //nolint:gosec // G118 误报：后台构建任务须脱离请求生命周期，不持 request ctx 是有意为之
	return b, nil
}

// runBuild 执行构建流水线并持久化状态。pipeline nil 时用 Mock。
// 流转用 UPDATE build_runs + INSERT images；panic/错误兜底标 failed。
func (s *Store) runBuild(ctx context.Context, p builder.Params) {
	s.pipeMu.RLock()
	pipe := s.pipeline
	s.pipeMu.RUnlock()
	if pipe == nil {
		pipe = builder.Mock{}
	}
	defer func() {
		if rec := recover(); rec != nil {
			// panic 栈可能含 cloneURL=https://<PAAS_GIT_TOKEN>@...，统一 MaskToken 脱敏（与正常错误路径一致），
			// 防 build:read 权限者经 GET /api/buildruns/{id} 读到平台 Git 凭证。
			// WithoutCancel：baseCtx cancel 后仍需落库失败状态（与 markBuildFailed 同理）。
			_, _ = s.db.Pool().Exec(context.WithoutCancel(ctx),
				`UPDATE build_runs SET status=$2, finished_at=$3, log=$4 WHERE id=$1`,
				p.BuildID, devops.BuildFailed, time.Now(), builder.MaskToken(fmt.Sprintf("构建异常: %v", rec)))
		}
	}()
	// pending -> running
	if _, err := s.db.Pool().Exec(ctx,
		`UPDATE build_runs SET status=$2 WHERE id=$1`, p.BuildID, devops.BuildRunning); err != nil {
		return
	}

	// 执行流水线（clone→build→push 或 mock 派生）。
	res, err := pipe.Build(ctx, p)
	res.Log = builder.MaskToken(res.Log) // 脱敏日志中的 Git token（防泄漏给 build:read 权限者）
	if err != nil {
		err = builder.MaskErr(err) // err 可能含 git clone 失败 stderr（含 token URL）
		// WithoutCancel：ctx 可能因 SIGTERM 已 cancel（pipe.Build 返 err），但失败状态仍需落库。
		_, _ = s.db.Pool().Exec(context.WithoutCancel(ctx),
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
	// 镜像落库 + 构建成功状态同一事务（原子），任一步失败回写 failed + 日志，
	// 避免「镜像已落库但 build_run 卡 running」的孤儿镜像（runBuild 无返回值，靠状态机驱动）。
	tx, txErr := s.db.Pool().Begin(ctx)
	if txErr != nil {
		s.markBuildFailed(ctx, p.BuildID, "开启事务失败: "+txErr.Error())
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	if _, err := tx.Exec(ctx,
		`INSERT INTO images (`+imageCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		img.ID, img.TenantID, img.AppID, img.Registry, img.Tag, img.Digest, img.Source,
		img.Branch, img.BuildRunID, img.BuiltAt, img.Status, img.Version); err != nil {
		s.markBuildFailed(ctx, p.BuildID, "镜像落库失败: "+err.Error()+"\n"+res.Log)
		return
	}
	if _, err := tx.Exec(ctx,
		`UPDATE build_runs SET status=$2, image_id=$3, finished_at=$4, log=$5 WHERE id=$1`,
		p.BuildID, devops.BuildSuccess, img.ID, time.Now(), res.Log); err != nil {
		s.markBuildFailed(ctx, p.BuildID, "构建状态回写失败: "+err.Error()+"\n"+res.Log)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		s.markBuildFailed(ctx, p.BuildID, "事务提交失败: "+err.Error()+"\n"+res.Log)
		return
	}
	committed = true
}

// markBuildFailed 回写构建失败状态 + 日志（best-effort，错误不再向上传播因 runBuild 无返回值）。
// 用 WithoutCancel ctx：SIGTERM 时 baseCtx 已 cancel，但失败状态必须落库（否则 build_run 永久卡 running，
// 只能等下次启动 SweepInterrupted 兜底）。pipe.Build 仍用原 ctx 响应取消，仅落库路径脱离请求生命周期。
func (s *Store) markBuildFailed(ctx context.Context, buildID, log string) {
	_, _ = s.db.Pool().Exec(context.WithoutCancel(ctx),
		`UPDATE build_runs SET status=$2, finished_at=$3, log=$4 WHERE id=$1`,
		buildID, devops.BuildFailed, time.Now(), log)
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

// ListAllImages 跨租户列出全部镜像（admin 平台总览，不过滤 tenant；按 tenant_id, built_at DESC 排序）。
func (s *Store) ListAllImages(ctx context.Context) ([]devops.Image, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+imageCols+` FROM images ORDER BY tenant_id, built_at DESC`)
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
		&im.Source, &im.Branch, &im.BuildRunID, &im.BuiltAt, &im.Status, &im.Version)
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

// ListAllReleases 跨租户列出全部发布（admin 平台总览，不过滤 tenant；按 tenant_id, created_at DESC 排序）。
func (s *Store) ListAllReleases(ctx context.Context) ([]devops.Release, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+releaseCols+` FROM releases ORDER BY tenant_id, created_at DESC`)
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
		&rel.CreatedAt, &rel.CreatedBy, &rel.PromotedFrom, &rel.Version, &rel.LaneID, &rel.SourceRunID)
}

// SetReleaseVersion 回填版本号（baseline stage 打版本）。
func (s *Store) SetReleaseVersion(ctx context.Context, id, version string) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE releases SET version=$1 WHERE id=$2 AND tenant_id=$3`, version, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("发布不存在: %s", id)
	}
	return nil
}

// MarkSourceRun 给部署记录回填触发它的 pipeline run ID（deploy stage 经 Deploy 接口写入）。
// 注：source_run_id 列由 migration 0022（Task 10）添加；列存在前运行时会报错，
// 默认 go test 不跑 integration，memory 路径完全可用。
func (s *Store) MarkSourceRun(ctx context.Context, id, runID string) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE releases SET source_run_id=$1 WHERE id=$2 AND tenant_id=$3`, runID, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("发布不存在: %s", id)
	}
	return nil
}

// SetVersion 给镜像回填正式版本号（release stage 打版本时调）。
// 注：images.version 列由 migration 0022（Task 10）添加；列存在前运行时会报错，
// 默认 go test 不跑 integration，memory 路径完全可用。
func (s *Store) SetVersion(ctx context.Context, id, version string) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE images SET version=$1 WHERE id=$2 AND tenant_id=$3`, version, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("镜像不存在: %s", id)
	}
	return nil
}

// CreateRelease 编排发布：取镜像 -> 找/建目标环境基线 Workload -> 更新镜像 -> 记录回滚指针 -> 存发布单。
// **逻辑逐行对齐内存版**（internal/devops/memory/store.go CreateRelease），仅把内存 map 操作换成 SQL。
// 事务仅覆盖 releases 表写入；workload 侧操作经接口，失败按内存版同款回滚语义（不在 DB 层做跨 store 事务）。
func (s *Store) CreateRelease(ctx context.Context, input devops.ReleaseInput) (devops.Release, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return devops.Release{}, err
	}

	// per-(app,env) 串行化：advisory xact lock 防并发发布丢失更新。
	// 复现：两并发发布同 app/env 各 List 读到相同 previousImageID，后写覆盖 -> 回滚指针链断裂。
	// lockTx 仅持锁，defer Rollback 在函数返回时（xact 结束）释放，覆盖整个发布临界区（List→UpdateImage→INSERT）。
	lockTx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return devops.Release{}, err
	}
	defer func() { _ = lockTx.Rollback(ctx) }()
	if _, err := lockTx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtext($1))`, input.AppID+"|"+input.EnvID); err != nil {
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

	// 泳道归一化：空串 -> 默认基线（向后兼容历史调用方）。
	lane := input.LaneID
	if lane == "" {
		lane = workload.LaneDefault
	}

	// 2. 找目标环境某泳道的基线 Workload（同 app×env×lane 唯一）（锁外调 workload 仓储，避免跨仓储持锁）
	// —— 对齐内存版 step 2
	wls, err := s.workload.List(ctx, input.EnvID, input.AppID, lane, workload.TypeService)
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
		// Name 规则：default 用 `<app>-svc`（兼容现有 seed），非 default 用 `<app>-svc-<lane>`。
		name := input.AppID + "-svc"
		if lane != workload.LaneDefault {
			name = input.AppID + "-svc-" + lane
		}
		wl = workload.Workload{
			ID:        newID("wl"),
			AppID:     input.AppID,
			EnvID:     input.EnvID,
			LaneID:    lane,
			Type:      workload.TypeService,
			Name:      name,
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
		Version:         "", // 初始为空，由 baseline stage 写入
		LaneID:          lane,
	}
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO releases (`+releaseCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		rel.ID, rel.TenantID, rel.AppID, rel.EnvID, rel.ImageID, rel.ImageDigest,
		rel.Strategy, rel.Status, rel.WorkloadID, rel.PreviousImageID, rel.IsRollback,
		rel.CreatedAt, rel.CreatedBy, rel.PromotedFrom, rel.Version, rel.LaneID, rel.SourceRunID)
	if err != nil {
		// 补偿事务：workload 已切新镜像但 release 记录未落库 -> 回滚 workload 到发布前状态，
		// 防丢失 PreviousImageID 回滚指针（best-effort，补偿失败不掩盖主错误）。
		// WithoutCancel 派生 ctx：客户端断连（ctx canceled）不阻断补偿，
		// 否则 workload 已切新镜像但 release 未落库 -> 回滚指针永久丢失；补偿失败打 error 日志。
		cctx := context.WithoutCancel(ctx)
		if len(wls) > 0 {
			if _, cerr := s.workload.UpdateImage(cctx, wl.ID, wl.Image, wl.ImageRef); cerr != nil {
				log.Printf("[devops] CreateRelease 补偿失败（恢复原镜像）: release=%s workload=%s: %v", rel.ID, wl.ID, cerr)
			}
		} else {
			if derr := s.workload.Delete(cctx, wl.ID); derr != nil { // 无基线时新建的 workload，删除
				log.Printf("[devops] CreateRelease 补偿失败（删除新 workload）: release=%s workload=%s: %v", rel.ID, wl.ID, derr)
			}
		}
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
	// 取原 release 对应镜像的 display，供 tx 失败时补偿恢复 workload 用（best-effort）。
	var origImg devops.Image
	origDisplay := ""
	if err = scanImage(s.db.Pool().QueryRow(ctx,
		`SELECT `+imageCols+` FROM images WHERE id=$1 AND tenant_id=$2`, orig.ImageID, tid), &origImg); err == nil {
		origDisplay = origImg.Registry + ":" + origImg.Tag
	}
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
		Version:         "", // 回滚发布初始为空
	}
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return devops.Release{}, err
	}
	committed := false
	defer func() {
		_ = tx.Rollback(ctx) // 已提交或失败均无害
		if !committed && origDisplay != "" {
			// tx 失败：workload 已回退到 prevImg 但 release 未记录 -> 补偿恢复到原镜像（best-effort）。
			// WithoutCancel 派生 ctx：客户端断连不阻断补偿；补偿失败打 error 日志（原静默吞错）。
			cctx := context.WithoutCancel(ctx)
			if _, cerr := s.workload.UpdateImage(cctx, orig.WorkloadID, origDisplay, orig.ImageDigest); cerr != nil {
				log.Printf("[devops] RollbackRelease 补偿失败（恢复原镜像）: release=%s workload=%s: %v", releaseID, orig.WorkloadID, cerr)
			}
		}
	}()
	if _, err = tx.Exec(ctx,
		`UPDATE releases SET status=$2 WHERE id=$1`, releaseID, devops.ReleaseRolledBack); err != nil {
		return devops.Release{}, err
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO releases (`+releaseCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		rb.ID, rb.TenantID, rb.AppID, rb.EnvID, rb.ImageID, rb.ImageDigest,
		rb.Strategy, rb.Status, rb.WorkloadID, rb.PreviousImageID, rb.IsRollback,
		rb.CreatedAt, rb.CreatedBy, rb.PromotedFrom, rb.Version, rb.LaneID, rb.SourceRunID); err != nil {
		return devops.Release{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return devops.Release{}, err
	}
	committed = true
	return rb, nil
}

// PromoteRelease 把源 release 镜像发布到 targetEnvID（流水线逐级提升），复用 CreateRelease 编排，
// 新 release 标 promoted_from=源 ID。targetEnvID 由 handler 经 environment.NextPromoteTarget 算出。
// GetRelease 已校验租户隔离（跨租户 not found 不泄漏）。promoted_from 标记 best-effort（rel 已建，
// UPDATE 失败仅丢失来源追溯，不影响编排正确性）。
func (s *Store) PromoteRelease(ctx context.Context, srcReleaseID, targetEnvID string) (devops.Release, error) {
	src, err := s.GetRelease(ctx, srcReleaseID)
	if err != nil {
		return devops.Release{}, err
	}
	rel, err := s.CreateRelease(ctx, devops.ReleaseInput{
		AppID:     src.AppID,
		EnvID:     targetEnvID,
		ImageID:   src.ImageID,
		Strategy:  src.Strategy,
		CreatedBy: src.CreatedBy,
	})
	if err != nil {
		return devops.Release{}, err
	}
	if _, err = s.db.Pool().Exec(ctx,
		`UPDATE releases SET promoted_from=$2 WHERE id=$1`, rel.ID, srcReleaseID); err != nil {
		return rel, err
	}
	rel.PromotedFrom = srcReleaseID
	return rel, nil
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
	// 去假数据：不灌 mock 仓库/构建/镜像/发布。用户绑定真实 git 仓库 + 触发构建产生真实记录。
	// 保留签名兼容 seedPGAllIfEmpty 调用。
	return nil
}

// ---------- 辅助（与内存版同款，供 PG store 内部复用） ----------

// newID 生成带前缀的短 ID（sha256 前 12 hex）。mock 期保证基本唯一，与内存版同款。
func newID(prefix string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d-%s", time.Now().UnixNano(), prefix)))
	return prefix + "-" + hex.EncodeToString(h[:6])
}

func mockCommit() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("commit-%d", time.Now().UnixNano())))
	return hex.EncodeToString(h[:20]) // 40 hex chars
}
