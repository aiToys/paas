// Package health 提供 Core 进程的存活探针端点。
package health

import (
	"net/http"

	"github.com/aitoys/paas/internal/httputil"
)

// Handler 实现 /livez 存活探针。
type Handler struct{}

// NewHandler 创建探针处理器。
func NewHandler() *Handler { return &Handler{} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// /livez 是 K8s 存活探针协议端点，保持裸 {"status":"ok"} 形态（不包 {data:T} 信封）。
	httputil.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
