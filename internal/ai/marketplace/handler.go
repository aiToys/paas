package marketplace

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/ai/agent"
	"github.com/aitoys/paas/internal/ai/prompt"
	"github.com/aitoys/paas/internal/ai/skill"
	"github.com/aitoys/paas/internal/ai/tool"
	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// 粗粒度权限标识（复用 agent:read/write——广场是 Agent 编排的一部分）。
const (
	PermMarketRead  = "agent:read"
	PermMarketWrite = "agent:write"
)

// PublishRequest 发布请求体。
type PublishRequest struct {
	EntityType string `json:"entityType"` // skill | prompt | tool | agent
	EntityID   string `json:"entityId"`   // 本租户实体 ID
	Category   string `json:"category"`   // 广场分类（空则取实体自带 Category）
}

// InstallResult 安装结果（fork 后的新实体）。
type InstallResult struct {
	EntityType string `json:"entityType"`
	EntityID   string `json:"entityId"` // fork 到本租户的新实体 ID
	Name       string `json:"name"`    // 可能带同名后缀
}

// EntityForker 各实体模块实现的 fork 编排（依赖倒置，避免 marketplace → 各实体包 import）。
// 发布：BuildSnapshot 从本租户实体生成脱敏快照（ctx 是发布者租户）。
// 安装：InstallSnapshot 把快照 fork 到本租户（ctx 是安装者租户），返回新实体 ID + 最终名（同名后缀）。
type EntityForker interface {
	BuildSnapshot(ctx context.Context, entityID, category string) (name, description, categoryOut string, snapshot json.RawMessage, err error)
	InstallSnapshot(ctx context.Context, item Item) (InstallResult, error)
}

// Handler 暴露广场 REST API。
//
// 路由：
//
//	GET    /api/marketplace                 广场列表（?entityType=&category=&q=）
//	GET    /api/marketplace/published       我的发布
//	POST   /api/marketplace                 发布（body {entityType, entityId, category}）
//	GET    /api/marketplace/{id}            详情（含 snapshot 预览）
//	DELETE /api/marketplace/{id}            下架（仅发布者）
//	POST   /api/marketplace/{id}/install    安装 fork 到本租户
type Handler struct {
	repo           Repository
	forkers        map[string]EntityForker // entityType → forker
	Authorize      func(r *http.Request, perm string) bool
	IsAdmin        func(r *http.Request) bool   // super_admin（admin 下架违规内容）
	PublisherNameFn func(r *http.Request) string // 发布者显示名（cmd/core 桥接 UserIDFrom）
}

func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo, forkers: map[string]EntityForker{}}
}

// RegisterForker 注册实体类型的发布/安装编排（cmd/core 装配时注入，依赖倒置）。
func (h *Handler) RegisterForker(entityType string, f EntityForker) { h.forkers[entityType] = f }

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/marketplace":
		h.serveCollection(w, r)
	case path == "/api/marketplace/published":
		h.servePublished(w, r)
	case strings.HasSuffix(path, "/install"):
		h.serveInstall(w, r, strings.TrimSuffix(strings.TrimPrefix(path, "/api/marketplace/"), "/install"))
	case strings.HasPrefix(path, "/api/marketplace/"):
		h.serveItem(w, r, strings.TrimPrefix(path, "/api/marketplace/"))
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermMarketRead) {
			return
		}
		q := r.URL.Query()
		list, err := h.repo.List(r.Context(), q.Get("entityType"), q.Get("category"), q.Get("q"))
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		// 列表不回 snapshot 全文（卡片只要元信息，减带宽）
		type listItem struct {
			Item
			Snapshot json.RawMessage `json:"snapshot,omitempty"`
		}
		out := make([]listItem, 0, len(list))
		for _, it := range list {
			it.Snapshot = nil
			out = append(out, listItem{Item: it})
		}
		httputil.WriteData(w, out)
	case http.MethodPost:
		if !h.allow(w, r, PermMarketWrite) {
			return
		}
		var req PublishRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		h.publish(w, r, req)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) publish(w http.ResponseWriter, r *http.Request, req PublishRequest) {
	f, ok := h.forkers[req.EntityType]
	if !ok {
		httputil.WriteError(w, http.StatusBadRequest, "不支持的 entityType: "+req.EntityType)
		return
	}
	name, desc, cat, snap, err := f.BuildSnapshot(r.Context(), req.EntityID, req.Category)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if cat == "" {
		httputil.WriteError(w, http.StatusBadRequest, ErrEmptyCategory.Error())
		return
	}
	tid, _ := tenant.TenantFrom(r.Context())
	it, err := h.repo.Create(r.Context(), Item{
		EntityType: req.EntityType, Name: name, Description: desc, Category: cat,
		Snapshot: snap, PublisherTenant: tid, PublisherName: h.publisherDisplayName(r),
	})
	if err != nil {
		h.writeErr(w, err)
		return
	}
	httputil.WriteDataCreated(w, it)
}

func (h *Handler) servePublished(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermMarketRead) {
		return
	}
	tid, _ := tenant.TenantFrom(r.Context())
	list, err := h.repo.ListByPublisher(r.Context(), tid)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, list)
}

func (h *Handler) serveItem(w http.ResponseWriter, r *http.Request, id string) {
	if id == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermMarketRead) {
			return
		}
		it, err := h.repo.Get(r.Context(), id)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, it)
	case http.MethodDelete:
		// 下架：发布者或 super_admin
		if !h.allow(w, r, PermMarketWrite) {
			return
		}
		it, err := h.repo.Get(r.Context(), id)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		if h.IsAdmin == nil || !h.IsAdmin(r) {
			tid, _ := tenant.TenantFrom(r.Context())
			if it.PublisherTenant != tid {
				httputil.WriteError(w, http.StatusForbidden, ErrNotPublisher.Error())
				return
			}
		}
		if err := h.repo.Delete(r.Context(), id); err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, map[string]string{"deleted": id})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) serveInstall(w http.ResponseWriter, r *http.Request, id string) {
	if id == "" || r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermMarketWrite) {
		return
	}
	it, err := h.repo.Get(r.Context(), id)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	f, ok := h.forkers[it.EntityType]
	if !ok {
		httputil.WriteError(w, http.StatusInternalServerError, "unsupported entityType: "+it.EntityType)
		return
	}
	res, err := f.InstallSnapshot(r.Context(), it)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if err := h.repo.IncInstalls(r.Context(), id); err != nil {
		// 安装已成功，计数失败不阻断（best-effort）
		_ = err
	}
	httputil.WriteDataCreated(w, res)
}

// isExistsErr 判定各实体仓储的同名冲突 sentinel（skill/prompt/tool/agent 四类）。
func isExistsErr(err error) bool {
	return err == skill.ErrSkillExists || err == prompt.ErrPromptExists ||
		err == tool.ErrToolExists || err == agent.ErrAgentExists
}

// PublisherNameFn 发布者显示名提取（cmd/core 桥接 gateway.UserIDFrom；未注入时空串，展示侧回退租户名）。
func (h *Handler) publisherDisplayName(r *http.Request) string {
	if h.PublisherNameFn == nil {
		return ""
	}
	return h.PublisherNameFn(r)
}

// writeErr 映射领域 sentinel 到 HTTP 状态。
func (h *Handler) writeErr(w http.ResponseWriter, err error) {
	switch {
	case err == ErrItemNotFound:
		httputil.WriteError(w, http.StatusNotFound, err.Error())
	case err == ErrNotPublisher:
		httputil.WriteError(w, http.StatusForbidden, err.Error())
	case isExistsErr(err):
		// 并发安装同名（uniqueName 检查与 Create 之间的窗口）→ 409 提示重试，而非 500
		httputil.WriteError(w, http.StatusConflict, "同名实体已存在（并发安装），请重试: "+err.Error())
	case IsFieldErr(err):
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		httputil.WriteServiceError(w, http.StatusInternalServerError, err)
	}
}
