// Package admin 实现 AI 编排实体的 admin 跨租户总览（只读）：
// /api/admin/ai/{agents|tools|knowledgebases|prompts|skills}。
//
// 与其他 /api/admin/* 资源总览同款模式：super_admin 只读、ListAll 跨租户、
// 不绕过租户写（资源运维仍在 console-user 租户内）。
package admin

import (
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/ai/agent"
	"github.com/aitoys/paas/internal/ai/knowledgebase"
	"github.com/aitoys/paas/internal/ai/marketplace"
	"github.com/aitoys/paas/internal/ai/prompt"
	"github.com/aitoys/paas/internal/ai/skill"
	"github.com/aitoys/paas/internal/ai/tool"
	"github.com/aitoys/paas/internal/httputil"
)

// Handler AI 编排 admin 总览。
type Handler struct {
	agents  agent.Repository
	tools   tool.Repository
	kbs     knowledgebase.Repository
	prompts prompt.Repository
	skills  skill.Repository
	market  marketplace.Repository
}

func NewHandler(
	agents agent.Repository,
	tools tool.Repository,
	kbs knowledgebase.Repository,
	prompts prompt.Repository,
	skills skill.Repository,
	market marketplace.Repository,
) *Handler {
	return &Handler{agents: agents, tools: tools, kbs: kbs, prompts: prompts, skills: skills, market: market}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	switch path {
	case "/api/admin/ai/agents":
		list, err := h.agents.ListAll(r.Context())
		h.write(w, list, err)
	case "/api/admin/ai/tools":
		list, err := h.tools.ListAll(r.Context())
		h.write(w, list, err)
	case "/api/admin/ai/knowledgebases":
		list, err := h.kbs.ListAll(r.Context())
		h.write(w, list, err)
	case "/api/admin/ai/prompts":
		list, err := h.prompts.ListAll(r.Context())
		h.write(w, list, err)
	case "/api/admin/ai/skills":
		list, err := h.skills.ListAll(r.Context())
		h.write(w, list, err)
	case "/api/admin/ai/marketplace":
		// 广场总览（admin 可发现违规内容；下架走 DELETE /api/marketplace/{id} + IsAdmin）
		list, err := h.market.ListAll(r.Context())
		h.write(w, list, err)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) write(w http.ResponseWriter, list any, err error) {
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, list)
}
