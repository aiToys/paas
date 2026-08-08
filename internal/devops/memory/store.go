// Package memory 提供 devops 四仓储的内存实现 + mock CI runner + Release 编排。
//
// 一个 Store 同时实现 CodeRepoRepository/BuildRunRepository/ImageRepository/ReleaseRepository。
// 实体间联动（BuildRun 产出 Image、Release 读 Image 并编排 Workload）在进程内共享状态完成。
// mock CI runner 用 goroutine 模拟构建延迟与状态流转，不接真实 Git/Docker/Registry。
package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/devops"
	"github.com/aitoys/paas/internal/devops/builder"
	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store 实现 devops 全部四个仓储接口。workload 仓储由 cmd/core 注入，供 Release 编排。
type Store struct {
	mu        sync.Mutex
	repos     map[string]devops.CodeRepo
	buildruns map[string]devops.BuildRun
	images    map[string]devops.Image
	releases  map[string]devops.Release
	workload  workload.Repository // 注入；Release 编排找/建/更新基线 Workload
	pipeline  builder.Pipeline    // 构建流水线（nil=Mock）；cmd/core 按 PAAS_DEVOPS_REAL 注入 Real
	baseCtx   context.Context     // 进程级 ctx（cmd/core 注入）；构建 goroutine 派生之，nil=Background 兼容
}

// NewStore 创建仓储（空，不 seed mock 演示数据）。wlRepo 为 Release 编排提供 Workload 能力。
// 去假数据：用户绑定真实 git 仓库 + 触发构建产生仓库/构建/镜像/发布记录。
func NewStore(wlRepo workload.Repository) *Store {
	s := &Store{
		repos:     map[string]devops.CodeRepo{},
		buildruns: map[string]devops.BuildRun{},
		images:    map[string]devops.Image{},
		releases:  map[string]devops.Release{},
		workload:  wlRepo,
	}
	return s
}

func tenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		return "", fmt.Errorf("missing tenant context")
	}
	return tid, nil
}

// ---------- CodeRepoRepository ----------

func (s *Store) ListRepos(ctx context.Context, appID string) ([]devops.CodeRepo, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]devops.CodeRepo, 0)
	for _, r := range s.repos {
		if r.TenantID != tid {
			continue
		}
		if appID != "" && r.AppID != appID {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) GetRepo(ctx context.Context, id string) (devops.CodeRepo, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return devops.CodeRepo{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.repos[id]
	if !ok || r.TenantID != tid {
		return devops.CodeRepo{}, fmt.Errorf("仓库不存在: %s", id)
	}
	return r, nil
}

func (s *Store) CreateRepo(ctx context.Context, r devops.CodeRepo) error {
	tid, err := tenantOrErr(ctx)
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.ID == "" {
		r.ID = newID("repo")
	}
	if _, exists := s.repos[r.ID]; exists {
		return fmt.Errorf("仓库已存在: %s", r.ID)
	}
	r.TenantID = tid
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	s.repos[r.ID] = r
	return nil
}

func (s *Store) DeleteRepo(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.repos[id]
	if !ok || r.TenantID != tid {
		return fmt.Errorf("仓库不存在: %s", id)
	}
	delete(s.repos, id)
	return nil
}

// ---------- BuildRunRepository ----------

func (s *Store) ListBuildRuns(ctx context.Context, appID string) ([]devops.BuildRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]devops.BuildRun, 0)
	for _, b := range s.buildruns {
		if b.TenantID != tid {
			continue
		}
		if appID != "" && b.AppID != appID {
			continue
		}
		out = append(out, b)
	}
	// 倒序：最新构建在前
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

// ListAllBuildRuns 跨租户列出全部构建（admin 平台总览，不过滤 tenant；按 TenantID 升序再 StartedAt 倒序）。
func (s *Store) ListAllBuildRuns(ctx context.Context) ([]devops.BuildRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]devops.BuildRun, 0, len(s.buildruns))
	for _, b := range s.buildruns {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out, nil
}

func (s *Store) GetBuildRun(ctx context.Context, id string) (devops.BuildRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return devops.BuildRun{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.buildruns[id]
	if !ok || b.TenantID != tid {
		return devops.BuildRun{}, fmt.Errorf("构建不存在: %s", id)
	}
	return b, nil
}

// Create 触发一次构建。校验仓库归属后置 pending，启动 mock CI runner 异步流转并产出 Image。
func (s *Store) CreateBuildRun(ctx context.Context, b devops.BuildRun) (devops.BuildRun, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return devops.BuildRun{}, err
	}
	s.mu.Lock()
	repo, ok := s.repos[b.RepoID]
	if !ok || repo.TenantID != tid || repo.AppID != b.AppID {
		s.mu.Unlock()
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
	s.buildruns[b.ID] = b
	s.mu.Unlock()

	// CI runner：pending -> running -> success/failed（产出 Image）。
	// 脱离请求 ctx（后台构建不应因请求取消而中断）；pipeline 决定 mock/real。
	// CI runner：pending -> running -> success/failed（产出 Image）。
	// baseCtx 派生自进程级 ctx（cmd/core 注入）：进程退出时 cancel → 构建中断，避免卡 running。
	// internal 仓库用 CloneURL（含 Gitea basic auth）；external 用 GitURL（+ injectToken 注 PAAS_GIT_TOKEN）。
	gitURL := repo.CloneURL
	if gitURL == "" {
		gitURL = repo.GitURL
	}
	go s.runBuild(s.baseCtxOrBg(), builder.Params{
		TenantID: tid, AppID: b.AppID, BuildID: b.ID, Commit: b.Commit, Branch: b.Branch,
		GitURL: gitURL, Dockerfile: repo.Dockerfile, BuildContext: repo.BuildContext,
	}) //nolint:gosec // G118 误报：后台构建须脱离请求生命周期，不持 request ctx 是有意为之
	return b, nil
}

// SetPipeline 注入构建流水线（cmd/core 按 PAAS_DEVOPS_REAL 注入 Real）；nil=Mock。
// 加锁写：防与 runBuild 异步读 s.pipeline 的 race（go test -race 会捕获）。
func (s *Store) SetPipeline(p builder.Pipeline) {
	s.mu.Lock()
	s.pipeline = p
	s.mu.Unlock()
}

// SetBaseCtx 注入进程级 ctx（cmd/core 在 run() 注入）；构建 goroutine 派生之感知 shutdown。
func (s *Store) SetBaseCtx(ctx context.Context) { s.baseCtx = ctx }

// baseCtxOrBg 返回 baseCtx（空则 Background，兼容测试/未注入场景）。
func (s *Store) baseCtxOrBg() context.Context {
	if s.baseCtx != nil {
		return s.baseCtx
	}
	return context.Background()
}

// runBuild 执行构建流水线并持久化状态。pipeline nil 时用 Mock。
func (s *Store) runBuild(ctx context.Context, p builder.Params) {
	s.mu.Lock()
	pipe := s.pipeline
	s.mu.Unlock()
	if pipe == nil {
		pipe = builder.Mock{}
	}
	defer func() {
		if rec := recover(); rec != nil {
			s.mu.Lock()
			b := s.buildruns[p.BuildID]
			b.Status = devops.BuildFailed
			b.FinishedAt = time.Now()
			// panic 栈可能含 cloneURL=https://<PAAS_GIT_TOKEN>@...，统一 MaskToken 脱敏（与正常错误路径一致），
			// 防 build:read 权限者经 GET /api/buildruns/{id} 读到平台 Git 凭证。
			b.Log = builder.MaskToken(fmt.Sprintf("构建异常: %v", rec))
			s.buildruns[p.BuildID] = b
			s.mu.Unlock()
		}
	}()
	// pending -> running
	s.mu.Lock()
	b := s.buildruns[p.BuildID]
	b.Status = devops.BuildRunning
	s.buildruns[p.BuildID] = b
	s.mu.Unlock()

	// 执行流水线（clone→build→push 或 mock 派生）。
	res, err := pipe.Build(ctx, p)
	res.Log = builder.MaskToken(res.Log) // 脱敏日志中的 Git token（防泄漏给 build:read 权限者）
	if err != nil {
		err = builder.MaskErr(err) // err 可能含 git clone 失败 stderr（含 token URL）
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b = s.buildruns[p.BuildID]
	b.FinishedAt = time.Now()
	if err != nil {
		b.Status = devops.BuildFailed
		b.Log = err.Error() + "\n" + res.Log
		s.buildruns[p.BuildID] = b
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
	b.Status = devops.BuildSuccess
	b.ImageID = img.ID
	b.Log = res.Log
	s.buildruns[p.BuildID] = b
	s.images[img.ID] = img
}

// ---------- ImageRepository ----------

func (s *Store) ListImages(ctx context.Context, appID string) ([]devops.Image, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]devops.Image, 0)
	for _, im := range s.images {
		if im.TenantID != tid {
			continue
		}
		if appID != "" && im.AppID != appID {
			continue
		}
		out = append(out, im)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BuiltAt.After(out[j].BuiltAt) })
	return out, nil
}

// ListAllImages 跨租户列出全部镜像（admin 平台总览，不过滤 tenant；按 TenantID 升序再 BuiltAt 倒序）。
func (s *Store) ListAllImages(ctx context.Context) ([]devops.Image, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]devops.Image, 0, len(s.images))
	for _, im := range s.images {
		out = append(out, im)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].BuiltAt.After(out[j].BuiltAt)
	})
	return out, nil
}

func (s *Store) GetImage(ctx context.Context, id string) (devops.Image, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return devops.Image{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	im, ok := s.images[id]
	if !ok || im.TenantID != tid {
		return devops.Image{}, fmt.Errorf("镜像不存在: %s", id)
	}
	return im, nil
}

// findImageIDByDigest 在本租户镜像中按 digest 反查 ID（Release 编排取回滚指针用）。
// 对外不加锁版本：调用方已持 s.mu（CreateRelease 临界区内调用，否则自死锁——Go mutex 不可重入）。
func (s *Store) findImageIDByDigest(tid, digest string) string {
	for _, im := range s.images {
		if im.TenantID == tid && im.Digest == digest {
			return im.ID
		}
	}
	return ""
}

// ---------- ReleaseRepository ----------

func (s *Store) ListReleases(ctx context.Context, appID string) ([]devops.Release, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]devops.Release, 0)
	for _, r := range s.releases {
		if r.TenantID != tid {
			continue
		}
		if appID != "" && r.AppID != appID {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// ListAllReleases 跨租户列出全部发布（admin 平台总览，不过滤 tenant；按 TenantID 升序再 CreatedAt 倒序）。
func (s *Store) ListAllReleases(ctx context.Context) ([]devops.Release, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]devops.Release, 0, len(s.releases))
	for _, r := range s.releases {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *Store) GetRelease(ctx context.Context, id string) (devops.Release, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return devops.Release{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.releases[id]
	if !ok || r.TenantID != tid {
		return devops.Release{}, fmt.Errorf("发布不存在: %s", id)
	}
	return r, nil
}

// SetReleaseVersion 回填版本号（baseline stage 打版本）。
func (s *Store) SetReleaseVersion(ctx context.Context, id, version string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.releases[id]
	if !ok || r.TenantID != tid {
		return fmt.Errorf("发布不存在: %s", id)
	}
	r.Version = version
	s.releases[id] = r
	return nil
}

// CreateRelease 编排发布：取镜像 -> 找/建目标环境基线 Workload -> 更新镜像 -> 记录回滚指针 -> 存发布单。
// 策略非 rolling 也走 rolling 编排（mock 期无真实流量切分），Release.Strategy 记录请求策略。
func (s *Store) CreateRelease(ctx context.Context, input devops.ReleaseInput) (devops.Release, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return devops.Release{}, err
	}

	// 1. 取镜像（校验租户 + 归属本应用）
	s.mu.Lock()
	img, ok := s.images[input.ImageID]
	if !ok || img.TenantID != tid || img.AppID != input.AppID {
		s.mu.Unlock()
		return devops.Release{}, fmt.Errorf("镜像不存在或不属于本应用: %s", input.ImageID)
	}
	s.mu.Unlock()

	display := img.Registry + ":" + img.Tag

	// 2-3. 持 s.mu 覆盖 List→UpdateImage→存 release 临界区，防并发发布丢失更新（回滚指针链断裂）。
	// 跨仓储嵌套 devops.mu → workload.mu 单向（workload 不调 devops），无死锁；CreateRelease 低频可接受串行化。
	s.mu.Lock()
	defer s.mu.Unlock()

	// 2. 找目标环境基线 Workload（Task 2 暂传 lane="" 保持原行为；Task 3 接入 lane 参数）
	wls, err := s.workload.List(ctx, input.EnvID, input.AppID, "", workload.TypeService)
	if err != nil {
		return devops.Release{}, err
	}

	var wl workload.Workload
	var previousImageID string
	if len(wls) > 0 {
		wl = wls[0]
		if wl.ImageRef != "" {
			previousImageID = s.findImageIDByDigest(tid, wl.ImageRef)
		}
		if _, err := s.workload.UpdateImage(ctx, wl.ID, display, img.Digest); err != nil {
			return devops.Release{}, err
		}
	} else {
		// 无基线 Workload -> 创建（基线 service，Replicas=1）
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

	// 3. 存发布单
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
	}
	s.releases[rel.ID] = rel // 已在 s.mu 临界区内（step2-3 持锁）
	return rel, nil
}

// RollbackRelease 回滚到上一镜像：更新 Workload 回退镜像 + 原发布标记 rolled-back + 新建回滚发布单。
func (s *Store) RollbackRelease(ctx context.Context, releaseID string) (devops.Release, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return devops.Release{}, err
	}

	s.mu.Lock()
	orig, ok := s.releases[releaseID]
	if !ok || orig.TenantID != tid {
		s.mu.Unlock()
		return devops.Release{}, fmt.Errorf("发布不存在: %s", releaseID)
	}
	if orig.PreviousImageID == "" {
		s.mu.Unlock()
		return devops.Release{}, fmt.Errorf("发布 %s 无上一镜像，无法回滚", releaseID)
	}
	prevImg, ok := s.images[orig.PreviousImageID]
	if !ok {
		s.mu.Unlock()
		return devops.Release{}, fmt.Errorf("上一镜像不存在: %s", orig.PreviousImageID)
	}
	s.mu.Unlock()

	display := prevImg.Registry + ":" + prevImg.Tag
	if _, err := s.workload.UpdateImage(ctx, orig.WorkloadID, display, prevImg.Digest); err != nil {
		return devops.Release{}, err
	}

	s.mu.Lock()
	orig.Status = devops.ReleaseRolledBack
	s.releases[releaseID] = orig
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
	s.releases[rb.ID] = rb
	s.mu.Unlock()
	return rb, nil
}

// PromoteRelease 把源 release 的镜像发布到 targetEnvID（流水线逐级提升）。
// 复用 CreateRelease 编排（找/建基线 Workload + 回滚指针），新 release 标 PromotedFrom=源 ID。
// targetEnvID 由 handler 经 environment.NextPromoteTarget 算出（store 不感知 env 阶序）。
// 不持锁调 CreateRelease（其内部自持 s.mu，Go mutex 不可重入会自死锁），PromotedFrom 标记单独锁内写。
func (s *Store) PromoteRelease(ctx context.Context, srcReleaseID, targetEnvID string) (devops.Release, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return devops.Release{}, err
	}
	s.mu.Lock()
	src, ok := s.releases[srcReleaseID]
	s.mu.Unlock()
	if !ok || src.TenantID != tid {
		return devops.Release{}, fmt.Errorf("发布不存在: %s", srcReleaseID)
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
	s.mu.Lock()
	rel.PromotedFrom = srcReleaseID
	s.releases[rel.ID] = rel
	s.mu.Unlock()
	return rel, nil
}

// ---------- 辅助 ----------

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// newID 生成带前缀的短 ID（sha256 前 12 hex）。mock 期保证基本唯一。
func newID(prefix string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d-%s", time.Now().UnixNano(), prefix)))
	return prefix + "-" + hex.EncodeToString(h[:6])
}

func mockCommit() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("commit-%d", time.Now().UnixNano())))
	return hex.EncodeToString(h[:20]) // 40 hex chars
}

func mockBuildLog(tag string) string {
	return fmt.Sprintf(`Step 1/5: FROM golang:1.22 AS build
Step 2/5: WORKDIR /src && COPY . .
Step 3/5: RUN CGO_ENABLED=0 go build -o /out/app ./cmd/app
Step 4/5: FROM gcr.io/distroless/static
Step 5/5: COPY /out/app /app && ENTRYPOINT ["/app"]
=> 推送镜像: %s`, tag)
}

// SeedDevOps 返回平台预置的 devops 四实体示例（repos/builds/images/releases），
// 供内存仓储自灌与 PG 仓储迁移后 seed 复用同一真源（DRY）。
func SeedDevOps() (repos []devops.CodeRepo, builds []devops.BuildRun, images []devops.Image, releases []devops.Release) {
	return seed()
}

// seed 生成跨两租户的演示数据，对齐已有应用/环境/工作负载 seed：
//   - acme: app-cs 仓库 + 构建 + 镜像 -> 发布到 env-acme-test（wl-cs-api）
//   - globex: app-agent 仓库 + 构建 + 镜像 -> 发布到 env-globex-prod（wl-agent-gw）
func seed() (repos []devops.CodeRepo, builds []devops.BuildRun, images []devops.Image, releases []devops.Release) {
	t := time.Now()
	commitAcme := "a1b2c3d4e5f67890a1b2c3d4e5f67890a1b2c3d4"
	commitGlobex := "b2c3d4e5f6789012b2c3d4e5f6789012b2c3d4e5"

	repos = []devops.CodeRepo{
		{ID: "repo-acme-cs", TenantID: "t-acme", AppID: "app-cs", GitURL: "https://github.com/acme/cs-svc.git", Branch: "main", Dockerfile: devops.DefaultDockerfile, BuildContext: devops.DefaultBuildContext, Status: devops.RepoStatusActive, CreatedAt: t},
		{ID: "repo-globex-agent", TenantID: "t-globex", AppID: "app-agent", GitURL: "https://github.com/globex/agent-platform.git", Branch: "main", Dockerfile: devops.DefaultDockerfile, BuildContext: devops.DefaultBuildContext, Status: devops.RepoStatusActive, CreatedAt: t},
	}

	images = []devops.Image{
		{ID: "img-acme-001", TenantID: "t-acme", AppID: "app-cs", Registry: "registry.paas.local/app-cs", Tag: "main-a1b2c3d4", Digest: "sha256:" + sha256hex("acme-001"+commitAcme), Source: commitAcme, Branch: "main", BuildRunID: "build-acme-001", BuiltAt: t, Status: devops.ImageReady},
		{ID: "img-globex-001", TenantID: "t-globex", AppID: "app-agent", Registry: "registry.paas.local/app-agent", Tag: "main-b2c3d4e5", Digest: "sha256:" + sha256hex("globex-001"+commitGlobex), Source: commitGlobex, Branch: "main", BuildRunID: "build-globex-001", BuiltAt: t, Status: devops.ImageReady},
	}

	builds = []devops.BuildRun{
		{ID: "build-acme-001", TenantID: "t-acme", AppID: "app-cs", RepoID: "repo-acme-cs", Trigger: devops.TriggerPush, Commit: commitAcme, Branch: "main", Message: "feat: 优化多模型路由", Status: devops.BuildSuccess, ImageID: "img-acme-001", Log: mockBuildLog("main-a1b2c3d4"), StartedAt: t, FinishedAt: t},
		{ID: "build-globex-001", TenantID: "t-globex", AppID: "app-agent", RepoID: "repo-globex-agent", Trigger: devops.TriggerPush, Commit: commitGlobex, Branch: "main", Message: "feat: 工具调用链路", Status: devops.BuildSuccess, ImageID: "img-globex-001", Log: mockBuildLog("main-b2c3d4e5"), StartedAt: t, FinishedAt: t},
	}

	releases = []devops.Release{
		{ID: "rel-acme-001", TenantID: "t-acme", AppID: "app-cs", EnvID: "env-acme-test", ImageID: "img-acme-001", ImageDigest: images[0].Digest, Strategy: devops.StrategyRolling, Status: devops.ReleaseSucceeded, WorkloadID: "wl-cs-api", CreatedAt: t, CreatedBy: "u-acme-admin"},
		{ID: "rel-globex-001", TenantID: "t-globex", AppID: "app-agent", EnvID: "env-globex-prod", ImageID: "img-globex-001", ImageDigest: images[1].Digest, Strategy: devops.StrategyRolling, Status: devops.ReleaseSucceeded, WorkloadID: "wl-agent-gw", CreatedAt: t, CreatedBy: "u-globex-admin"},
	}
	return
}
