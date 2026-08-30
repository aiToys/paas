package configcenter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// 粗粒度权限标识（复用 governance 切片已加入 BuiltinRoles 的权限）。
// 配置中心属治理四件套，独立于物理环境，不接入 prod:write。
const (
	PermConfigCenterRead  = "governance:read"
	PermConfigCenterWrite = "governance:write"
)

// AppLookup 按应用名查应用 ID（依赖倒置，避免 configcenter→application import）。
// 实现按 ctx tenant 过滤；跨租户/不存在返 ""（统一 not found 不泄漏）。
type AppLookup interface {
	AppIDByName(ctx context.Context, appName string) (string, error)
}

// Handler 暴露配置中心 REST API。
//
// 路由：
//
//	GET    /api/configcenter/namespaces                  命名空间列表
//	POST   /api/configcenter/namespaces                  创建
//	GET    /api/configcenter/namespaces/{id}             详情
//	DELETE /api/configcenter/namespaces/{id}             删除（级联）
//	GET    /api/configcenter/namespaces/{id}/items       配置项列表（draft）
//	POST   /api/configcenter/namespaces/{id}/items       upsert 配置项
//	DELETE /api/configcenter/namespaces/{id}/items/{iid} 删除配置项
//	POST   /api/configcenter/namespaces/{id}/publish     发布
//	GET    /api/configcenter/namespaces/{id}/publishes   发布历史
//	GET    /api/configcenter/namespaces/{id}/published   客户端发现（active 快照）
//	POST   /api/configcenter/publishes/{pid}/rollback    回滚
type Handler struct {
	repo          Repository
	Authorize     func(r *http.Request, perm string) bool
	serviceLookup ServiceLookup // 可选，CreateNamespace 时校验 ServiceID 归属（防悬挂引用）
	appLookup     AppLookup     // 可选，按应用名发现端点（/api/configcenter/apps/{name}/published）
	Audit         AuditFunc     // 可空：写操作审计（与 AppHandler.AuditFunc 同源桥接）
}

// NewHandler 创建配置中心 handler。
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// WithAudit 注入审计记录器（ns 维度写操作记 configcenter_* 审计）。
func (h *Handler) WithAudit(fn AuditFunc) *Handler { h.Audit = fn; return h }

// WithServiceLookup 注入 governance Service 存在性校验器（依赖倒置）。
// 非空时 CreateNamespace 的 ServiceID 需存在且属本租户，防悬挂引用脏数据。
func (h *Handler) WithServiceLookup(sl ServiceLookup) *Handler {
	h.serviceLookup = sl
	return h
}

// WithAppLookup 注入应用名→ID 解析器（依赖倒置）。
// 非空时启用按应用名发现端点 GET /api/configcenter/apps/{appName}/published。
func (h *Handler) WithAppLookup(al AppLookup) *Handler {
	h.appLookup = al
	return h
}

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// itemBelongsToNS 校验 itemID 归属 namespaceID（防跨 ns 越权删改）。ns 维度与 app 维度
// 两 handler 共用（DRY，单一真源）。
func itemBelongsToNS(ctx context.Context, repo Repository, nsID, itemID string) (bool, error) {
	items, err := repo.ListItems(ctx, nsID)
	if err != nil {
		return false, err
	}
	for _, it := range items {
		if it.ID == itemID {
			return true, nil
		}
	}
	return false, nil
}

// writePublishedJSON 写客户端发现协议响应（{published,version,snapshot[,publishId]} shape，
// 客户端直取非 {data:T}）。withPID=false 用于按应用名发现（客户端不感知 ns publish ID）。
func writePublishedJSON(w http.ResponseWriter, p Publish, withPID bool) {
	body := map[string]interface{}{
		"published": true,
		"version":   p.Version,
		"snapshot":  p.Snapshot,
	}
	if withPID {
		body["publishId"] = p.ID
	}
	httputil.WriteJSON(w, http.StatusOK, body)
}

// listOverridesResolvedRepo 泳道覆盖解析（env 精确 → env=” 回退），包级供两类发现端点复用。
func listOverridesResolvedRepo(ctx context.Context, repo Repository, appID, envID, lane string) ([]LaneOverride, error) {
	ovs, err := repo.ListLaneOverrides(ctx, appID, envID, lane)
	if err != nil {
		return nil, err
	}
	if len(ovs) == 0 && envID != "" {
		return repo.ListLaneOverrides(ctx, appID, "", lane)
	}
	return ovs, nil
}

// writeUnpublishedJSON 写发现端点空态（未发布/未知应用统一此 shape，不泄漏存在性）。
func writeUnpublishedJSON(w http.ResponseWriter) {
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{"published": false})
}

// writeNamespaceErr 按 sentinel 错误统一映射：名字冲突 → 409（引导改名），
// 其余业务错误 → 400，底层技术错误由 WriteServiceError 内部脱敏为 500。
func writeNamespaceErr(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNamespaceNameTaken) || errors.Is(err, ErrNoChanges) {
		httputil.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	httputil.WriteServiceError(w, http.StatusBadRequest, err)
}

// recordAudit 记审计（best-effort：Audit 未注入时跳过；tenantID 空时从 ctx 兜底，
// 仍空由桥接层归 platform）。
func (h *Handler) recordAudit(r *http.Request, tenantID, action, resourceID, detail string) {
	if h.Audit == nil {
		return
	}
	if tenantID == "" {
		tenantID, _ = tenant.IDOrErr(r.Context())
	}
	h.Audit(r.Context(), tenantID, action, resourceID, detail)
}

// ServeHTTP 按路径分发。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/configcenter/namespaces":
		h.serveNamespaceCollection(w, r)
	case strings.HasPrefix(path, "/api/configcenter/namespaces/"):
		h.serveNamespaceItem(w, r)
	case strings.HasPrefix(path, "/api/configcenter/publishes/"):
		h.servePublishAction(w, r)
	case strings.HasPrefix(path, "/api/configcenter/apps/"):
		h.serveAppPublished(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

// serveNamespaceCollection GET 列表 / POST 创建。
func (h *Handler) serveNamespaceCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermConfigCenterRead) {
			return
		}
		list, err := h.repo.ListNamespaces(r.Context(), r.URL.Query().Get("serviceId"))
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermConfigCenterWrite) {
			return
		}
		var n Namespace
		if err := json.NewDecoder(r.Body).Decode(&n); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		// ServiceID 非空时校验关联服务存在 + 属本租户，防悬挂引用（typo/已删/跨租户脏数据）。
		if n.ServiceID != "" && h.serviceLookup != nil {
			ok, lerr := h.serviceLookup.ServiceExists(r.Context(), n.ServiceID)
			if lerr != nil {
				httputil.WriteInternalError(w, lerr)
				return
			}
			if !ok {
				httputil.WriteError(w, http.StatusBadRequest, "关联服务不存在: "+n.ServiceID)
				return
			}
		}
		saved, err := h.repo.CreateNamespace(r.Context(), n)
		if err != nil {
			writeNamespaceErr(w, err)
			return
		}
		h.recordAudit(r, saved.TenantID, "configcenter_ns_create", saved.ID, "name="+saved.Name)
		httputil.WriteDataCreated(w, saved)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// appScopeWriteDenied 校验 ns 维度写操作目标不是应用派生命名空间（scope=app）。
// 应用派生 ns 归应用权限域（application:write + AppGuard），ns 维度写入会绕过该护栏，
// 故写操作（删 ns/建改 item/publish）命中 scope=app 一律 403；ns 不存在保持 404 不泄漏。
// 读操作（GET）放行（应用详情页与共享视图均可读）。
func (h *Handler) appScopeWriteDenied(w http.ResponseWriter, r *http.Request, nsID string) bool {
	n, err := h.repo.GetNamespace(r.Context(), nsID)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return true
	}
	if n.Scope == ScopeApp {
		httputil.WriteError(w, http.StatusForbidden, "应用派生配置请经应用详情操作")
		return true
	}
	return false
}

// serveNamespaceItem 处理 namespaces/{id}[/{sub}[/{iid}]]。
func (h *Handler) serveNamespaceItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/configcenter/namespaces/")
	rest = strings.TrimRight(rest, "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	nsID := parts[0]
	switch len(parts) {
	case 1:
		h.serveNamespaceGetDelete(w, r, nsID)
	case 2:
		switch parts[1] {
		case "items":
			h.serveItemCollection(w, r, nsID)
		case "publish":
			h.servePublish(w, r, nsID)
		case "publishes":
			h.servePublishHistory(w, r, nsID)
		case "published":
			h.servePublished(w, r, nsID)
		case "ref-users":
			h.serveRefUsers(w, r, nsID)
		default:
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
	case 3:
		if parts[1] == "items" {
			h.serveItemDelete(w, r, nsID, parts[2])
		} else {
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveNamespaceGetDelete(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermConfigCenterRead) {
			return
		}
		n, err := h.repo.GetNamespace(r.Context(), nsID)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		httputil.WriteData(w, n)
		return
	}
	if r.Method == http.MethodDelete {
		if !h.allow(w, r, PermConfigCenterWrite) {
			return
		}
		if h.appScopeWriteDenied(w, r, nsID) {
			return
		}
		if err := h.repo.DeleteNamespace(r.Context(), nsID); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		h.recordAudit(r, "", "configcenter_ns_delete", nsID, "")
		httputil.WriteData(w, map[string]string{"deleted": nsID})
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// serveItemCollection GET items / POST upsert item。
func (h *Handler) serveItemCollection(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermConfigCenterRead) {
			return
		}
		list, err := h.repo.ListItems(r.Context(), nsID)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermConfigCenterWrite) {
			return
		}
		if h.appScopeWriteDenied(w, r, nsID) {
			return
		}
		var item ConfigItem
		if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		item.NamespaceID = nsID
		saved, err := h.repo.UpsertItem(r.Context(), item)
		if err != nil {
			writeNamespaceErr(w, err)
			return
		}
		h.recordAudit(r, "", "configcenter_item_upsert", nsID, "item="+saved.ID+",key="+saved.Key)
		httputil.WriteDataCreated(w, saved)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) serveItemDelete(w http.ResponseWriter, r *http.Request, nsID, itemID string) {
	if r.Method != http.MethodDelete {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterWrite) {
		return
	}
	if h.appScopeWriteDenied(w, r, nsID) {
		return
	}
	// 校验 item 归属该 namespace，防止 DELETE /nsA/items/{item-of-nsB} 跨 ns 越权删除。
	belongs, err := itemBelongsToNS(r.Context(), h.repo, nsID, itemID)
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
	h.recordAudit(r, "", "configcenter_item_delete", nsID, "item="+itemID)
	httputil.WriteData(w, map[string]string{"deleted": itemID})
}

// servePublish POST 发布（生成版本快照）。
func (h *Handler) servePublish(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterWrite) {
		return
	}
	if h.appScopeWriteDenied(w, r, nsID) {
		return
	}
	pub, err := h.repo.CreatePublish(r.Context(), nsID)
	if err != nil {
		writeNamespaceErr(w, err)
		return
	}
	h.recordAudit(r, "", "configcenter_publish", nsID, fmt.Sprintf("version=%d,publishId=%s", pub.Version, pub.ID))
	httputil.WriteDataCreated(w, pub)
}

// servePublishHistory GET 发布历史。
func (h *Handler) servePublishHistory(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterRead) {
		return
	}
	list, err := h.repo.ListPublishes(r.Context(), nsID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, list)
}

// servePublished GET 客户端发现（active 快照 + version）。
func (h *Handler) servePublished(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterRead) {
		return
	}
	active, ok, err := h.repo.ActivePublish(r.Context(), nsID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	if !ok {
		writeUnpublishedJSON(w)
		return
	}
	// 发现协议：保持 {published,version,snapshot,publishId} shape（前端 published.value = await json() 直取），
	// 仅经 httputil 统一编码（Content-Type 显式）。非标准 {data:T}，因属数据面客户端发现契约。
	writePublishedJSON(w, active, true)
}

// serveAppPublished GET /api/configcenter/apps/{appName}/published 按应用名发现（active 快照）。
// 与 ns 维度发现同契约（{published,version,snapshot}），不含 publishId——应用维度以应用为锚，
// 客户端无需感知派生 ns 的 publish 记录 ID。未知应用名/未发布统一 {"published":false} 不泄漏。
func (h *Handler) serveAppPublished(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterRead) {
		return
	}
	if h.appLookup == nil {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	// 路径形态 apps/{appName}/published：首段=应用名（r.URL.Path 已解码，勿再 PathUnescape 双重解码），
	// 末段必须为 published。
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/configcenter/apps/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != "published" || parts[0] == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	appName := parts[0]
	appID, err := h.appLookup.AppIDByName(r.Context(), appName)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	if appID == "" {
		writeUnpublishedJSON(w)
		return
	}
	// env 解析：env 精确 → env='' 回退（与 FindAppNamespaceEnv 同规则；query 名为 env）。
	envID := r.URL.Query().Get("env")
	ns, ok, err := h.repo.FindAppNamespaceEnv(r.Context(), appID, envID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	if !ok {
		writeUnpublishedJSON(w)
		return
	}
	active, ok, err := h.repo.ActivePublish(r.Context(), ns.ID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	if !ok {
		writeUnpublishedJSON(w)
		return
	}
	// 发现协议：与 ns 维度 servePublished 同款 {published,version,snapshot} shape（客户端直取）。
	// 三层 merge 与应用维度端点同款语义：shared 引用 → 基线 → lane 覆盖（此端点是
	// 数据面客户端主通道——chatbot dynconfig 按应用名发现，漏 shared 层则共享配置到不了数据面）。
	shared, err := sharedLayersRepo(r.Context(), h.repo, ns.ID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	var ovs []LaneOverride
	if lane := r.URL.Query().Get("lane"); lane != "" && lane != "default" {
		ovs, err = listOverridesResolvedRepo(r.Context(), h.repo, appID, envID, lane)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
	}
	body := map[string]interface{}{
		"published": true,
		"version":   active.Version,
		"snapshot":  MergeSnapshot3(shared, active.Snapshot, ovs),
	}
	if len(ovs) > 0 {
		body["overrideHash"] = OverrideHash(ovs)
	}
	if len(shared) > 0 {
		body["sharedHash"] = SharedHash(shared)
	}
	httputil.WriteJSON(w, http.StatusOK, body)
}

// servePublishAction POST /api/configcenter/publishes/{pid}/rollback 回滚。
func (h *Handler) servePublishAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterWrite) {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/configcenter/publishes/")
	parts := strings.Split(strings.TrimRight(rest, "/"), "/")
	if len(parts) != 2 || parts[1] != "rollback" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	pid := parts[0]
	// 应用派生 ns 的回滚归应用权限域：先经 publish 反查 ns，scope=app 拒绝（防绕过 AppGuard）。
	nsID, err := h.repo.PublishNamespaceID(r.Context(), pid)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if h.appScopeWriteDenied(w, r, nsID) {
		return
	}
	rb, err := h.repo.RollbackPublish(r.Context(), pid)
	if err != nil {
		if errors.Is(err, ErrPublishAlreadyActive) {
			httputil.WriteError(w, http.StatusConflict, err.Error())
			return
		}
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(r, "", "configcenter_rollback", nsID, fmt.Sprintf("version=%d,publishId=%s", rb.Version, rb.ID))
	httputil.WriteData(w, rb)
}

// serveRefUsers GET /namespaces/{id}/ref-users 影响面反查：该 shared ns 被哪些
// 应用派生 ns 引用（发布时展示影响面）。富化 app ns 名（前端解析应用/env 归属）。
func (h *Handler) serveRefUsers(w http.ResponseWriter, r *http.Request, nsID string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermConfigCenterRead) {
		return
	}
	refs, err := h.repo.ListNSRefUsers(r.Context(), nsID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	type refUserView struct {
		NSRef
		AppNSName string `json:"appNsName,omitempty"` // 引用方派生 ns 名（app-<id>[-<env>]）
	}
	out := make([]refUserView, 0, len(refs))
	for _, ref := range refs {
		v := refUserView{NSRef: ref}
		if ns, err := h.repo.GetNamespace(r.Context(), ref.AppNSID); err == nil {
			v.AppNSName = ns.Name
		}
		out = append(out, v)
	}
	httputil.WriteData(w, out)
}
