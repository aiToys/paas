package tool

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/ai/tool/mcp"
	"github.com/aitoys/paas/internal/httputil"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermToolRead  = "tool:read"
	PermToolWrite = "tool:write"
)

const toolInvokeTimeout = 30 * time.Second // MCP/HTTP 工具调用上限

// Handler 暴露工具管理 REST API。
//
// 路由：
//
//	GET    /api/tools           列表
//	POST   /api/tools           创建
//	GET    /api/tools/{id}      详情
//	PUT    /api/tools/{id}      更新
//	DELETE /api/tools/{id}      删除
//	POST   /api/tools/{id}/test 测试工具（MCP: initialize + tools/list）
//	POST   /api/tools/{id}/invoke 调用工具（MCP: tools/call，body={name,arguments}）
type Handler struct {
	repo      Repository
	Authorize func(r *http.Request, perm string) bool
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
	case path == "/api/tools":
		h.serveCollection(w, r)
	case strings.HasPrefix(path, "/api/tools/"):
		h.serveItem(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermToolRead) {
			return
		}
		list, err := h.repo.List(r.Context())
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		// 响应一律掩码副本（apiKey 等凭证不回传前端）
		masked := make([]Tool, len(list))
		for i, t := range list {
			masked[i] = t.Masked()
		}
		httputil.WriteData(w, masked)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermToolWrite) {
			return
		}
		var t Tool
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		saved, err := h.repo.Create(r.Context(), t)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteDataCreated(w, saved.Masked())
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) serveItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimRight(r.URL.Path, "/")
	rest := strings.TrimPrefix(path, "/api/tools/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	id := parts[0]
	switch {
	case len(parts) == 1:
		h.serveOne(w, r, id)
	case len(parts) == 2 && parts[1] == "test":
		h.serveTest(w, r, id)
	case len(parts) == 2 && parts[1] == "invoke":
		h.serveInvoke(w, r, id)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveOne(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermToolRead) {
			return
		}
		t, err := h.repo.Get(r.Context(), id)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, t.Masked())
	case http.MethodPut:
		if !h.allow(w, r, PermToolWrite) {
			return
		}
		var t Tool
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		t.ID = id
		// 掩码回写保护：前端编辑回填掩码值时保留库中原值，防掩码覆盖真实凭证（与 appconfig 同款）
		cur, err := h.repo.Get(r.Context(), id)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		for k, v := range t.Config {
			if v == ConfigMask && cur.Config[k] != "" {
				t.Config[k] = cur.Config[k]
			}
		}
		saved, err := h.repo.Update(r.Context(), t)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, saved.Masked())
	case http.MethodDelete:
		if !h.allow(w, r, PermToolWrite) {
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

// serveTest 测试工具连通性（type=mcp：initialize + tools/list，返工具定义）。
// 其他类型留后续（http: ping；builtin: 列注册 handler）。
func (h *Handler) serveTest(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermToolRead) {
		return
	}
	t, err := h.repo.Get(r.Context(), id)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if t.Type != TypeMCP {
		httputil.WriteError(w, http.StatusBadRequest, "test 暂仅支持 mcp 类型")
		return
	}
	ctx, cancel := withTimeout(r.Context(), toolInvokeTimeout)
	defer cancel()
	cli := mcp.GetClient(t.Config[CfgMCPServerURL], t.Config[CfgMCPAPIKey])
	if err := cli.Initialize(ctx); err != nil {
		httputil.WriteServiceError(w, http.StatusBadGateway, err)
		return
	}
	tools, err := cli.ListTools(ctx)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadGateway, err)
		return
	}
	httputil.WriteData(w, map[string]any{"tools": tools})
}

// serveInvoke 调用工具（type=mcp：tools/call，body={name,arguments}）。
func (h *Handler) serveInvoke(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermToolRead) {
		return
	}
	t, err := h.repo.Get(r.Context(), id)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if t.Type != TypeMCP {
		httputil.WriteError(w, http.StatusBadRequest, "invoke 暂仅支持 mcp 类型")
		return
	}
	var req struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Name == "" {
		httputil.WriteError(w, http.StatusBadRequest, "name 不能为空")
		return
	}
	ctx, cancel := withTimeout(r.Context(), toolInvokeTimeout)
	defer cancel()
	cli := mcp.GetClient(t.Config[CfgMCPServerURL], t.Config[CfgMCPAPIKey])
	res, err := cli.Invoke(ctx, req.Name, req.Arguments)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadGateway, err)
		return
	}
	httputil.WriteData(w, res)
}

// writeErr 映射领域 sentinel 到 HTTP 状态（与 KB/maas 同款）。
func (h *Handler) writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrToolNotFound):
		httputil.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrToolExists):
		httputil.WriteError(w, http.StatusConflict, err.Error())
	case isFieldErr(err):
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		httputil.WriteServiceError(w, http.StatusInternalServerError, err)
	}
}
