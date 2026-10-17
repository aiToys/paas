// Package workload —— 运行态/运维操作/终端的 HTTP 端点（spec 2026-10-17 §7 增量契约）。
//
// 权限：读（runtime/events/logs）= workload:read；写（restart/recreate/terminal）=
// workload:write + 应用权限 + 生产 prod:write（服务端查真实环境，不信客户端参数）。
// 终端额外要求 workload:exec（tenant-admin 通配持有；developer/viewer 默认无）。
// 审计：restart/recreate/terminal 受理与终端连接关闭均留痕。
package workload

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// PermWorkloadExec 是容器终端的独立权限（不并入 workload:write：交互式 stdin 敏感度更高）。
// tenant:admin 通配通行；developer 默认不授予（spec §8）。
const PermWorkloadExec = "workload:exec"

// serveRuntime 处理 /api/workloads 下运行态子路由。命中已处理返回 true。
//
//	GET    /api/workloads/{id}/runtime
//	GET    /api/workloads/{id}/instances/{pod}/events?podUID=
//	POST   /api/workloads/{id}/restart                          → 202 {data:operation}
//	POST   /api/workloads/{id}/instances/{pod}/recreate         → 202 {data:operation}
//	GET    /api/workloads/{id}/operations[?limit=&offset=]
//	GET    /api/workloads/{id}/operations/{opID}
//	POST   /api/workloads/{id}/instances/{pod}/terminal-sessions
func (h *Handler) serveRuntime(w http.ResponseWriter, r *http.Request, rest string) bool {
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		return false
	}
	id := parts[0]
	if id == "" {
		return false
	}
	switch {
	case len(parts) == 2 && parts[1] == "runtime" && r.Method == http.MethodGet:
		h.serveRuntimeDetail(w, r, id)
		return true
	case len(parts) == 2 && parts[1] == "restart" && r.Method == http.MethodPost:
		h.serveRestart(w, r, id)
		return true
	case len(parts) == 2 && parts[1] == "operations" && r.Method == http.MethodGet:
		h.serveOperationsList(w, r, id)
		return true
	case len(parts) == 3 && parts[1] == "operations" && r.Method == http.MethodGet:
		h.serveOperationGet(w, r, id, parts[2])
		return true
	case len(parts) == 4 && parts[1] == "instances" && parts[3] == "events" && r.Method == http.MethodGet:
		h.servePodEvents(w, r, id, parts[2])
		return true
	case len(parts) == 4 && parts[1] == "instances" && parts[3] == "recreate" && r.Method == http.MethodPost:
		h.serveRecreate(w, r, id, parts[2])
		return true
	case len(parts) == 4 && parts[1] == "instances" && parts[3] == "terminal-sessions" && r.Method == http.MethodPost:
		h.serveTerminalCreate(w, r, id, parts[2])
		return true
	}
	return false
}

// serveRuntimeDetail GET /{id}/runtime：观察 + 实例 + 能力 + 活跃操作。
// 未注入 runtimeReader（非集群）返回显式 unavailable（空实例不伪装 fresh）。
func (h *Handler) serveRuntimeDetail(w http.ResponseWriter, r *http.Request, id string) {
	if !h.allow(w, r, PermWorkloadRead) {
		return
	}
	wl, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	var rt Runtime
	if h.runtimeReader == nil {
		rt = Runtime{
			Workload:     wl,
			Observation:  Observation{Source: ObsSourceNone, State: ObsStateUnavailable},
			Instances:    []RuntimeInstance{},
			Capabilities: capUnavailable("not_cluster", "非集群部署，运行态不可用"),
		}
	} else {
		rt, err = h.runtimeReader.Runtime(r.Context(), id)
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
	}
	rt.Workload = wl // 期望态以库内为准（reader 只填观察/实例/能力）
	if h.ops != nil {
		if active, aerr := h.ops.Active(r.Context(), id); aerr == nil && active.ID != "" {
			rt.ActiveOperation = &active
		}
	}
	httputil.WriteData(w, rt)
}

// capUnavailable 生成全不可用能力集。
func capUnavailable(code, msg string) RuntimeCapabilities {
	c := Capability{Allowed: false, ReasonCode: code, Message: msg}
	return RuntimeCapabilities{Logs: c, Events: c, Restart: c, Recreate: c, Exec: c}
}

// servePodEvents GET /{id}/instances/{pod}/events?podUID=
func (h *Handler) servePodEvents(w http.ResponseWriter, r *http.Request, id, pod string) {
	if !h.allow(w, r, PermWorkloadRead) {
		return
	}
	if _, err := h.repo.Get(r.Context(), id); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if h.runtimeReader == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "事件不可用（非集群部署）")
		return
	}
	// 参数名双收（podUid/podUID，与 logs 端点同因）。
	podUID := r.URL.Query().Get("podUid")
	if podUID == "" {
		podUID = r.URL.Query().Get("podUID")
	}
	events, err := h.runtimeReader.PodEvents(r.Context(), id, pod, podUID)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if events == nil {
		events = []PodEvent{}
	}
	httputil.WriteData(w, events)
}

// serveRestart POST /{id}/restart：受理滚动重启（202 + operation）。
func (h *Handler) serveRestart(w http.ResponseWriter, r *http.Request, id string) {
	if !h.allow(w, r, PermWorkloadWrite) {
		return
	}
	if h.ops == nil || h.runtimeReader == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "重启不可用（非集群部署或能力未启用）")
		return
	}
	wl, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if !h.allowApp(w, r, wl.AppID) {
		return
	}
	if !h.allowProd(w, r, wl.EnvID) {
		return
	}
	if wl.Type != TypeService {
		httputil.WriteError(w, http.StatusBadRequest, "仅常驻服务（service）支持滚动重启；Job 重跑/CronJob 立即执行是不同操作")
		return
	}
	if wl.Replicas <= 0 {
		httputil.WriteError(w, http.StatusBadRequest, "副本为 0，先恢复副本再执行重启")
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	if strings.TrimSpace(body.Reason) == "" {
		httputil.WriteError(w, http.StatusBadRequest, "请填写重启原因")
		return
	}
	actor := ""
	if h.ActorFn != nil {
		actor = h.ActorFn(r)
	}
	op, _, err := h.ops.CreateRestart(r.Context(), wl, body.Reason, actor, r.Header.Get("Idempotency-Key"))
	if err != nil {
		if errors.Is(err, ErrOpConflict) {
			httputil.WriteError(w, http.StatusConflict, "该工作负载已有进行中的运维操作，请等待完成后再试")
			return
		}
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(r, "workload_restart", id, "env="+wl.EnvID+" lane="+wl.LaneID+" reason="+body.Reason)
	w.WriteHeader(http.StatusAccepted) // 202 = 已受理；完成由操作记录跟踪
	_ = json.NewEncoder(w).Encode(map[string]any{"data": op})
}

// serveRecreate POST /{id}/instances/{pod}/recreate：受理单实例重建（Eviction）。
func (h *Handler) serveRecreate(w http.ResponseWriter, r *http.Request, id, pod string) {
	if !h.allow(w, r, PermWorkloadWrite) {
		return
	}
	if h.ops == nil || h.runtimeActor == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "重建不可用（非集群部署或能力未启用）")
		return
	}
	wl, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if !h.allowApp(w, r, wl.AppID) {
		return
	}
	if !h.allowProd(w, r, wl.EnvID) {
		return
	}
	var body struct {
		PodUID string `json:"podUid"`
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	if strings.TrimSpace(body.Reason) == "" {
		httputil.WriteError(w, http.StatusBadRequest, "请填写重建原因")
		return
	}
	if body.PodUID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "podUid 必填（防止实例名复用错删）")
		return
	}
	actor := ""
	if h.ActorFn != nil {
		actor = h.ActorFn(r)
	}
	// 先受理记录（快照副本数），再执行驱逐——执行失败立刻标 failed（有审计有记录）。
	op, _, err := h.ops.CreateRecreate(r.Context(), wl, pod, body.PodUID, body.Reason, actor, r.Header.Get("Idempotency-Key"))
	if err != nil {
		if errors.Is(err, ErrOpConflict) {
			httputil.WriteError(w, http.StatusConflict, "该工作负载已有进行中的运维操作，请等待完成后再试")
			return
		}
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	if err := h.runtimeActor.Evict(r.Context(), id, pod, body.PodUID); err != nil {
		op.Status = OpStatusFailed
		op.Evidence = "驱逐失败：" + err.Error()
		now := time.Now()
		op.FinishedAt = &now
		_ = h.ops.UpdateStatus(r.Context(), op)
		status := http.StatusBadRequest
		switch {
		case errors.Is(err, ErrUIDMismatch):
			httputil.WriteError(w, http.StatusConflict, "实例已变化（UID 不匹配），请刷新后重试")
			return
		case errors.Is(err, ErrTooManyEvict):
			httputil.WriteError(w, http.StatusTooManyRequests, "驱逐被 PodDisruptionBudget 阻断（可用副本不足），请稍后重试")
			return
		case errors.Is(err, ErrNotManaged):
			httputil.WriteError(w, http.StatusBadRequest, "该实例不由平台托管控制器管理，不支持重建")
			return
		}
		httputil.WriteServiceError(w, status, err)
		return
	}
	h.recordAudit(r, "workload_recreate", id, "pod="+pod+" uid="+body.PodUID+" reason="+body.Reason)
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": op})
}

// serveOperationsList GET /{id}/operations?limit=&offset=
func (h *Handler) serveOperationsList(w http.ResponseWriter, r *http.Request, id string) {
	if !h.allow(w, r, PermWorkloadRead) {
		return
	}
	if h.ops == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "操作记录不可用（能力未启用）")
		return
	}
	if _, err := h.repo.Get(r.Context(), id); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	var limit, offset int
	_, _ = fmt.Sscanf(r.URL.Query().Get("limit"), "%d", &limit) // 空/非数字回退默认值
	_, _ = fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &offset)
	ops, err := h.ops.ListByWorkload(r.Context(), id, limit, offset)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, ops)
}

// serveOperationGet GET /{id}/operations/{opID}
func (h *Handler) serveOperationGet(w http.ResponseWriter, r *http.Request, id, opID string) {
	if !h.allow(w, r, PermWorkloadRead) {
		return
	}
	if h.ops == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "操作记录不可用（能力未启用）")
		return
	}
	op, err := h.ops.Get(r.Context(), id, opID)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	httputil.WriteData(w, op)
}

// serveTerminalCreate POST /{id}/instances/{pod}/terminal-sessions
// body {podUid, container, shell, reason} → {data:{sessionId, expiresAt, streamPath}}
func (h *Handler) serveTerminalCreate(w http.ResponseWriter, r *http.Request, id, pod string) {
	if !h.allow(w, r, PermWorkloadExec) {
		return
	}
	if h.terminals == nil || !h.terminals.Enabled() {
		httputil.WriteError(w, http.StatusServiceUnavailable, "容器终端未启用")
		return
	}
	wl, err := h.repo.Get(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	if !h.allowApp(w, r, wl.AppID) {
		return
	}
	if !h.allowProd(w, r, wl.EnvID) {
		return
	}
	var body struct {
		PodUID    string `json:"podUid"`
		Container string `json:"container"`
		Shell     string `json:"shell"`
		Reason    string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.PodUID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "podUid 必填")
		return
	}
	if strings.TrimSpace(body.Reason) == "" {
		httputil.WriteError(w, http.StatusBadRequest, "请填写连接原因（审计留痕）")
		return
	}
	user := ""
	if h.UserFn != nil {
		user = h.UserFn(r)
	}
	tid := tenantIDOf(r)
	sid, exp, err := h.terminals.Create(tid, user, id, pod, body.PodUID, body.Container, body.Shell, body.Reason)
	if err != nil {
		switch {
		case errors.Is(err, ErrTerminalDisabled):
			httputil.WriteError(w, http.StatusServiceUnavailable, "容器终端未启用")
		case errors.Is(err, ErrTerminalBusy):
			httputil.WriteError(w, http.StatusConflict, "并发终端会话数已达上限（每用户 2 个）")
		default:
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
		}
		return
	}
	h.recordAudit(r, "workload_terminal_open", id, "pod="+pod+" container="+body.Container+" reason="+body.Reason)
	httputil.WriteDataCreated(w, map[string]any{
		"sessionId": sid, "expiresAt": exp, "streamPath": "/api/terminal-sessions/" + sid + "/stream",
	})
}

// tenantIDOf 从请求 ctx 取租户（终端会话绑定租户边界用）。
func tenantIDOf(r *http.Request) string {
	tid, _ := tenant.TenantFrom(r.Context())
	return tid
}
