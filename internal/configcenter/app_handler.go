package configcenter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/environment"
	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// EnvTypeResolver 解析环境类型（prod|test），用于生产写闸门（依赖倒置，同 appconfig 模式）。
type EnvTypeResolver = environment.EnvTypeResolver

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
	repo        Repository
	envResolver EnvTypeResolver // 可空：生产写闸门（目标 env type=prod 需 prod:write）
	Authorize   func(r *http.Request, perm string) bool
	Guard       GuardAdapter // 可空：受限应用 enforcement
	Audit       AuditFunc    // 可空：publish 审计
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

// WithEnvResolver 注入环境类型解析器，启用生产写闸门（prod:write）。
func (h *AppHandler) WithEnvResolver(r EnvTypeResolver) *AppHandler { h.envResolver = r; return h }

// prodGateRequired 判定目标环境是否需生产闸门（只判定不写响应）。
// 未注入 resolver/envID 空时跳过；fail-closed：环境查不到（不存在/跨租户）保守按生产。
func (h *AppHandler) prodGateRequired(r *http.Request, envID string) bool {
	if h.envResolver == nil || envID == "" {
		return false
	}
	etype, err := h.envResolver.EnvType(r.Context(), envID)
	return err != nil || etype == environment.TypeProd
}

func (h *AppHandler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// allowWrite 写权限：application:write + 受限应用 AppGuard write 动作（渐进启用，Guard 可空）
// + 生产闸门（envID 非空且 type=prod 需 prod:write）。
func (h *AppHandler) allowWrite(w http.ResponseWriter, r *http.Request, appID, envID string) bool {
	if !h.allow(w, r, "application:write") {
		return false
	}
	if h.prodGateRequired(r, envID) && !h.allow(w, r, environment.PermProdWrite) {
		return false // 403 已由 allow 写出（单一出口，F2 修复双重写响应）
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
		h.servePublish(w, r, appID, r.URL.Query().Get("envId"))
	case len(sub) == 1 && sub[0] == "publishes":
		h.servePublishHistory(w, r, appID, r.URL.Query().Get("envId"))
	case len(sub) == 1 && sub[0] == "published":
		// envId 为主（与 dynamic-configs 全家一致）；env 为兼容别名（发现协议习惯，防调用方踩坑）。
		envQ := r.URL.Query().Get("envId")
		if envQ == "" {
			envQ = r.URL.Query().Get("env")
		}
		h.servePublished(w, r, appID, envQ, r.URL.Query().Get("lane"))
	case len(sub) == 2 && sub[0] == "items":
		h.serveItemDelete(w, r, appID, r.URL.Query().Get("envId"), sub[1])
	case len(sub) == 2 && sub[0] == "rollback":
		h.serveRollback(w, r, appID, r.URL.Query().Get("envId"), sub[1])
	case len(sub) == 2 && sub[0] == "lane-overrides" && sub[1] == "promote":
		// 精确匹配先于通配 lane-overrides/{key} DELETE（promote 是保留字，key 不会叫 promote——
		// DELETE /lane-overrides/promote 会被此分支吃掉返 405，可接受：key 命名空间保留字）。
		h.serveLanePromote(w, r, appID, r.URL.Query().Get("envId"), r.URL.Query().Get("lane"))
	case sub[0] == "lane-overrides":
		h.serveLaneOverrides(w, r, appID, r.URL.Query().Get("envId"), r.URL.Query().Get("lane"), parts[3:])
	case len(sub) == 1 && sub[0] == "shared-refs":
		h.serveSharedRefs(w, r, appID, r.URL.Query().Get("envId"))
	case len(sub) == 2 && sub[0] == "shared-refs":
		h.serveSharedRefDelete(w, r, appID, r.URL.Query().Get("envId"), sub[1])
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
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	envID := r.URL.Query().Get("envId")
	if r.Method == http.MethodGet {
		// 读路径回退语义：env 精确未命中回退 env='' 基线（FindAppNamespaceEnv）。
		ns, ok, err := h.repo.FindAppNamespaceEnv(r.Context(), appID, envID)
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
	if !h.allowWrite(w, r, appID, envID) {
		return
	}
	ns, err := h.ensureAppNS(w, r, appID, envID)
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
	h.recordAudit(r.Context(), ns.TenantID, "configcenter_item_upsert", appID, "item="+saved.ID+",key="+saved.Key+",envId="+envID)
	httputil.WriteDataCreated(w, saved)
}

// ensureAppNS 写路径懒建应用派生 ns（EnsureByApp）。名字冲突（手工共享 ns 占了 app-<id> 名，
// sentinel ErrNamespaceNameTaken）映射 409 引导改名；其余错误 500 脱敏。
// 失败时响应已写出，返回零值 + non-nil err 终止调用方。
func (h *AppHandler) ensureAppNS(w http.ResponseWriter, r *http.Request, appID, envID string) (Namespace, error) {
	ns, err := h.repo.EnsureByAppEnv(r.Context(), appID, envID)
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
func (h *AppHandler) findAppNS(w http.ResponseWriter, r *http.Request, appID, envID string) (Namespace, bool) {
	ns, ok, err := h.repo.FindAppNamespaceEnv(r.Context(), appID, envID)
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
func (h *AppHandler) serveItemDelete(w http.ResponseWriter, r *http.Request, appID, envID, itemID string) {
	if r.Method != http.MethodDelete {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allowWrite(w, r, appID, envID) {
		return
	}
	ns, ok := h.findAppNS(w, r, appID, envID)
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
func (h *AppHandler) servePublish(w http.ResponseWriter, r *http.Request, appID, envID string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allowWrite(w, r, appID, envID) {
		return
	}
	ns, err := h.ensureAppNS(w, r, appID, envID)
	if err != nil {
		return
	}
	pub, err := h.repo.CreatePublish(r.Context(), ns.ID)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(r.Context(), ns.TenantID, "configcenter_publish", appID, fmt.Sprintf("version=%d,publishId=%s,envId=%s", pub.Version, pub.ID, envID))
	httputil.WriteDataCreated(w, pub)
}

// servePublishHistory GET 发布历史（只读不懒建，无 ns 返空列表）。
func (h *AppHandler) servePublishHistory(w http.ResponseWriter, r *http.Request, appID, envID string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, "application:read") {
		return
	}
	// 回退语义：env 精确未命中回退 env='' 基线（历史发现兼容）。
	ns, ok, err := h.repo.FindAppNamespaceEnv(r.Context(), appID, envID)
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
// 发现解析：env 精确 → env='' 回退 → 无（store 层 FindAppNamespaceEnv）；lane 同规则取覆盖，
// 服务端三层 merge（shared 引用 → 基线快照 → 泳道覆盖），有覆盖/引用时附 overrideHash/sharedHash
// 指纹（无则省略，向后兼容）。发现协议 shape：{published,version,snapshot,publishId[,overrideHash][,sharedHash]}。
func (h *AppHandler) servePublished(w http.ResponseWriter, r *http.Request, appID, envID, lane string) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, "application:read") {
		return
	}
	ns, ok, err := h.repo.FindAppNamespaceEnv(r.Context(), appID, envID)
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
	// shared 引用层（引用挂在 ns 上，env 隔离天然生效；shared 未发布贡献空集）。
	shared, err := h.sharedLayers(r.Context(), ns.ID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	// lane 非空才取覆盖（同回退规则）。
	var ovs []LaneOverride
	if lane != "" {
		ovs, err = h.listOverridesResolved(r.Context(), appID, envID, lane)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
	}
	snapshot := MergeSnapshot3(shared, active.Snapshot, ovs)
	resp := map[string]interface{}{
		"published": true,
		"version":   active.Version, // 应用自身版本（shared 变更不污染，指纹独立）
		"snapshot":  snapshot,
		"publishId": active.ID,
	}
	if len(ovs) > 0 {
		resp["overrideHash"] = OverrideHash(ovs)
	}
	if len(shared) > 0 {
		resp["sharedHash"] = SharedHash(shared)
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// sharedLayers 解析 ns 的 shared 引用层（引用顺序 = merge 铺垫顺序）。
// shared ns 已删（引用悬挂）静默跳过——引用是弱关联，删 shared 不应打断应用发现。
func (h *AppHandler) sharedLayers(ctx context.Context, appNSID string) ([]SharedLayer, error) {
	refs, err := h.repo.ListNSRefs(ctx, appNSID)
	if err != nil {
		return nil, err
	}
	out := make([]SharedLayer, 0, len(refs))
	for _, ref := range refs {
		shared, err := h.repo.GetNamespace(ctx, ref.SharedNSID)
		if err != nil {
			continue // 已删/跨租户（GetNamespace 租户过滤）悬挂引用跳过
		}
		layer := SharedLayer{NSID: shared.ID}
		if pub, ok, err := h.repo.ActivePublish(ctx, shared.ID); err == nil && ok {
			layer.Version = pub.Version
			layer.Snapshot = pub.Snapshot
		}
		out = append(out, layer)
	}
	return out, nil
}

// listOverridesResolved 泳道覆盖解析：env 精确 → env='' 回退（与 ns 发现同规则）。
func (h *AppHandler) listOverridesResolved(ctx context.Context, appID, envID, lane string) ([]LaneOverride, error) {
	return listOverridesResolvedRepo(ctx, h.repo, appID, envID, lane)
}

// serveRollback POST /dynamic-configs/rollback/{pid} 应用维度回滚。
// 权限域与 publish 对称（application:write + AppGuard write）；经 PublishNamespaceID 校验
// pid 所属 ns 是本应用派生 ns（防跨应用回滚他人发布）；成功记审计 configcenter_rollback。
func (h *AppHandler) serveRollback(w http.ResponseWriter, r *http.Request, appID, envID, publishID string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allowWrite(w, r, appID, envID) {
		return
	}
	// 回滚不懒建：无派生 ns 即无发布历史，404（防只读意图产生写副作用）。
	ns, ok := h.findAppNS(w, r, appID, envID)
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
	// 回滚同步重置草稿为目标版本快照：draft 与 active 指针同时归位，
	// 避免回滚后 draft（仍是旧版本发布时的值）与生效值错位——前端 diff 显示
	// 「有待发布变更」的假差异（用户并未编辑任何东西）。失败语义：回滚已生效，
	// 草稿重置失败仅日志提示可手动编辑对齐（不做事务回滚——active 指针切换是主操作）。
	if err := h.resetItemsToSnapshot(r.Context(), ns.ID, rb.Snapshot); err != nil {
		log.Printf("configcenter rollback %s: reset draft items failed: %v", rb.ID, err)
	}
	h.recordAudit(r.Context(), ns.TenantID, "configcenter_rollback", appID, fmt.Sprintf("version=%d,publishId=%s,envId=%s", rb.Version, rb.ID, envID))
	httputil.WriteData(w, rb)
}

// resetItemsToSnapshot 把 ns 的 draft items 逐 key 对齐到快照（多的删、缺的补、不同的改）。
func (h *AppHandler) resetItemsToSnapshot(ctx context.Context, namespaceID string, snapshot map[string]string) error {
	existing, err := h.repo.ListItems(ctx, namespaceID)
	if err != nil {
		return err
	}
	existingByKey := make(map[string]ConfigItem, len(existing))
	for _, it := range existing {
		existingByKey[it.Key] = it
	}
	// 快照中的 key：补缺 + 改异（type 保留 draft 原值，快照不存 type）
	for key, val := range snapshot {
		if cur, ok := existingByKey[key]; ok {
			if cur.Value == val {
				continue
			}
			cur.Value = val
			if _, err := h.repo.UpsertItem(ctx, cur); err != nil {
				return err
			}
			continue
		}
		if _, err := h.repo.UpsertItem(ctx, ConfigItem{NamespaceID: namespaceID, Key: key, Value: val}); err != nil {
			return err
		}
	}
	// draft 有但快照没有的 key：删（回滚目标版本里不存在）
	for key, it := range existingByKey {
		if _, ok := snapshot[key]; !ok {
			if err := h.repo.DeleteItem(ctx, it.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// serveLaneOverrides 泳道覆盖三操作（挂 dynamic-configs/lane-overrides）：
//
//	GET    /dynamic-configs/lane-overrides?envId=&lane=  列覆盖（lane 必填，default 拒绝）
//	POST   /dynamic-configs/lane-overrides?envId=&lane=  upsert 覆盖（即时生效，无版本链）
//	DELETE /dynamic-configs/lane-overrides/{key}?envId=&lane=  删覆盖
//
// 写操作需 application:write + 生产闸门 + AppGuard write；全记审计（configcenter_lane_*）。
func (h *AppHandler) serveLaneOverrides(w http.ResponseWriter, r *http.Request, appID, envID, lane string, rest []string) {
	// lane="default" 即基线（无覆盖语义），入口统一拒 400 防客户端误传静默当基线。
	if lane == "" {
		httputil.WriteError(w, http.StatusBadRequest, "lane 参数必填")
		return
	}
	if lane == "default" {
		httputil.WriteError(w, http.StatusBadRequest, "lane=default 即基线，不接受覆盖")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, "application:read") {
			return
		}
		ovs, err := h.listOverridesResolved(r.Context(), appID, envID, lane)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, ovs)
	case http.MethodPost:
		if !h.allowWrite(w, r, appID, envID) {
			return
		}
		var o LaneOverride
		if err := json.NewDecoder(r.Body).Decode(&o); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		o.AppID = appID
		o.EnvID = envID
		o.LaneID = lane
		saved, err := h.repo.UpsertLaneOverride(r.Context(), o)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		h.recordAudit(r.Context(), saved.TenantID, "configcenter_lane_override_upsert", appID,
			"key="+saved.Key+",lane="+lane+",envId="+envID)
		httputil.WriteDataCreated(w, saved)
	case http.MethodDelete:
		if len(rest) != 1 || rest[0] == "" {
			httputil.WriteError(w, http.StatusNotFound, "not found")
			return
		}
		if !h.allowWrite(w, r, appID, envID) {
			return
		}
		if err := h.repo.DeleteLaneOverride(r.Context(), appID, envID, lane, rest[0]); err != nil {
			if errors.Is(err, ErrLaneOverrideNotFound) {
				httputil.WriteError(w, http.StatusNotFound, err.Error())
				return
			}
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		h.recordAudit(r.Context(), tenantIDFromCtx(r.Context()), "configcenter_lane_override_delete", appID,
			"key="+rest[0]+",lane="+lane+",envId="+envID)
		httputil.WriteData(w, map[string]string{"deleted": rest[0]})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// serveSharedRefs 共享配置引用两操作（挂 dynamic-configs/shared-refs）：
//
//	GET    /dynamic-configs/shared-refs?envId=  列引用（富化：shared ns 名/active 版本/key 数）
//	POST   /dynamic-configs/shared-refs?envId=  body {sharedNsId} 建引用
//
// 引用挂在 (app, env) 派生 ns 上（各 env 独立引用，隔离天然生效）。
// 写需 application:write + 生产闸门 + AppGuard write；记审计 configcenter_ns_ref_*。
func (h *AppHandler) serveSharedRefs(w http.ResponseWriter, r *http.Request, appID, envID string) {
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, "application:read") {
			return
		}
		ns, ok := h.findAppNS(w, r, appID, envID)
		if !ok {
			httputil.WriteData(w, []sharedRefView{})
			return
		}
		refs, err := h.repo.ListNSRefs(r.Context(), ns.ID)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		out := make([]sharedRefView, 0, len(refs))
		for _, ref := range refs {
			v := sharedRefView{NSRef: ref}
			if shared, err := h.repo.GetNamespace(r.Context(), ref.SharedNSID); err == nil {
				v.SharedName = shared.Name
				if pub, ok, err := h.repo.ActivePublish(r.Context(), shared.ID); err == nil && ok {
					v.SharedVersion = pub.Version
					v.SharedKeys = len(pub.Snapshot)
				}
			}
			out = append(out, v)
		}
		httputil.WriteData(w, out)
	case http.MethodPost:
		if !h.allowWrite(w, r, appID, envID) {
			return
		}
		ns, err := h.ensureAppNS(w, r, appID, envID)
		if err != nil {
			return
		}
		var body struct{ SharedNSID string `json:"sharedNsId"` }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SharedNSID == "" {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body: sharedNsId 必填")
			return
		}
		ref, err := h.repo.AddNSRef(r.Context(), ns.ID, body.SharedNSID)
		if err != nil {
			h.writeRefErr(w, err)
			return
		}
		h.recordAudit(r.Context(), ns.TenantID, "configcenter_ns_ref_add", appID, "ref="+ref.ID+",sharedNs="+body.SharedNSID+",envId="+envID)
		httputil.WriteDataCreated(w, ref)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// serveSharedRefDelete DELETE /dynamic-configs/shared-refs/{refId}?envId= 解除引用。
func (h *AppHandler) serveSharedRefDelete(w http.ResponseWriter, r *http.Request, appID, envID, refID string) {
	if r.Method != http.MethodDelete {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allowWrite(w, r, appID, envID) {
		return
	}
	ns, ok := h.findAppNS(w, r, appID, envID)
	if !ok {
		return
	}
	// 归属校验：ref 必须挂在本应用派生 ns 上（防跨应用解除他人引用）。
	refs, err := h.repo.ListNSRefs(r.Context(), ns.ID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	belongs := false
	for _, ref := range refs {
		if ref.ID == refID {
			belongs = true
			break
		}
	}
	if !belongs {
		httputil.WriteError(w, http.StatusNotFound, "引用不存在: "+refID)
		return
	}
	if err := h.repo.DeleteNSRef(r.Context(), refID); err != nil {
		h.writeRefErr(w, err)
		return
	}
	h.recordAudit(r.Context(), ns.TenantID, "configcenter_ns_ref_remove", appID, "ref="+refID+",envId="+envID)
	httputil.WriteData(w, map[string]string{"deleted": refID})
}

// sharedRefView 引用富化视图（列表展示：shared ns 名 + active 版本 + key 数）。
type sharedRefView struct {
	NSRef
	SharedName    string `json:"sharedName,omitempty"`    // shared ns 已删（悬挂）省略
	SharedVersion int    `json:"sharedVersion,omitempty"` // 0=未发布
	SharedKeys    int    `json:"sharedKeys"`
}

// writeRefErr 引用错误统一映射（sentinel → HTTP）。
func (h *AppHandler) writeRefErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNamespaceNotFound):
		httputil.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrRefExists):
		httputil.WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrRefNotShared):
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrRefNotFound):
		httputil.WriteError(w, http.StatusNotFound, err.Error())
	default:
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
	}
}

// serveLanePromote POST /dynamic-configs/lane-overrides/promote 泳道灰度提升到基线：
// 覆盖合并进基线 draft（UpsertItem 覆盖值）→ CreatePublish 新版本 → 逐 key 删覆盖。
// 失败语义：合并/发布失败时不删覆盖（泳道维持原状，可重试）；删覆盖失败则新版本已生效
// 但覆盖残留（幂等重试 promote 会因空集 400，提示手动清理——接受此边界，不做分布式事务）。
// 权限与 publish 对称（application:write + AppGuard write + prod 闸门——提升即全量生效）。
func (h *AppHandler) serveLanePromote(w http.ResponseWriter, r *http.Request, appID, envID, lane string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if lane == "" || lane == "default" {
		httputil.WriteError(w, http.StatusBadRequest, "lane 参数必填且非 default")
		return
	}
	if !h.allowWrite(w, r, appID, envID) {
		return
	}
	ns, err := h.ensureAppNS(w, r, appID, envID)
	if err != nil {
		return
	}
	// 直接按精确 (app, env, lane) 取覆盖（不走 env 回退——提升的是用户在页面上看到的那个 env 的泳道覆盖）
	ovs, err := h.repo.ListLaneOverrides(r.Context(), appID, envID, lane)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	if len(ovs) == 0 {
		httputil.WriteError(w, http.StatusBadRequest, "该泳道无覆盖可提升")
		return
	}
	// ① 覆盖值合并进基线 draft（同 key 覆盖更新）
	for _, o := range ovs {
		if _, err := h.repo.UpsertItem(r.Context(), ConfigItem{
			NamespaceID: ns.ID, Key: o.Key, Value: o.Value,
		}); err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return // 不删覆盖，可重试
		}
	}
	// ② 发新版本（快照含覆盖值）
	pub, err := h.repo.CreatePublish(r.Context(), ns.ID)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return // 同上，覆盖未动
	}
	// ③ 逐 key 删覆盖（新版本已生效；失败则覆盖残留，幂等重试 400 提示手动清理）
	for _, o := range ovs {
		if err := h.repo.DeleteLaneOverride(r.Context(), appID, envID, lane, o.Key); err != nil {
			httputil.WriteServiceError(w, http.StatusInternalServerError, fmt.Errorf("新版本 v%d 已发布，但清理泳道覆盖失败（key=%s）：请手动删除残留覆盖", pub.Version, o.Key))
			return
		}
	}
	h.recordAudit(r.Context(), ns.TenantID, "configcenter_lane_promote", appID,
		fmt.Sprintf("lane=%s,version=%d,keys=%d,envId=%s", lane, pub.Version, len(ovs), envID))
	httputil.WriteDataCreated(w, pub)
}

// recordAudit 记审计（best-effort，失败仅日志不阻断主流程）。
// actor 由 cmd/core 桥接时经 gateway.UserIDFrom(ctx) 取（AuditFunc 只收基本类型，handler 不感知身份）。
func (h *AppHandler) recordAudit(ctx context.Context, tenantID, action, resourceID, detail string) {
	if h.Audit == nil {
		return
	}
	h.Audit(ctx, tenantID, action, resourceID, detail)
}

// tenantIDFromCtx 从 ctx 取租户（无租户 ctx 返空串，由审计适配层兜底归 platform）。
func tenantIDFromCtx(ctx context.Context) string {
	tid, _ := tenant.TenantFrom(ctx)
	return tid
}
