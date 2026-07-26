package application

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Handler 暴露应用 REST API：列表、详情、创建、绑定资源。
type Handler struct {
	repo Repository
}

// NewHandler 创建应用 API handler。
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// ServeHTTP 路由到具体方法（Go 1.22 ServeMux 已按方法+路径分发，这里做子路由细分）。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimPrefix(r.URL.Path, "/api/applications")
	path = strings.Trim(path, "/")

	// GET /api/applications
	if path == "" && r.Method == http.MethodGet {
		apps, err := h.repo.List(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": apps})
		return
	}

	// POST /api/applications
	if path == "" && r.Method == http.MethodPost {
		var a Application
		if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid body")
			return
		}
		if err := h.repo.Create(r.Context(), a); err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(a)
		return
	}

	// 剩余: /{id} 或 /{id}/bindings
	parts := strings.Split(path, "/")
	id := parts[0]

	if r.Method == http.MethodGet && len(parts) == 1 {
		a, err := h.repo.Get(r.Context(), id)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(a)
		return
	}

	if r.Method == http.MethodPost && len(parts) == 2 && parts[1] == "bindings" {
		var body struct {
			Type string `json:"type"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Type == "" {
			writeErr(w, http.StatusBadRequest, "missing type")
			return
		}
		a, err := h.repo.BindResource(r.Context(), id, body.Type)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		_ = json.NewEncoder(w).Encode(a)
		return
	}

	writeErr(w, http.StatusNotFound, "not found")
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
