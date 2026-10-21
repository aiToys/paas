// Package configcenter admin_handler.go 提供配置中心 admin 只读 API（spec 2026-10-19 §8.6）。
//
// 路由（全挂 adminGuard(super_admin)，cmd/core 装配；handler 内不重复鉴权）：
//
//	GET /api/admin/configcenter/namespaces       跨租户命名空间列表（聚合 draft 条数 + active 版本）
//	GET /api/admin/configcenter/namespaces/{id}  详情四区：草稿（掩码）/ 已发布（仅键）/ 引用影响面 / 发布记录
//
// 后台先只读（spec §8.6）；敏感值全掩码（Value → ••••••，Publish.Snapshot 只列 keys），
// diff 允许看键变化（added/removed）而非明文。跨租户单条读沿用「ListAll 定位拿 TenantID →
// 目标租户 ctx 复用既有租户方法」模式（pipeline/governance admin 同款），零新增跨租户 store 方法。
package configcenter

import (
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	adminutil "github.com/aitoys/paas/internal/web/admin"
)

// adminMaskedValue admin 视图配置值统一掩码（与 security Maskged 同形；明文仅租户侧 Resolve 用）。
const adminMaskedValue = "••••••"

// AdminConfigItem admin 视图配置项（值掩码，键/类型/时间保留——排障需要知道有哪些键）。
type AdminConfigItem struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Value     string `json:"value"` // 恒为掩码
	Type      string `json:"type"`
	UpdatedAt string `json:"updatedAt"`
}

// AdminPublishView admin 视图发布版本（Snapshot 只列 keys，不回明文）。
type AdminPublishView struct {
	ID        string   `json:"id"`
	Version   int      `json:"version"`
	Status    string   `json:"status"` // active | rolled-back
	Keys      []string `json:"keys"`   // 快照键清单（有序）
	CreatedAt string   `json:"createdAt"`
}

// AdminNSDiff 草稿相对 active 的键变化（只看键不看值——值已掩码）。
// 无 active 发布时全部草稿键视为 added（首次发布前的一切都是待发布）。
type AdminNSDiff struct {
	Added   []string `json:"added"`   // 草稿有、active 无（新增未发布）
	Removed []string `json:"removed"` // active 有、草稿无（已删未发布）
}

// AdminNamespaceRow 列表行：ns 基础字段 + 轻量聚合（draft 条数 / active 版本，未发布为 0）。
type AdminNamespaceRow struct {
	Namespace
	DraftItems    int `json:"draftItems"`
	ActiveVersion int `json:"activeVersion"` // 0=从未发布
	RefCount      int `json:"refCount"`      // app=引用的 shared 数；shared=被引用数（影响面）
}

// AdminNSDetail 详情四区响应。
type AdminNSDetail struct {
	Namespace Namespace         `json:"namespace"`
	Draft     []AdminConfigItem `json:"draft"`
	Active    *AdminPublishView `json:"active"` // nil=从未发布
	Diff      AdminNSDiff       `json:"diff"`
	Refs      []NSRef           `json:"refs"` // scope=app：ListNSRefs（引用的 shared）；scope=shared：ListNSRefUsers（被谁引用）
	// RefsError 非空 = 引用清单查询失败（Refs 不代表无引用，前端显示错误条）
	RefsError string             `json:"refsError,omitempty"`
	Publishes []AdminPublishView `json:"publishes"`
}

// AdminHandler 配置中心 admin 只读 handler。
type AdminHandler struct {
	repo Repository
}

// NewAdminHandler 创建 admin handler。
func NewAdminHandler(repo Repository) *AdminHandler { return &AdminHandler{repo: repo} }

// ServeHTTP 按路径分发：/namespaces 列表；/namespaces/{id} 详情。
func (h *AdminHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	if path == "/api/admin/configcenter/namespaces" {
		if r.Method != http.MethodGet {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.serveList(w, r)
		return
	}
	if strings.HasPrefix(path, "/api/admin/configcenter/namespaces/") {
		if r.Method != http.MethodGet {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		h.serveDetail(w, r, strings.Trim(strings.TrimPrefix(path, "/api/admin/configcenter/namespaces/"), "/"))
		return
	}
	httputil.WriteError(w, http.StatusNotFound, "not found")
}

// serveList 跨租户 ns 列表 + 轻量聚合（逐 ns 以归属租户 ctx 查 draft 数/active 版本/引用数；
// 单 ns 失败跳过不连坐——admin 总览不因个别租户数据异常整体 500）。
func (h *AdminHandler) serveList(w http.ResponseWriter, r *http.Request) {
	all, err := h.repo.ListAllNamespaces(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	rows := make([]AdminNamespaceRow, 0, len(all))
	for _, ns := range all {
		row := AdminNamespaceRow{Namespace: ns}
		ctx, _ := adminutil.TenantCtx(r, ns.TenantID)
		if items, err := h.repo.ListItems(ctx, ns.ID); err == nil {
			row.DraftItems = len(items)
		}
		if p, ok, _ := h.repo.ActivePublish(ctx, ns.ID); ok {
			row.ActiveVersion = p.Version
		}
		if ns.Scope == "shared" {
			if refs, err := h.repo.ListNSRefUsers(ctx, ns.ID); err == nil {
				row.RefCount = len(refs)
			}
		} else {
			if refs, err := h.repo.ListNSRefs(ctx, ns.ID); err == nil {
				row.RefCount = len(refs)
			}
		}
		rows = append(rows, row)
	}
	rows = adminutil.FilterTenant(r, rows, func(x AdminNamespaceRow) string { return x.TenantID })
	httputil.WriteData(w, rows)
}

// serveDetail 详情四区：草稿（掩码）/ active（keys）/ diff（键变化）/ 引用影响面 + 发布记录。
func (h *AdminHandler) serveDetail(w http.ResponseWriter, r *http.Request, nsID string) {
	if nsID == "" || strings.Contains(nsID, "/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	all, err := h.repo.ListAllNamespaces(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	var ns Namespace
	found := false
	for _, n := range all {
		if n.ID == nsID {
			ns, found = n, true
			break
		}
	}
	if !found {
		httputil.WriteError(w, http.StatusNotFound, "命名空间不存在: "+nsID)
		return
	}
	ctx, _ := adminutil.TenantCtx(r, ns.TenantID)
	out := AdminNSDetail{Namespace: ns, Draft: []AdminConfigItem{}, Refs: []NSRef{}, Publishes: []AdminPublishView{}}

	// 草稿：值掩码（键/类型/时间保留）。
	draftKeys := map[string]bool{}
	if items, err := h.repo.ListItems(ctx, ns.ID); err == nil {
		for _, it := range items {
			draftKeys[it.Key] = true
			out.Draft = append(out.Draft, AdminConfigItem{
				ID: it.ID, Key: it.Key, Value: adminMaskedValue, Type: it.Type,
				UpdatedAt: it.UpdatedAt.Format("2006-01-02 15:04:05"),
			})
		}
	}

	// active 发布 + 键 diff（added=draft 有 active 无；removed=active 有 draft 无）。
	if p, ok, _ := h.repo.ActivePublish(ctx, ns.ID); ok {
		ap := adminPublishView(p)
		out.Active = &ap
		out.Diff.Added = []string{}
		out.Diff.Removed = []string{}
		for _, k := range ap.Keys {
			if !draftKeys[k] {
				out.Diff.Removed = append(out.Diff.Removed, k)
			}
		}
		for k := range draftKeys {
			if _, inSnap := p.Snapshot[k]; !inSnap {
				out.Diff.Added = append(out.Diff.Added, k)
			}
		}
		sort.Strings(out.Diff.Added)
		sort.Strings(out.Diff.Removed)
	}

	// 引用影响面：shared=被哪些应用 ns 引用（发布时的影响范围）；app=引用了哪些 shared。
	// 查询失败置 RefsError（R4 审计 M-3）：失败 ≠ 0 引用——吞成空表会让管理员误判
	// 「没有引用方、可以放心改/删」。
	var refsErr error
	if ns.Scope == "shared" {
		out.Refs, refsErr = h.repo.ListNSRefUsers(ctx, ns.ID)
	} else {
		out.Refs, refsErr = h.repo.ListNSRefs(ctx, ns.ID)
	}
	if refsErr != nil {
		out.RefsError = "引用清单加载失败（不视为无引用）"
		log.Printf("configcenter admin: ns %s 引用查询失败: %v", nsID, refsErr) //nolint:gosec // nsID 非敏感
		out.Refs = []NSRef{}
	}

	// 发布记录：版本降序（ListPublishes 已保证），快照只列 keys。
	if pubs, err := h.repo.ListPublishes(ctx, ns.ID); err == nil {
		for _, p := range pubs {
			out.Publishes = append(out.Publishes, adminPublishView(p))
		}
	}
	httputil.WriteData(w, out)
}

// adminPublishView Publish → admin 视图（keys 有序，值不回）。
func adminPublishView(p Publish) AdminPublishView {
	keys := make([]string, 0, len(p.Snapshot))
	for k := range p.Snapshot {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return AdminPublishView{
		ID: p.ID, Version: p.Version, Status: p.Status, Keys: keys,
		CreatedAt: p.CreatedAt.Format("2006-01-02 15:04:05"),
	}
}
