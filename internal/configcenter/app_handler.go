package configcenter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
)

// AppHandler 应用维度动态配置 handler（scope=app 主路径）。
//
// 路由（挂 application composite 的 dynamic-configs 分发）：
//
//	GET    /api/applications/{id}/dynamic-configs            列 draft 项（自动 EnsureByApp）
//	POST   /api/applications/{id}/dynamic-configs            upsert 项
//	DELETE /api/applications/{id}/dynamic-configs/{itemId}   删项
//	POST   /api/applications/{id}/dynamic-configs/publish    发布
//	GET    /api/applications/{id}/dynamic-configs/publishes  发布历史
//	GET    /api/applications/{id}/dynamic-configs/published  当前生效
//	POST   /api/applications/{id}/dynamic-configs/rollback/{pid}  回滚（校验 pid 属本应用派生 ns）
//
// 权限 application:read/write（应用资产归应用权限域）；受限应用写需 AppGuard write 动作。
type AppHandler struct {
	repo      Repository
	Authorize func(r *http.Request, perm string) bool
	Guard     GuardAdapter // 可空：受限应用 enforcement
	Audit     AuditFunc    // 可空：publish 审计
}

// GuardAdapter 应用级权限判定（依赖倒置，避免 configcenter→application import）。
type GuardAdapter interface {
	Allow(r *http.Request, appID, action string) bool
}

// AuditFunc 审计记录（依赖倒置）。参数：ctx, tenantID, action, resourceID, detail。
type AuditFunc func(ctx context.Context, tenantID, action, resourceID, detail string)

// NewAppHandler 创建应用维度动态配置 handler。
func NewAppHandler(repo Repository) *AppHandler {
	return &AppHandler{repo: repo}
}

// WithGuard 注入应用级权限判定器（受限应用 enforcement）。
func (h *AppHandler) WithGuard(g GuardAdapter) *AppHandler { h.Guard = g; return h }

// WithAudit 注入审计记录器（publish 记 configcenter_publish）。
func (h *AppHandler) WithAudit(fn AuditFunc) *AppHandler { h.Audit = fn; return h }

func (h *AppHandler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// allowWrite 写权限：application:write + 受限应用 AppGuard write 动作（渐进启用，Guard 可空）。
func (h *AppHandler) allowWrite(w http.ResponseWriter, r *http.Request, appID string) bool {
	if !h.allow(w, r, "application:write") {
		return false
	}
	if h.Guard != nil && !h.Guard.Allow(r, appID, "write") {
		httputil.WriteError(w, http.StatusForbidden, "无该应用的应用级权限（write）")
		return false
	}
	return true
}

// ServeHTTP 处理 /api/applications/{id}/dynamic-configs[...]（路径前缀匹配后按剩余段分发）。
func (h *AppHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/applications/")
	rest = strings.TrimRight(rest, "/")
	parts := strings.Split(rest, "/")
	// parts[0]=appID, parts[1]=dynamic-configs（composite 保证），剩余段为子操作
	if len(parts) < 2 || parts[0] == "" || parts[1] != "dynamic-configs" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	appID := parts[0]
	sub := parts[2:]
	switch {
	case len(sub) == 0:
		h.serveCollection(w, r, appID)
	case len(sub) == 1 && sub[0] == "publish":
		h.servePublish(w, r, appID)
	case len(sub) == 1 && sub[0] == "publishes":
		h.servePublishHistory(w, r, appID)
	case len(sub) == 1 && sub[0] == "published":
		h.servePublished(w, r, appID)
	case len(sub) == 2 && sub[0] == "items":
		h.serveItemDelete(w, r, appID, sub[1])
	case len(sub) == 2 && sub[0] == "rollback":
		h.serveRollback(w, r, appID, sub[1])
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveCollection GET 列 draft 项（只读，FindAppNamespace 不懒建，无 ns 返空列表）/
// POST upsert 项（写路径才 EnsureByApp 懒建）。
func (h *AppHandler) serveCollection(w http.ResponseWriter, r *http.Request, appID string) {
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, "application:read") {
			return
		}
	case http.MethodPost:
		if !h.allowWrite(w, r, appID) {
			return
		}
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if r.Method == http.MethodGet {
		ns, ok, err := h.repo.FindAppNamespace(r.Context(), appID)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		if !ok {
			httputil.WriteData(w, []ConfigItem{})
			return
		}
		list, err := h.repo.ListItems(r.Context(), ns.ID)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}
	ns, err := h.ensureAppNS(w, r, appID)
	if err != nil {
		return
	}
	var item ConfigItem
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	item.NamespaceID = ns.ID
	saved, err := h.repo.UpsertItem(r.Context(), item)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(r.Context(), ns.TenantID, "configcenter_item_upsert", appID, "item="+saved.ID+",key="+saved.Key)
	httputil.WriteDataCreated(w, saved)
}

// ensureAppNS 写路径懒建应用派生 ns（EnsureByApp）。名字冲突（手工共享 ns 占了 app-<id> 名，
// sentinel ErrNamespaceNameTaken）映射 409 引导改名；其余错误 500 脱敏。
// 失败时响应已写出，返回零值 + non-nil err 终止调用方。
func (h *AppHandler) ensureAppNS(w http.ResponseWriter, r *http.Request, appID string) (Namespace, error) {
	ns, err := h.repo.EnsureByApp(r.Context(), appID)
	if err == nil {
		return ns, nil
	}
	if errors.Is(err, ErrNamespaceNameTaken) {
		httputil.WriteError(w, http.StatusConflict,
			fmt.Sprintf("命名空间名被占用：app-%s（手工共享命名空间占用，请改名）", appID))
		return Namespace{}, err
	}
	httputil.WriteInternalError(w, err)
	return Namespace{}, err
}

// findAppNS 只读路径查应用派生 ns（不懒建）。无 ns 返 404（rollback/itemDelete 等需既有
// 资源的操作不再凭空建 ns）。失败时响应已写出。
func (h *AppHandler) findAppNS(w http.ResponseWriter, r *http.Request, appID string) (Namespace, bool) {
	ns, ok, err := h.repo.FindAppNamespace(r.Context(), appID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return Namespace{}, false
	}
	if !ok {
		httputil.WriteError(w, http.StatusNotFound, "应用动态配置不存在")
		return Namespace{}, false
	}
	return ns, true
}

// serveItemDelete DELETE /dynamic-configs/items/{itemId}（校验 item 归属该应用 ns，防跨 ns 越权删；
// 不懒建——无 ns 即无 item，404）。
func (h *AppHandler) serveItemDelete(w http.ResponseWriter, r *http.Request, appID, itemID string) {
	if r.Method != http.MethodDelete {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allowWrite(w, r, appID) {
		return
	}
	ns, ok := h.findAppNS(w, r, appID)
	if !ok {
		return
	}
	belongs, err := itemBelongsToNS(r.Context(), h.repo, ns.ID, itemID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	if !belongs {
		httputil.WriteError(w, http.StatusNotFound, "配置项不存在: "+itemID)
		return
	}
	if err := h.repo.DeleteItem(r.Context(), itemID); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	h.recordAudit(r.Context(), ns.TenantID, "configcenter_item_delete", appID, "item="+itemID)
	httputil.WriteData(w, map[string]string{"deleted": itemID})
}

// servePublish POST 发布（EnsureByApp + CreatePublish 快照 + 审计）。
func (h *AppHandler) servePublish(w http.ResponseWriter, r *http.Request, appID string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allowWrite(w, r, appID) {
		return
	}
	ns, err := h.ensureAppNS(w, r, appID)
	if err != nil {
		return
	}
	pub, err := h.repo.CreatePublish(r.Context(), ns.ID)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(r.Context(), ns.TenantID, "configcenter_publish", appID, fmt.Sprintf("version=%d,publishId=%s", pub.Version, pub.ID))
	httputil.WriteDataCreated(w, pub)
}

// servePublishHistory GET 发布历史（只读不懒建，无 ns 返空列表）。
func (h *AppHandler) servePublishHistory(w http.ResponseWriter, r *http.Request, appID string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, "application:read") {
		return
	}
	ns, ok, err := h.repo.FindAppNamespace(r.Context(), appID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	if !ok {
		httputil.WriteData(w, []Publish{})
		return
	}
	list, err := h.repo.ListPublishes(r.Context(), ns.ID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, list)
}

// servePublished GET 当前生效（只读不懒建，无 ns/无 active 返 {"published":false}）。
// 发现协议 shape 与 ns 维度端点一致（{published,version,snapshot,publishId}）。
func (h *AppHandler) servePublished(w http.ResponseWriter, r *http.Request, appID string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, "application:read") {
		return
	}
	ns, ok, err := h.repo.FindAppNamespace(r.Context(), appID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	if !ok {
		httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{"published": false})
		return
	}
	active, ok, err := h.repo.ActivePublish(r.Context(), ns.ID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	if !ok {
		httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{"published": false})
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{
		"published": true,
		"version":   active.Version,
		"snapshot":  active.Snapshot,
		"publishId": active.ID,
	})
}

// serveRollback POST /dynamic-configs/rollback/{pid} 应用维度回滚。
// 权限域与 publish 对称（application:write + AppGuard write）；经 PublishNamespaceID 校验
// pid 所属 ns 是本应用派生 ns（防跨应用回滚他人发布）；成功记审计 configcenter_rollback。
func (h *AppHandler) serveRollback(w http.ResponseWriter, r *http.Request, appID, publishID string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allowWrite(w, r, appID) {
		return
	}
	// 回滚不懒建：无派生 ns 即无发布历史，404（防只读意图产生写副作用）。
	ns, ok := h.findAppNS(w, r, appID)
	if !ok {
		return
	}
	pubNSID, err := h.repo.PublishNamespaceID(r.Context(), publishID)
	if err != nil {
		// 跨应用/跨租户/不存在的 pid 统一 404 不泄漏存在性。
		httputil.WriteError(w, http.StatusNotFound, "发布不存在: "+publishID)
		return
	}
	if pubNSID != ns.ID {
		httputil.WriteError(w, http.StatusNotFound, "发布不存在: "+publishID)
		return
	}
	rb, err := h.repo.RollbackPublish(r.Context(), publishID)
	if err != nil {
		if errors.Is(err, ErrPublishAlreadyActive) {
			httputil.WriteError(w, http.StatusConflict, err.Error())
			return
		}
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(r.Context(), ns.TenantID, "configcenter_rollback", appID, fmt.Sprintf("version=%d,publishId=%s", rb.Version, rb.ID))
	httputil.WriteData(w, rb)
}

// recordAudit 记审计（best-effort，失败仅日志不阻断主流程）。
// actor 由 cmd/core 桥接时经 gateway.UserIDFrom(ctx) 取（AuditFunc 只收基本类型，handler 不感知身份）。
func (h *AppHandler) recordAudit(ctx context.Context, tenantID, action, resourceID, detail string) {
	if h.Audit == nil {
		return
	}
	h.Audit(ctx, tenantID, action, resourceID, detail)
}
