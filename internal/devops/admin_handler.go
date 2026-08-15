// Package devops admin_handler.go 提供 DevOps admin REST API（构建/镜像/发布 跨租户管理）。
//
// 路由（全挂 adminGuard(super_admin)，cmd/core 装配；handler 内不重复 Authorize）：
//
//	GET    /api/admin/buildruns/{id}        跨租户构建详情（含 Log 字段）
//	GET    /api/admin/images/{id}           跨租户镜像详情
//	GET    /api/admin/releases/{id}         跨租户发布详情
//	POST   /api/admin/releases/{id}/rollback   跨租户回滚（绕过 prod:write，super_admin 有权干预生产）
//
// 不代建（业务编排类）；BuildRun/Image/Release Repository 无 Delete 方法，故不提供删除端点；
// BuildRun 重试涉及异步构建流转（baseCtx/pipeline 注入），不在 admin handler 干净复用，YAGNI 跳过；
// 镜像不可变，无 L2 写操作。
//
// 跨租户单条读：devops 三个 Repository 只有 ListAll*（无 GetAny），统一 ListAllXxx filter by id
// （与 workload admin handler 同款）。
package devops

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	adminutil "github.com/aitoys/paas/internal/web/admin"
)

// AdminAuditRecorder admin 写操作审计（依赖倒置，避免 devops->security）。
// tenantID = 资源所属租户（target_tenant）；actor = super_admin UserID；action 带 admin: 前缀。
type AdminAuditRecorder = adminutil.AuditRecorder // admin 写操作审计（依赖倒置，统一真源 internal/web/admin）

// AdminHandler 暴露 DevOps admin REST API（/api/admin/buildruns|images|releases 路径前缀分发）。
//
// 注入：BuildRunRepository/ImageRepository/ReleaseRepository（既有接口，含 ListAll*）+
// AdminAuditRecorder + actor 提取器。
type AdminHandler struct {
	builds  BuildRunRepository
	images  ImageRepository
	release ReleaseRepository
	audit   AdminAuditRecorder
	actorOf func(*http.Request) string
}

// AdminHandlerOpt admin handler 配置。
type AdminHandlerOpt func(*AdminHandler)

// NewAdminHandler 创建 admin handler。
func NewAdminHandler(builds BuildRunRepository, images ImageRepository, release ReleaseRepository, opts ...AdminHandlerOpt) *AdminHandler {
	h := &AdminHandler{builds: builds, images: images, release: release}
	for _, o := range opts {
		o(h)
	}
	return h
}

// WithAdminAudit 注入审计 recorder。
func WithAdminAudit(a AdminAuditRecorder) AdminHandlerOpt {
	return func(h *AdminHandler) { h.audit = a }
}

// WithAdminActor 注入 actor 提取器（取 super_admin UserID 作审计 actor）。
func WithAdminActor(f func(*http.Request) string) AdminHandlerOpt {
	return func(h *AdminHandler) { h.actorOf = f }
}

// ServeHTTP 按路径前缀分发到对应实体 handler。
// 注意：/api/admin/buildruns（无尾斜杠）GET 列表由 cmd/core reg.Register 直接处理；
// 这里只处理 /api/admin/buildruns/（有尾斜杠，{id} 路径）。
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case strings.HasPrefix(path, "/api/admin/buildruns/"):
		h.serveBuildRun(w, r)
	case strings.HasPrefix(path, "/api/admin/images/"):
		h.serveImage(w, r)
	case strings.HasPrefix(path, "/api/admin/releases/"):
		h.serveRelease(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// adminTenantCtx 派生资源所属租户 ctx（admin 跨租户操作以资源租户身份执行下游）。
func (h *AdminHandler) actor(r *http.Request) string {
	if h.actorOf != nil {
		return h.actorOf(r)
	}
	return "admin"
}

// recordAudit best-effort 记审计（错误不影响主流程）。
func (h *AdminHandler) recordAudit(r *http.Request, tenantID, action, resourceType, resourceID, detail string) {
	if h.audit == nil {
		return
	}
	_ = h.audit.Record(r.Context(), tenantID, h.actor(r), action, resourceType, resourceID, detail)
}

// ---------- 构建 ----------

// findBuildByID 跨租户取单条构建（ListAll filter by id）。Repository.Get 强制 ctx tenant，admin 需绕过。
func (h *AdminHandler) findBuildByID(ctx context.Context, id string) (BuildRun, error) {
	list, err := h.builds.ListAllBuildRuns(ctx)
	if err != nil {
		return BuildRun{}, err
	}
	for _, b := range list {
		if b.ID == id {
			return b, nil
		}
	}
	return BuildRun{}, fmt.Errorf("构建不存在: %s", id)
}

// serveBuildRun 处理 /api/admin/buildruns/{id}。
func (h *AdminHandler) serveBuildRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/admin/buildruns/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 1 || parts[0] == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	id := parts[0]
	b, err := h.findBuildByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, b)
}

// ---------- 镜像 ----------

// findImageByID 跨租户取单条镜像（ListAll filter by id）。
func (h *AdminHandler) findImageByID(ctx context.Context, id string) (Image, error) {
	list, err := h.images.ListAllImages(ctx)
	if err != nil {
		return Image{}, err
	}
	for _, im := range list {
		if im.ID == id {
			return im, nil
		}
	}
	return Image{}, fmt.Errorf("镜像不存在: %s", id)
}

// serveImage 处理 /api/admin/images/{id}。
func (h *AdminHandler) serveImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/admin/images/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 1 || parts[0] == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	id := parts[0]
	im, err := h.findImageByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, im)
}

// ---------- 发布 ----------

// findReleaseByID 跨租户取单条发布（ListAll filter by id）。
func (h *AdminHandler) findReleaseByID(ctx context.Context, id string) (Release, error) {
	list, err := h.release.ListAllReleases(ctx)
	if err != nil {
		return Release{}, err
	}
	for _, r := range list {
		if r.ID == id {
			return r, nil
		}
	}
	return Release{}, fmt.Errorf("发布不存在: %s", id)
}

// serveRelease 处理 /api/admin/releases/{id} 与 /api/admin/releases/{id}/rollback。
func (h *AdminHandler) serveRelease(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/admin/releases/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) < 1 || parts[0] == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	id := parts[0]
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}

	switch action {
	case "":
		if r.Method != http.MethodGet {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		rel, err := h.findReleaseByID(r.Context(), id)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		httputil.WriteData(w, rel)
	case "rollback":
		h.serveRollback(w, r, id)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveRollback 回滚到上一镜像，绕过 prod:write（super_admin 有权干预生产），写操作记审计。
// 以资源租户 ctx 执行 RollbackRelease（其内部 Get/List 强制 ctx tenant）。
func (h *AdminHandler) serveRollback(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rel, err := h.findReleaseByID(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	ctx, rr := adminutil.TenantCtx(r, rel.TenantID)
	rb, err := h.release.RollbackRelease(ctx, id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(rr, rel.TenantID, "admin:rollback", "release", id, fmt.Sprintf("回滚发布 %s", id))
	httputil.WriteData(w, rb)
}
