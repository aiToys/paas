// 诊断触发入口（spec impl-spec S5.1）：POST /api/admin/diagnose/run。
//
// super_admin 语境（adminGuard + PermPlatformDiagnostics；cmd/core 装配）。
// 同步返回结构化报告（server 无 WriteTimeout；ctx 整体超时 diagnose.Timeout）——
// 诊断 P95 目标 <90s，SSE 的流式收益对 curl/脚本调用方不如同步 JSON 可靠。
package diagnose

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/ai/agent"
	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/provider"
)

// Runner 诊断运行器（依赖倒置；cmd/core 桥接 agent.Runtime.RunDiagnose）。
type Runner interface {
	RunDiagnose(ctx context.Context, a agent.Agent, msgs []provider.Message, onChunk func(provider.Chunk)) error
}

// AuditRecorder 轻量审计（与 devops change handler 同款窄接口）。
type AuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// Handler 诊断 HTTP handler。
type Handler struct {
	run   Runner
	model string // 主控模型（env PAAS_DIAGNOSE_MODEL 或默认）
	audit AuditRecorder
	actor func(*http.Request) string
}

// NewHandler 构造。model 为诊断主控模型名（装配层读 env，空用 DefaultModel）。
func NewHandler(run Runner, model string) *Handler {
	if model == "" {
		model = DefaultModel
	}
	return &Handler{run: run, model: model}
}

// WithAudit 注入审计（依赖倒置；不调则不记）。
func (h *Handler) WithAudit(a AuditRecorder, actorFn func(*http.Request) string) *Handler {
	h.audit = a
	h.actor = actorFn
	return h
}

// ServeHTTP 路由：POST /run 触发诊断。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/run") && r.Method == http.MethodPost:
		h.handleRun(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "route not found")
	}
}

// diagnoseRun 虚拟诊断 agent（agent 包 Agent 的最小使用：Model/SystemPrompt/MaxSteps）。
func (h *Handler) diagAgent() agent.Agent {
	return agent.Agent{
		ID:           "diag-builtin",
		Name:         "平台诊断",
		Model:        h.model,
		SystemPrompt: SystemPrompt(),
		MaxSteps:     MaxSteps,
		Enabled:      true,
	}
}

func (h *Handler) handleRun(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "请求体不是合法 JSON")
		return
	}
	if err := req.Normalize(); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.run == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "诊断运行器未装配（内置工具集或模型不可用）")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), Timeout)
	defer cancel()

	msgs := []provider.Message{{Role: "user", Content: UserPrompt(req)}}
	var sb strings.Builder
	start := time.Now()
	err := h.run.RunDiagnose(ctx, h.diagAgent(), msgs, func(c provider.Chunk) {
		if c.Content != "" {
			sb.WriteString(c.Content)
		}
	})
	duration := time.Since(start).Seconds()
	if err != nil {
		// 底层技术错误脱敏（依赖失败/模型不可达等信息对调用方有意义，技术细节不泄漏）。
		httputil.WriteServiceError(w, http.StatusBadGateway, err)
		return
	}
	rep := ParseReport(sb.String())
	rep.DurationSeconds = duration
	rep.TokensUsed = len([]rune(UserPrompt(req))) + sb.Len() // rune 粗估（与 MeterFn 同口径）
	h.recordAudit(r, req, rep)
	httputil.WriteData(w, rep)
}

// recordAudit 写操作审计（POST /run 是写方法语义；诊断目标 + 结论摘要入 detail）。
func (h *Handler) recordAudit(r *http.Request, req Request, rep Report) {
	if h.audit == nil {
		return
	}
	actor := ""
	if h.actor != nil {
		actor = h.actor(r)
	}
	detail := "target=" + req.Target.Namespace + "/" + req.Target.Workload
	if req.Target.Pod != "" {
		detail += "/" + req.Target.Pod
	}
	detail += " severity=" + rep.Verdict.Severity + " category=" + rep.Verdict.Category
	if len(rep.RootCauseHypotheses) > 0 {
		detail += " top=" + rep.RootCauseHypotheses[0].Hypothesis
	}
	// 诊断是平台级操作：无租户 ctx 时归 "platform"（adapter 已处理）。
	_ = h.audit.Record(r.Context(), "", actor, "diagnose.run", "diagnosis",
		req.Target.Namespace+"/"+req.Target.Workload, detail)
}
