// Package health 提供 Core 进程的存活探针端点。
package health

import (
	"encoding/json"
	"net/http"
)

// Handler 实现 /livez 存活探针。
type Handler struct{}

// NewHandler 创建探针处理器。
func NewHandler() *Handler { return &Handler{} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
