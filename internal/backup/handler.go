package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// Handler 是 backup HTTP 处理器（List/Create/Delete/Restore）。
// Authorize 方法级权限判定（main.go 注入 gateway.RequestAllowed），复用 dataservice 权限。
type Handler struct {
	repo       Repository
	Authorize  func(r *http.Request, perm string) bool
	envType    EnvTypeResolver     // envID → 环境类型（main.go 注入 environment store）
	resourceEv ResourceEnvResolver // resourceID → envID（main.go 注入 dataservice store）
	audit      AuditRecorder       // 写操作审计（备份是数据安全高敏操作）；nil 跳过
	actorFn    func(*http.Request) string

	// 真实快照执行面（spec 专项05 阶段B）；nil = 无 K8s 数据面，snapshot/restore 503。
	executor Executor
	targets  TargetResolver
	restorer Restorer
}

// Restorer 从源数据服务派生并创建恢复目标新实例（依赖倒置，cmd/core 桥接 dataservice
// repo+applier；backup 不 import dataservice 避免领域耦合）。
type Restorer interface {
	// CreateRestored 复制源实例规格创建新实例（name 指定新名），返回新实例 ID。
	// 新实例由 dataservice applier 投影数据面（STS 拉起由恢复 Job 内 pg_isready 等待）。
	CreateRestored(ctx context.Context, tenantID, sourceID, targetID, name string) (newID string, err error)
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

// WithExecutor 注入真实快照执行面（K8s 数据面部署时；nil 保持纯记录模式）。
func WithExecutor(ex Executor, t TargetResolver, r Restorer) HandlerOption {
	return func(h *Handler) { h.executor = ex; h.targets = t; h.restorer = r }
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
	case strings.HasPrefix(p, "/api/backups/") && strings.HasSuffix(p, "/restore") && r.Method == http.MethodPost:
		h.restore(w, r)
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

// newBackupID 时间基 ID（同资源可多次备份——旧确定性 ID「bk-<ds>-<type>」致同资源
// 第二次创建 409，只能删旧建新，历史行为修正）。
func newBackupID() string {
	return "bk-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(r, "dataservice:write") {
		httputil.WriteError(w, http.StatusForbidden, "无权限")
		return
	}
	var input CreateBackupRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	tid, ok := tenantID(w, r)
	if !ok {
		return
	}
	// 丢弃客户端伪造的产物、校验与运行状态，只接受创建参数。
	b := Backup{ResourceID: input.ResourceID, Type: input.Type, Mode: input.Mode}
	b.TenantID = tid
	if b.ResourceID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "resourceId 必填")
		return
	}
	// 生产数据服务的备份属高危操作：需 prod:write（developer 生产只读）。
	if !h.allowProdResource(w, r, b.ResourceID) {
		return
	}
	// type 默认值必须先补再生成 ID（R8 审计 B1）。
	if b.Type == "" {
		b.Type = TypeFull
	}
	b.ID = newBackupID()
	b.CreatedAt = time.Now()

	mode := b.Mode
	if mode == "" {
		mode = ModeRecord // 兼容旧前端（无 mode 即记录模式）
	}
	switch mode {
	case ModeRecord:
		// 记录模式：仅持久化意图（无产物无恢复证明）。SizeMB=0——模拟值不混入
		// 真实容量统计（spec 专项05 阶段A：不再生成 deterministicSize 假大小）。
		b.Mode = ModeRecord
		b.Status = StatusCompleted
		if err := h.repo.Create(r.Context(), b); err != nil {
			httputil.WriteServiceError(w, http.StatusConflict, err)
			return
		}
		h.recordAudit(r, tid, "backup_create", b.ID, "mode=record resource="+b.ResourceID)
		httputil.WriteDataCreated(w, b)
	case ModeSnapshot:
		if b.Type != TypeFull {
			httputil.WriteError(w, http.StatusBadRequest, "真实快照仅支持 full 类型")
			return
		}
		if h.executor == nil || h.targets == nil {
			httputil.WriteError(w, http.StatusServiceUnavailable, "真实快照未启用（无 K8s 数据面或执行器未装配）")
			return
		}
		t, err := h.targets.ResolveTarget(r.Context(), tid, b.ResourceID)
		if err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "数据服务不存在或无法定位数据面")
			return
		}
		if !EngineSupported(t.Engine) {
			httputil.WriteError(w, http.StatusBadRequest,
				fmt.Sprintf("引擎 %s 暂不支持真实快照（当前支持：postgres）", t.Engine))
			return
		}
		// 受理即落库（Pacer 接管推进；Start 同步失败不回滚受理——观察器重试/超时兜底）。
		b.Mode = ModeSnapshot
		b.Status = StatusAccepted
		b.Engine = t.Engine
		b.Provider = "file"
		if err := h.repo.Create(r.Context(), b); err != nil {
			httputil.WriteServiceError(w, http.StatusConflict, err)
			return
		}
		if serr := h.executor.Start(r.Context(), b, t); serr != nil {
			h.recordAudit(r, tid, "backup_create", b.ID, "mode=snapshot resource="+b.ResourceID+" start_err="+truncate(serr.Error(), 120))
		} else {
			h.recordAudit(r, tid, "backup_create", b.ID, "mode=snapshot resource="+b.ResourceID)
		}
		httputil.WriteDataCreated(w, b)
	default:
		httputil.WriteError(w, http.StatusBadRequest, "mode 仅支持 record|snapshot（恢复作业经 /restore 创建）")
	}
}

// restore 从快照恢复到新建实例（spec 专项05 §4B：恢复默认建新实例；覆盖原实例不做）。
func (h *Handler) restore(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(r, "dataservice:write") {
		httputil.WriteError(w, http.StatusForbidden, "无权限")
		return
	}
	tid, ok := tenantID(w, r)
	if !ok {
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/backups/"), "/restore")
	src, err := h.repo.Get(r.Context(), tid, id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	// 恢复证据链：仅真实快照 + completed + 有产物定位可恢复（记录模式无恢复证明）。
	if src.EffectiveMode() != ModeSnapshot || src.Status != StatusCompleted || (src.ArtifactRef != "file://"+backupsPVCName+"/"+dumpFile(src) || !ValidChecksum(src.Checksum)) {
		httputil.WriteError(w, http.StatusConflict, "仅已完成的真实快照可恢复（记录模式无恢复证明）")
		return
	}
	if h.executor == nil || h.targets == nil || h.restorer == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "恢复执行面未装配")
		return
	}
	// 生产源实例恢复属高危：需 prod:write（新实例落在同租户，环境由源规格带出）。
	if !h.allowProdResource(w, r, src.ResourceID) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 128 {
		httputil.WriteError(w, http.StatusBadRequest, "恢复需要 8..128 字符的 Idempotency-Key，重试请复用原值")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	opID := RestoreID(tid, src.ID, key)
	if body.Name == "" {
		body.Name = "restored-" + strings.TrimPrefix(opID, "bk-r-")[:12]
	}
	// 先登记操作、再由 Pacer 幂等确保目标；任何失败均有可定位记录，重试不再新建。
	b := Backup{
		ID: opID, TenantID: tid, ResourceID: src.ResourceID, Type: TypeFull,
		Mode: ModeRestore, Status: StatusAccepted, Engine: src.Engine, Provider: "file",
		ArtifactRef: src.ArtifactRef, Checksum: src.Checksum,
		SourceBackupID: src.ID, RestoredResourceID: RestoreTargetID(opID), RestoreName: body.Name,
		CreatedAt: time.Now(),
	}
	if existing, err := h.repo.Get(r.Context(), tid, opID); err == nil {
		if existing.RestoreName != body.Name {
			httputil.WriteError(w, http.StatusConflict, "该幂等键已用于不同恢复参数")
			return
		}
		httputil.WriteData(w, existing)
		return
	}
	if err := h.repo.Create(r.Context(), b); err != nil {
		if existing, getErr := h.repo.Get(r.Context(), tid, opID); getErr == nil && existing.RestoreName == body.Name {
			httputil.WriteData(w, existing)
			return
		}
		httputil.WriteServiceError(w, http.StatusConflict, err)
		return
	}
	h.recordAudit(r, tid, "backup_restore", b.ID, "source="+src.ID+" restored="+b.RestoredResourceID)
	httputil.WriteDataCreated(w, b)
}

// restoreName 派生恢复实例名（时间基防撞名）。
func restoreName() string {
	return "restored-" + strconv.FormatInt(time.Now().UnixNano(), 36)
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
	if !b.Terminal() {
		httputil.WriteError(w, http.StatusConflict, "运行中的备份或恢复不能删除")
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
