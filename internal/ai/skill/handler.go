package skill

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐，复用 agent:read/write——Skill 是 Agent 编排的一部分）。
const (
	PermSkillRead  = "agent:read"
	PermSkillWrite = "agent:write"
)

// Handler 暴露 Skill 管理 REST API。
//
// 路由：
//
//	GET    /api/skills           列表
//	POST   /api/skills           创建
//	GET    /api/skills/{id}      详情
//	PUT    /api/skills/{id}      更新
//	DELETE /api/skills/{id}      删除
type Handler struct {
	repo      Repository
	Authorize func(r *http.Request, perm string) bool
	// References 由装配层注入，查询本租户 Agent，不依赖 agent 包。
	References func(context.Context, string) ([]Reference, error)
}

// Reference 是引用 Skill 的 Agent 摘要。
type Reference struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func NewHandler(repo Repository) *Handler { return &Handler{repo: repo} }

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
	case path == "/api/skills/templates" || strings.HasPrefix(path, "/api/skills/templates/"):
		h.serveTemplates(w, r, path)
	case path == "/api/skills":
		h.serveCollection(w, r)
	case strings.HasPrefix(path, "/api/skills/"):
		h.serveItem(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermSkillRead) {
			return
		}
		list, err := h.repo.List(r.Context())
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermSkillWrite) {
			return
		}
		// 缺省启用，显式 false 必须保留；仓储不再覆盖调用者的状态。
		s := Skill{Enabled: true}
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		s.InstalledFrom = "" // 来源由模板安装/广场 fork 写入，不接受自建请求伪造。
		saved, err := h.repo.Create(r.Context(), s)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteDataCreated(w, saved)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) serveItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimRight(r.URL.Path, "/")
	rest := strings.TrimPrefix(path, "/api/skills/")
	if rest == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	id := rest
	if strings.HasSuffix(rest, "/references") {
		if r.Method != http.MethodGet {
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !h.allow(w, r, PermSkillRead) {
			return
		}
		id = strings.TrimSuffix(rest, "/references")
		refs, err := h.references(r.Context(), id)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, refs)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermSkillRead) {
			return
		}
		s, err := h.repo.Get(r.Context(), id)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, s)
	case http.MethodPut:
		if !h.allow(w, r, PermSkillWrite) {
			return
		}
		var s Skill
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		s.ID = id
		old, err := h.repo.Get(r.Context(), id)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		s.InstalledFrom = old.InstalledFrom
		if old.Enabled && !s.Enabled {
			if err := h.checkUnused(r.Context(), id); err != nil {
				h.writeErr(w, err)
				return
			}
		}
		saved, err := h.repo.Update(r.Context(), s)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, saved)
	case http.MethodDelete:
		if !h.allow(w, r, PermSkillWrite) {
			return
		}
		if err := h.checkUnused(r.Context(), id); err != nil {
			h.writeErr(w, err)
			return
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

// writeErr 映射领域 sentinel 到 HTTP 状态（与 tool/agent 同款）。
func (h *Handler) writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrSkillNotFound):
		httputil.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrSkillExists):
		httputil.WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrSkillInUse):
		httputil.WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrReferencesUnavailable):
		httputil.WriteError(w, http.StatusServiceUnavailable, err.Error())
	case isFieldErr(err):
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		httputil.WriteServiceError(w, http.StatusInternalServerError, err)
	}
}

func (h *Handler) references(ctx context.Context, id string) ([]Reference, error) {
	if _, err := h.repo.Get(ctx, id); err != nil {
		return nil, err
	}
	if h.References == nil {
		return nil, ErrReferencesUnavailable
	}
	refs, err := h.References(ctx, id)
	if err != nil {
		return nil, ErrReferencesUnavailable
	}
	if refs == nil {
		refs = []Reference{}
	}
	return refs, nil
}

func (h *Handler) checkUnused(ctx context.Context, id string) error {
	refs, err := h.references(ctx, id)
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		return ErrSkillInUse
	}
	return nil
}
