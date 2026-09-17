package backup

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// Handler 是 backup HTTP 处理器（List/Create/Delete）。
// Authorize 方法级权限判定（main.go 注入 gateway.RequestAllowed），复用 dataservice 权限。
type Handler struct {
	repo       Repository
	Authorize  func(r *http.Request, perm string) bool
	envType    EnvTypeResolver     // envID → 环境类型（main.go 注入 environment store）
	resourceEv ResourceEnvResolver // resourceID → envID（main.go 注入 dataservice store）
	audit      AuditRecorder       // 写操作审计（备份是数据安全高敏操作）；nil 跳过
	actorFn    func(*http.Request) string
}

// AuditRecorder 记审计（依赖倒置，cmd/core 桥接 security.AuditStore；与 governance 同源模式）。
type AuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// HandlerOption 配置 handler（函数式选项）。
type HandlerOption func(*Handler)

// WithEnvResolver 注入环境类型 + 资源环境解析器，启用生产写校验（prod:write）。
func WithEnvResolver(et EnvTypeResolver, re ResourceEnvResolver) HandlerOption {
	return func(h *Handler) { h.envType = et; h.resourceEv = re }
}

// WithAudit 注入审计 recorder + actor 解析（写操作记审计，元审计 2026-09-04 补）。
func WithAudit(a AuditRecorder, actor func(*http.Request) string) HandlerOption {
	return func(h *Handler) { h.audit = a; h.actorFn = actor }
}

// recordAudit best-effort 记审计（错误不影响主流程）。
func (h *Handler) recordAudit(r *http.Request, tid, action, resourceID, detail string) {
	if h.audit == nil {
		return
	}
	actor := ""
	if h.actorFn != nil {
		actor = h.actorFn(r)
	}
	_ = h.audit.Record(r.Context(), tid, actor, action, "backup", resourceID, detail)
}

func NewHandler(repo Repository, opts ...HandlerOption) *Handler {
	h := &Handler{repo: repo}
	for _, o := range opts {
		o(h)
	}
	return h
}

func (h *Handler) authorize(r *http.Request, perm string) bool {
	if h.Authorize == nil {
		return true
	}
	return h.Authorize(r, perm)
}

// allowProdResource 校验目标数据服务资源所在环境的生产写权限。
// 链路：resourceID → envID → EnvType；prod 或解析失败（fail-closed）需 prod:write。
// 未注入 resolver 时跳过（dev/测试场景，与现状一致）。
func (h *Handler) allowProdResource(w http.ResponseWriter, r *http.Request, resourceID string) bool {
	if h.resourceEv == nil || h.envType == nil {
		return true
	}
	need := true // fail-closed 默认需校验
	if envID, err := h.resourceEv.ResourceEnv(r.Context(), resourceID); err == nil {
		if etype, err := h.envType.EnvType(r.Context(), envID); err == nil && etype != "prod" {
			need = false
		}
	}
	if !need {
		return true
	}
	if !h.authorize(r, PermProdWrite) {
		httputil.WriteError(w, http.StatusForbidden, "需要生产写权限（prod:write）")
		return false
	}
	return true
}

// tenantID 从 ctx 取租户；缺失即 fail-closed 写 401（不信任请求体 TenantID）。
func tenantID(w http.ResponseWriter, r *http.Request) (string, bool) {
	tid, ok := tenant.TenantFrom(r.Context())
	if !ok || tid == "" {
		httputil.WriteError(w, http.StatusUnauthorized, "missing tenant context")
		return "", false
	}
	return tid, true
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/api/backups" && r.Method == http.MethodGet:
		h.list(w, r)
	case p == "/api/backups" && r.Method == http.MethodPost:
		h.create(w, r)
	case strings.HasPrefix(p, "/api/backups/") && r.Method == http.MethodDelete:
		h.delete(w, r)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "不支持的操作")
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(r, "dataservice:read") {
		httputil.WriteError(w, http.StatusForbidden, "无权限")
		return
	}
	tid, ok := tenantID(w, r)
	if !ok {
		return
	}
	res := r.URL.Query().Get("resourceId")
	bs, err := h.repo.List(r.Context(), tid, res)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, bs)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(r, "dataservice:write") {
		httputil.WriteError(w, http.StatusForbidden, "无权限")
		return
	}
	var b Backup
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	tid, ok := tenantID(w, r)
	if !ok {
		return
	}
	b.TenantID = tid
	if b.ResourceID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "resourceId 必填")
		return
	}
	// 生产数据服务的备份属高危操作：需 prod:write（developer 生产只读）。
	if !h.allowProdResource(w, r, b.ResourceID) {
		return
	}
	// type 默认值必须先补再生成 ID（R8 审计 B1：原顺序在 body 无 type 时生成
	// 尾部悬挂 '-' 的 ID，且与默认值补齐后实际 type 不一致）。
	if b.Type == "" {
		b.Type = TypeFull
	}
	if b.ID == "" {
		b.ID = "bk-" + b.ResourceID + "-" + b.Type
	}
	b.Status = StatusCompleted
	b.SizeMB = deterministicSize(b.ResourceID + b.Type)
	b.CreatedAt = time.Now()
	if err := h.repo.Create(r.Context(), b); err != nil {
		httputil.WriteServiceError(w, http.StatusConflict, err)
		return
	}
	h.recordAudit(r, tid, "backup_create", b.ID, "resource="+b.ResourceID+" type="+b.Type)
	httputil.WriteDataCreated(w, b)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(r, "dataservice:write") {
		httputil.WriteError(w, http.StatusForbidden, "无权限")
		return
	}
	tid, ok := tenantID(w, r)
	if !ok {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/backups/")
	// 删除前先查备份归属资源，校验其环境的生产写权限。
	b, err := h.repo.Get(r.Context(), tid, id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if !h.allowProdResource(w, r, b.ResourceID) {
		return
	}
	if err := h.repo.Delete(r.Context(), tid, id); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	h.recordAudit(r, tid, "backup_delete", id, "resource="+b.ResourceID)
	w.WriteHeader(http.StatusNoContent)
}

// deterministicSize 由 key 派生确定性大小（mock，无随机）。
func deterministicSize(key string) int {
	sum := sha256.Sum256([]byte(key))
	return int(binary.BigEndian.Uint32(sum[:4])%500) + 10
}
