package devops

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

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
type EnvTypeResolver interface {
	EnvType(ctx context.Context, envID string) (string, error)
}

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

// WithUserIDFrom 注入用户 ID 解析器，填充 Release.CreatedBy。
func WithUserIDFrom(f func(context.Context) string) HandlerOpt {
	return func(h *Handler) { h.UserIDFrom = f }
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
	case strings.HasSuffix(path, "/rollback"):
		h.serveReleaseAction(w, r)
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
		if err := h.repos.CreateRepo(r.Context(), repo); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		httputil.WriteJSON(w, http.StatusCreated, repo)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
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
		httputil.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]string{"deleted": repoID})
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
		if err := h.builds.CreateBuildRun(r.Context(), b); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, err.Error())
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
		httputil.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, b)
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
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": list})
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
		httputil.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, im)
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
			httputil.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		httputil.WriteJSON(w, http.StatusCreated, rel)
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
	if len(parts) != 2 || parts[1] != "rollback" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	releaseID := parts[0]
	// 生产环境回滚需 prod:write：先取发布单的环境类型再校验
	orig, err := h.releases.GetRelease(r.Context(), releaseID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	if !h.allowProd(w, r, orig.EnvID) {
		return
	}
	rb, err := h.releases.RollbackRelease(r.Context(), releaseID)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	httputil.WriteJSON(w, http.StatusOK, rb)
}
