package devops

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/aitoys/paas/internal/devops/gitea"
	"github.com/aitoys/paas/internal/devops/registry"
	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/internal/httputil"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermRepoRead     = "repository:read"
	PermRepoWrite    = "repository:write"
	PermBuildRead    = "build:read"
	PermBuildWrite   = "build:write"
	PermImageRead    = "image:read"
	PermReleaseRead  = "release:read"
	PermReleaseWrite = "release:write"
	// PermProdWrite 生产环境写操作额外权限；developer 无此权限 -> 生产只读。
	PermProdWrite = "prod:write"
)

// EnvTypeResolver 解析环境类型（prod|test），用于生产写权限校验。
// 依赖倒置：devops 不直接 import environment，由 cmd/core 注入实现。
type EnvTypeResolver = environment.EnvTypeResolver

// EnvPromoter 解析发布流水线下一个晋升目标环境，用于 promote 端点逐级提升。
// 依赖倒置：cmd/core 注入 environment.Repository 实现。
type EnvPromoter = environment.EnvPromoter

// Handler 暴露 DevOps REST API。
//
// 路由：
//
//	GET    /api/applications/{id}/repositories        仓库列表
//	POST   /api/applications/{id}/repositories        绑定仓库
//	DELETE /api/applications/{id}/repositories/{rid}  解绑
//	GET    /api/applications/{id}/buildruns           构建记录
//	POST   /api/applications/{id}/buildruns           触发构建
//	GET    /api/buildruns/{bid}                       构建详情（含日志）
//	GET    /api/applications/{id}/images              镜像列表
//	GET    /api/images/{iid}                          镜像详情
//	GET    /api/applications/{id}/releases            发布历史
//	POST   /api/applications/{id}/releases            创建发布
//	POST   /api/releases/{rid}/rollback               回滚
type Handler struct {
	repos       CodeRepoRepository
	builds      BuildRunRepository
	images      ImageRepository
	releases    ReleaseRepository
	envResolver EnvTypeResolver
	// envPromoter 解析发布流水线下一个晋升目标环境；nil 时 promote 端点返 400（未配置流水线）。
	envPromoter EnvPromoter
	// giteaClient 内置 Git 后端客户端；nil 时 internal 来源建仓/浏览不可用（降级 503）。
	giteaClient *gitea.Client
	// registryClient 镜像库 v2 客户端；nil 时 registry 实时视图不可用（降级 503）。
	registryClient *registry.Client
	// Authorize 校验当前请求是否持有权限；nil 跳过（测试场景）。
	Authorize func(r *http.Request, perm string) bool
	// UserIDFrom 从身份 ctx 取用户 ID（填 Release.CreatedBy）；nil 则空。
	UserIDFrom func(ctx context.Context) string
}

// NewHandler 创建 DevOps handler。
func NewHandler(repos CodeRepoRepository, builds BuildRunRepository, images ImageRepository, releases ReleaseRepository, opts ...HandlerOpt) *Handler {
	h := &Handler{repos: repos, builds: builds, images: images, releases: releases}
	for _, o := range opts {
		o(h)
	}
	return h
}

// HandlerOpt 配置 Handler。
type HandlerOpt func(*Handler)

// WithEnvResolver 注入环境类型解析器，启用生产写权限校验。
func WithEnvResolver(r EnvTypeResolver) HandlerOpt {
	return func(h *Handler) { h.envResolver = r }
}

// WithEnvPromoter 注入晋升目标解析器，启用发布流水线 promote 端点。
func WithEnvPromoter(p EnvPromoter) HandlerOpt {
	return func(h *Handler) { h.envPromoter = p }
}

// WithUserIDFrom 注入用户 ID 解析器，填充 Release.CreatedBy。
func WithUserIDFrom(f func(context.Context) string) HandlerOpt {
	return func(h *Handler) { h.UserIDFrom = f }
}

// WithGiteaClient 注入内置 Git 后端客户端，启用 internal 来源建仓 + 仓库浏览。
func WithGiteaClient(c *gitea.Client) HandlerOpt {
	return func(h *Handler) { h.giteaClient = c }
}

// WithRegistryClient 注入镜像库 v2 客户端，启用 registry 实时视图（catalog/tags）。
func WithRegistryClient(c *registry.Client) HandlerOpt {
	return func(h *Handler) { h.registryClient = c }
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// allowProd 校验目标环境的生产写权限。
// 未注入 envResolver 或 envID 为空时跳过；非生产或不存在放行（不存在由后续 repo 报错）；
// 生产则校验 prod:write（developer 被拦 -> 生产只读）。
func (h *Handler) allowProd(w http.ResponseWriter, r *http.Request, envID string) bool {
	if h.envResolver == nil || envID == "" {
		return true
	}
	etype, err := h.envResolver.EnvType(r.Context(), envID)
	// fail-closed：环境查不到（不存在/跨租户）保守按生产处理，需 prod:write。
	if err != nil || etype == "prod" {
		return h.allow(w, r, PermProdWrite)
	}
	return true
}

func (h *Handler) userID(ctx context.Context) string {
	if h.UserIDFrom == nil {
		return ""
	}
	return h.UserIDFrom(ctx)
}

// ServeHTTP 按路径前缀分发到应用子路由或详情/动作路由。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case strings.HasPrefix(path, "/api/applications/"):
		h.serveApp(w, r)
	case path == "/api/buildruns":
		// 跨应用列表（appID="" = 租户内全部）
		h.serveBuildRuns(w, r, "")
	case strings.HasPrefix(path, "/api/buildruns/"):
		h.serveBuildDetail(w, r)
	case path == "/api/images":
		h.serveImages(w, r, "")
	case strings.HasPrefix(path, "/api/images/"):
		h.serveImageDetail(w, r)
	case path == "/api/releases":
		h.serveReleases(w, r, "")
	case strings.HasSuffix(path, "/rollback"), strings.HasSuffix(path, "/promote"):
		h.serveReleaseAction(w, r)
	case path == "/api/registry/repositories":
		// registry 实时视图：列 hub.wang.dd 所有镜像仓库名（catalog）
		h.serveRegistryCatalog(w, r)
	case path == "/api/registry/tags":
		// registry 实时视图：某仓库的 tag + digest（?repository=paas/paas-core）
		h.serveRegistryTags(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveApp 处理 /api/applications/{id}/{sub}[/{rid}]。
func (h *Handler) serveApp(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/applications/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) < 2 {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	appID, sub := parts[0], parts[1]
	switch sub {
	case "repositories":
		switch len(parts) {
		case 2:
			h.serveRepos(w, r, appID)
		case 3:
			h.serveRepoDelete(w, r, parts[2])
		case 4:
			// /repositories/{rid}/{tree|commits} 仓库内容浏览（仅 internal）
			h.serveRepoBrowse(w, r, parts[2], parts[3])
		default:
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
	case "buildruns":
		if len(parts) == 2 {
			h.serveBuildRuns(w, r, appID)
		} else {
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
	case "images":
		if len(parts) == 2 {
			h.serveImages(w, r, appID)
		} else {
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
	case "releases":
		if len(parts) == 2 {
			h.serveReleases(w, r, appID)
		} else {
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// ---------- 仓库 ----------

func (h *Handler) serveRepos(w http.ResponseWriter, r *http.Request, appID string) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermRepoRead) {
			return
		}
		list, err := h.repos.ListRepos(r.Context(), appID)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermRepoWrite) {
			return
		}
		var repo CodeRepo
		if err := json.NewDecoder(r.Body).Decode(&repo); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		repo.AppID = appID
		if repo.Source == "" {
			repo.Source = RepoSourceExternal
		}
		// 内置仓库：调 Gitea 建仓 + 回填 clone URL（含/不含凭证分离）。
		if repo.Source == RepoSourceInternal {
			if h.giteaClient == nil {
				httputil.WriteError(w, http.StatusServiceUnavailable, "内置 Git 后端未启用")
				return
			}
			if repo.GiteaRepo == "" {
				httputil.WriteError(w, http.StatusBadRequest, "字段非法或缺失: giteaRepo")
				return
			}
			gRepo, err := h.giteaClient.CreateRepo(r.Context(), gitea.CreateRepoInput{
				Name:          repo.GiteaRepo,
				DefaultBranch: repo.Branch,
				Private:       true,
				AutoInit:      true, // 初始化 README 使默认分支存在，clone/浏览可用
			})
			if err != nil {
				h.writeGiteaErr(w, err)
				return
			}
			repo.GiteaOwner = h.giteaClient.Username()
			repo.GitURL = gRepo.CloneURL                                                    // 展示用（不含凭证）
			repo.CloneURL = h.giteaClient.CloneURLWithAuth(repo.GiteaOwner, repo.GiteaRepo) // builder clone 用（含凭证）
			if repo.Branch == "" {
				repo.Branch = gRepo.DefaultBranch
			}
		}
		if err := repo.Validate(); err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		if repo.Status == "" {
			repo.Status = RepoStatusActive
		}
		if err := h.repos.CreateRepo(r.Context(), repo); err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		httputil.WriteDataCreated(w, repo)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// writeGiteaErr 把 Gitea client 错误映射到 HTTP 状态（参考 maas provider 模式）。
func (h *Handler) writeGiteaErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, gitea.ErrRepoExists):
		httputil.WriteError(w, http.StatusConflict, "仓库已存在")
	case errors.Is(err, gitea.ErrGiteaUnavailable):
		httputil.WriteError(w, http.StatusServiceUnavailable, "Git 后端不可达")
	case errors.Is(err, gitea.ErrUnauthorized):
		httputil.WriteError(w, http.StatusServiceUnavailable, "Git 后端鉴权失败")
	default:
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
	}
}

func (h *Handler) serveRepoDelete(w http.ResponseWriter, r *http.Request, repoID string) {
	if r.Method != http.MethodDelete {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermRepoWrite) {
		return
	}
	if err := h.repos.DeleteRepo(r.Context(), repoID); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, map[string]string{"deleted": repoID})
}

// serveRepoBrowse 处理 /api/applications/{id}/repositories/{rid}/{action}。
// action: tree（文件树）/ commits（提交历史）/ file（单文件内容，?path=）。仅 internal 仓库支持（external 返 405）。
func (h *Handler) serveRepoBrowse(w http.ResponseWriter, r *http.Request, repoID, action string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermRepoRead) {
		return
	}
	if h.giteaClient == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "内置 Git 后端未启用")
		return
	}
	repo, err := h.repos.GetRepo(r.Context(), repoID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "仓库不存在")
		return
	}
	if repo.Source != RepoSourceInternal {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "外部仓库不支持平台内浏览，请到外部 Git 平台查看")
		return
	}
	owner := repo.GiteaOwner
	if owner == "" {
		owner = h.giteaClient.Username()
	}
	name := repo.GiteaRepo
	ref := r.URL.Query().Get("ref")
	switch action {
	case "tree":
		tree, err := h.giteaClient.GetTree(r.Context(), owner, name, ref)
		if err != nil {
			h.writeGiteaErr(w, err)
			return
		}
		httputil.WriteData(w, tree)
	case "commits":
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		commits, err := h.giteaClient.ListCommits(r.Context(), owner, name, limit)
		if err != nil {
			h.writeGiteaErr(w, err)
			return
		}
		httputil.WriteData(w, commits)
	case "file":
		// 单文件内容（base64 解码为字符串）：点击文件树节点查看代码，避免「文件打不开」。
		path := r.URL.Query().Get("path")
		if path == "" {
			httputil.WriteError(w, http.StatusBadRequest, "path 参数不能为空")
			return
		}
		content, fc, err := h.giteaClient.GetFileContent(r.Context(), owner, name, path, ref)
		if err != nil {
			h.writeGiteaErr(w, err)
			return
		}
		httputil.WriteData(w, map[string]any{"path": fc.Path, "name": fc.Name, "size": fc.Size, "content": content})
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// ---------- 镜像库实时视图（registry v2）----------

// serveRegistryCatalog 列 registry 所有镜像仓库名（GET /api/registry/repositories）。
// 复用 image:read 权限。registry 不可达返 503（降级，不 panic）。
func (h *Handler) serveRegistryCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermImageRead) {
		return
	}
	if h.registryClient == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "镜像库实时视图未启用")
		return
	}
	names, err := h.registryClient.Catalog(r.Context())
	if err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "镜像库不可达")
		return
	}
	// 转为对象数组（前端统一 .data 解包），便于未来加 tag 数/最新推送等聚合字段
	out := make([]map[string]any, 0, len(names))
	for _, n := range names {
		out = append(out, map[string]any{"name": n})
	}
	httputil.WriteData(w, out)
}

// serveRegistryTags 列某仓库的 tag + digest（GET /api/registry/tags?repository=paas/paas-core）。
func (h *Handler) serveRegistryTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermImageRead) {
		return
	}
	if h.registryClient == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "镜像库实时视图未启用")
		return
	}
	repo := r.URL.Query().Get("repository")
	if repo == "" {
		httputil.WriteError(w, http.StatusBadRequest, "字段非法或缺失: repository")
		return
	}
	tags, err := h.registryClient.Tags(r.Context(), repo)
	if err != nil {
		if errors.Is(err, registry.ErrRepoNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "镜像仓库不存在")
			return
		}
		httputil.WriteError(w, http.StatusServiceUnavailable, "镜像库不可达")
		return
	}
	httputil.WriteData(w, tags)
}

// ---------- 构建 ----------

func (h *Handler) serveBuildRuns(w http.ResponseWriter, r *http.Request, appID string) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermBuildRead) {
			return
		}
		list, err := h.builds.ListBuildRuns(r.Context(), appID)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermBuildWrite) {
			return
		}
		var b BuildRun
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		b.AppID = appID
		if b.RepoID == "" {
			httputil.WriteError(w, http.StatusBadRequest, "repoId 不能为空")
			return
		}
		// internal 仓库：从 Gitea 拿最新 commit sha + message 填入（替代 store 的 mock），
		// 使构建记录的 commit/tag/message 真实（builder script COMMIT 非空则不 rev-parse，直接用此 sha 生成 tag）。
		// external 仓库或 Gitea 不可达时回退 store mock（保持原行为）。
		if b.Commit == "" && h.giteaClient != nil {
			if repo, rerr := h.repos.GetRepo(r.Context(), b.RepoID); rerr == nil && repo.Source == RepoSourceInternal {
				owner := repo.GiteaOwner
				if owner == "" {
					owner = h.giteaClient.Username()
				}
				if cs, gerr := h.giteaClient.ListCommits(r.Context(), owner, repo.GiteaRepo, 1); gerr == nil && len(cs) > 0 {
					b.Commit = cs[0].SHA
					if b.Message == "" {
						b.Message = cs[0].Message
					}
				}
			}
		}
		if _, err := h.builds.CreateBuildRun(r.Context(), b); err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		httputil.WriteJSON(w, http.StatusCreated, map[string]string{"status": "triggered"})
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// serveBuildDetail 处理 /api/buildruns/{id}。
func (h *Handler) serveBuildDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermBuildRead) {
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/buildruns/"), "/")
	if id == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	b, err := h.builds.GetBuildRun(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, b)
}

// ---------- 镜像 ----------

func (h *Handler) serveImages(w http.ResponseWriter, r *http.Request, appID string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermImageRead) {
		return
	}
	list, err := h.images.ListImages(r.Context(), appID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, list)
}

// serveImageDetail 处理 /api/images/{id}。
func (h *Handler) serveImageDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermImageRead) {
		return
	}
	id := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/images/"), "/")
	if id == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	im, err := h.images.GetImage(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, im)
}

// ---------- 发布 ----------

func (h *Handler) serveReleases(w http.ResponseWriter, r *http.Request, appID string) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermReleaseRead) {
			return
		}
		list, err := h.releases.ListReleases(r.Context(), appID)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermReleaseWrite) {
			return
		}
		var input ReleaseInput
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		input.AppID = appID
		input.CreatedBy = h.userID(r.Context())
		// 生产环境发布需 prod:write（developer 被拦，生产只读）
		if !h.allowProd(w, r, input.EnvID) {
			return
		}
		rel, err := h.releases.CreateRelease(r.Context(), input)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		httputil.WriteDataCreated(w, rel)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// serveReleaseAction 处理 /api/releases/{id}/rollback。
func (h *Handler) serveReleaseAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermReleaseWrite) {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/releases/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) != 2 {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	releaseID, action := parts[0], parts[1]
	orig, err := h.releases.GetRelease(r.Context(), releaseID)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	switch action {
	case "rollback":
		// 生产环境回滚需 prod:write：先取发布单的环境类型再校验
		if !h.allowProd(w, r, orig.EnvID) {
			return
		}
		rb, err := h.releases.RollbackRelease(r.Context(), releaseID)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		httputil.WriteData(w, rb)
	case "promote":
		// 发布流水线逐级提升：算下个阶序环境，目标 prod 需 prod:write。
		if h.envPromoter == nil {
			httputil.WriteError(w, http.StatusBadRequest, "未配置发布流水线（晋升目标解析器未注入）")
			return
		}
		target, err := h.envPromoter.NextPromoteTarget(r.Context(), orig.EnvID)
		if err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "已是最高阶环境，无晋升目标")
			return
		}
		if !h.allowProd(w, r, target.ID) {
			return
		}
		rel, err := h.releases.PromoteRelease(r.Context(), releaseID, target.ID)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		httputil.WriteData(w, rel)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}
