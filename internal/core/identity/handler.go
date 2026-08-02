package identity

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// HashPasswordFn 把明文密码哈希为存储串（依赖倒置，避免 identity→auth 循环；
// main.go 注入 auth.HashPassword）。
type HashPasswordFn func(plain string) (string, error)

// PasswordValidatorFn 校验明文密码强度（依赖倒置，避免 identity->auth 循环）。
// 返 error 表示不达标。nil 时跳过校验（向后兼容）。
type PasswordValidatorFn func(plain string) error

// Handler 是 identity 管理 API 的 HTTP 处理器（/api/tenants、/api/users、/api/api-keys、/api/roles）。
// 各方法以 http.HandlerFunc 暴露，由 main.go 经 reg.Register 注册（同时登记 OpenAPI）。
type Handler struct {
	repo              Repository
	hashPassword      HashPasswordFn
	passwordValidator PasswordValidatorFn
	// IsPlatformAdmin 判定调用者是否平台超管（main.go 注入 gateway.IsPlatformAdmin）。
	// 平台超管可跨租户管理；普通 tenant-admin 仅限本租户（防越权）。
	IsPlatformAdmin func(*http.Request) bool
}

// NewHandler 创建 identity 管理 handler。
func NewHandler(repo Repository) *Handler { return &Handler{repo: repo} }

// HashPassword 设置密码哈希函数（main.go 注入 auth.HashPassword）。
func (h *Handler) HashPassword(fn HashPasswordFn) *Handler { h.hashPassword = fn; return h }

// PasswordValidator 设置密码强度校验器（main.go 注入 auth.ValidatePassword）。
func (h *Handler) PasswordValidator(fn PasswordValidatorFn) *Handler {
	h.passwordValidator = fn
	return h
}

// platformAdmin 判定调用者是否平台超管；未注入 IsPlatformAdmin 时保守按 false（最小权限）。
func (h *Handler) platformAdmin(r *http.Request) bool {
	if h.IsPlatformAdmin == nil {
		return false
	}
	return h.IsPlatformAdmin(r)
}

// callerTenant 取调用者租户（ctx）；缺失返空（tenant-admin 作用域即空，查不到任何记录）。
func callerTenant(r *http.Request) string {
	tid, _ := tenant.TenantFrom(r.Context())
	return tid
}

// —— 响应辅助（core 契约 {data:T}/{error:msg}）——

func decodeBody(r *http.Request, dst any) error {
	return json.NewDecoder(r.Body).Decode(dst)
}

// —— Tenants ——

func (h *Handler) ListTenants(w http.ResponseWriter, r *http.Request) {
	// 租户枚举为平台级能力：仅平台超管可访问，普通 tenant-admin 仅能感知本租户。
	if !h.platformAdmin(r) {
		httputil.WriteError(w, http.StatusForbidden, "需要平台管理员权限")
		return
	}
	ts, err := h.repo.ListTenants(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	httputil.WriteData(w, ts)
}

type tenantInput struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (h *Handler) CreateTenant(w http.ResponseWriter, r *http.Request) {
	if !h.platformAdmin(r) {
		httputil.WriteError(w, http.StatusForbidden, "需要平台管理员权限")
		return
	}
	var in tenantInput
	if err := decodeBody(r, &in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	if in.ID == "" || in.Name == "" {
		httputil.WriteError(w, http.StatusBadRequest, "id 与 name 必填")
		return
	}
	if err := h.repo.CreateTenant(r.Context(), Tenant{ID: in.ID, Name: in.Name, CreatedAt: time.Now()}); err != nil {
		httputil.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	httputil.WriteData(w, Tenant{ID: in.ID, Name: in.Name})
}

func (h *Handler) DeleteTenant(w http.ResponseWriter, r *http.Request) {
	if !h.platformAdmin(r) {
		httputil.WriteError(w, http.StatusForbidden, "需要平台管理员权限")
		return
	}
	id := pathID(r, "/api/tenants/")
	if id == "" {
		httputil.WriteError(w, http.StatusBadRequest, "缺少 id")
		return
	}
	if err := h.repo.DeleteTenant(r.Context(), id); err != nil {
		httputil.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// —— Users ——

func (h *Handler) ListUsers(w http.ResponseWriter, r *http.Request) {
	// 平台超管可按 query tenantId 过滤（空=全租户）；普通 tenant-admin 强制限定本租户。
	tenantID := r.URL.Query().Get("tenantId")
	if !h.platformAdmin(r) {
		tenantID = callerTenant(r)
	}
	us, err := h.repo.ListUsers(r.Context(), tenantID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	// 不回传 password_hash
	for i := range us {
		us[i].PasswordHash = ""
	}
	httputil.WriteData(w, us)
}

type userInput struct {
	ID       string   `json:"id"`
	TenantID string   `json:"tenantId"`
	Name     string   `json:"name"`
	Email    string   `json:"email"`
	Password string   `json:"password"`
	Roles    []string `json:"roles"`
	IsAdmin  bool     `json:"isAdmin"`
	Status   string   `json:"status"`
}

func (h *Handler) CreateUser(w http.ResponseWriter, r *http.Request) {
	var in userInput
	if err := decodeBody(r, &in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	if in.ID == "" || in.TenantID == "" || in.Name == "" {
		httputil.WriteError(w, http.StatusBadRequest, "id/tenantId/name 必填")
		return
	}
	// 普通 tenant-admin：强制归属本租户，且不可创建超管（防提权）。
	if !h.platformAdmin(r) {
		in.TenantID = callerTenant(r)
		in.IsAdmin = false
	}
	u := User{
		ID: in.ID, TenantID: in.TenantID, Name: in.Name, Email: in.Email,
		Roles: in.Roles, IsAdmin: in.IsAdmin, Status: in.Status, CreatedAt: time.Now(),
	}
	if u.Status == "" {
		u.Status = StatusActive
	}
	if in.Password != "" && h.hashPassword != nil {
		if h.passwordValidator != nil {
			if err := h.passwordValidator(in.Password); err != nil {
				httputil.WriteError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		hash, err := h.hashPassword(in.Password)
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, "密码哈希失败")
			return
		}
		u.PasswordHash = hash
	}
	if err := h.repo.CreateUser(r.Context(), u); err != nil {
		httputil.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	u.PasswordHash = ""
	httputil.WriteData(w, u)
}

func (h *Handler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "/api/users/")
	if id == "" {
		httputil.WriteError(w, http.StatusBadRequest, "缺少 id")
		return
	}
	// 普通 tenant-admin：目标用户必须归属本租户，且不可授予超管（防提权）。
	if !h.platformAdmin(r) {
		if !h.userInCallerTenant(w, r, id) {
			return
		}
	}
	var in userInput
	if err := decodeBody(r, &in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	if !h.platformAdmin(r) {
		in.IsAdmin = false
	}
	u := User{
		ID: id, Name: in.Name, Email: in.Email, Roles: in.Roles,
		IsAdmin: in.IsAdmin, Status: in.Status,
	}
	if in.Password != "" && h.hashPassword != nil {
		if h.passwordValidator != nil {
			if err := h.passwordValidator(in.Password); err != nil {
				httputil.WriteError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		hash, err := h.hashPassword(in.Password)
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, "密码哈希失败")
			return
		}
		u.PasswordHash = hash
	}
	if err := h.repo.UpdateUser(r.Context(), u); err != nil {
		httputil.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	httputil.WriteData(w, map[string]any{"id": id})
}

func (h *Handler) DeleteUser(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "/api/users/")
	if id == "" {
		httputil.WriteError(w, http.StatusBadRequest, "缺少 id")
		return
	}
	// 平台超管可跨租户删除（tenantID 空）；普通 tenant-admin 强制限定本租户。
	tenantID := ""
	if !h.platformAdmin(r) {
		if !h.userInCallerTenant(w, r, id) {
			return
		}
		tenantID = callerTenant(r)
	}
	if err := h.repo.DeleteUser(r.Context(), tenantID, id); err != nil {
		httputil.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// userInCallerTenant 校验目标用户归属调用者租户（tenant-admin 作用域）；
// 不归属或不存在写 404 返回 false（不泄漏存在性）。平台超管不应调用此方法。
func (h *Handler) userInCallerTenant(w http.ResponseWriter, r *http.Request, id string) bool {
	if _, err := h.repo.GetUser(r.Context(), callerTenant(r), id); err != nil {
		httputil.WriteError(w, http.StatusNotFound, "用户不存在")
		return false
	}
	return true
}

// —— API Keys ——

func (h *Handler) ListAPIKeys(w http.ResponseWriter, r *http.Request) {
	// 平台超管可按 query tenantId 过滤（空=全租户）；普通 tenant-admin 强制限定本租户。
	tenantID := r.URL.Query().Get("tenantId")
	if !h.platformAdmin(r) {
		tenantID = callerTenant(r)
	}
	ks, err := h.repo.ListAPIKeys(r.Context(), tenantID)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	// 列表掩码明文 key（不泄漏）
	for i := range ks {
		ks[i].Key = maskKey(ks[i].Key)
	}
	httputil.WriteData(w, ks)
}

type apiKeyInput struct {
	ID       string   `json:"id"`
	TenantID string   `json:"tenantId"`
	UserID   string   `json:"userId"`
	Roles    []string `json:"roles"`
}

func (h *Handler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	var in apiKeyInput
	if err := decodeBody(r, &in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	if in.TenantID == "" || in.UserID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "tenantId/userId 必填")
		return
	}
	// 普通 tenant-admin：API Key 强制归属本租户（防跨租户创建）。
	if !h.platformAdmin(r) {
		in.TenantID = callerTenant(r)
	}
	if in.ID == "" {
		in.ID = "k-" + randHex(8)
	}
	key := "sk-" + randHex(20)
	k := APIKey{
		ID: in.ID, TenantID: in.TenantID, UserID: in.UserID,
		Roles: in.Roles, Key: key, CreatedAt: time.Now(),
	}
	if err := h.repo.CreateAPIKey(r.Context(), k); err != nil {
		httputil.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	httputil.WriteData(w, k) // 创建时返明文一次
}

func (h *Handler) DeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "/api/api-keys/")
	if id == "" {
		httputil.WriteError(w, http.StatusBadRequest, "缺少 id")
		return
	}
	// 普通 tenant-admin：目标 Key 必须归属本租户（ListAPIKeys 过滤后核对，不泄漏存在性）。
	if !h.platformAdmin(r) && !h.apiKeyInCallerTenant(w, r, id) {
		return
	}
	if err := h.repo.DeleteAPIKey(r.Context(), id); err != nil {
		httputil.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// apiKeyInCallerTenant 校验目标 API Key 归属调用者租户；不归属写 404 返回 false。
func (h *Handler) apiKeyInCallerTenant(w http.ResponseWriter, r *http.Request, id string) bool {
	ks, err := h.repo.ListAPIKeys(r.Context(), callerTenant(r))
	if err != nil {
		httputil.WriteInternalError(w, err)
		return false
	}
	for _, k := range ks {
		if k.ID == id {
			return true
		}
	}
	httputil.WriteError(w, http.StatusNotFound, "API Key 不存在")
	return false
}

// —— Roles（只读）——

// roleView 是角色列表项（含权限）。
type roleView struct {
	Name        string       `json:"name"`
	Permissions []Permission `json:"permissions"`
}

func (h *Handler) ListRoles(w http.ResponseWriter, r *http.Request) {
	builtin := BuiltinRoles()
	out := make([]roleView, 0, len(builtin))
	for _, r := range builtin {
		out = append(out, roleView{Name: r.Name, Permissions: r.Permissions}) //nolint:staticcheck // S1016: Role 字段多于 roleView，显式字面量比整型转换更清晰
	}
	httputil.WriteData(w, out)
}

// —— 辅助 ——

// pathID 从请求路径取末段 id（如 /api/users/u-1 → u-1）。
func pathID(r *http.Request, prefix string) string {
	p := strings.TrimPrefix(r.URL.Path, prefix)
	// 去掉可能的子路径
	if i := strings.Index(p, "/"); i >= 0 {
		p = p[:i]
	}
	return p
}

// maskKey 掩码 API Key 明文（保留前缀，防泄漏）。
func maskKey(key string) string {
	if len(key) <= 8 {
		return strings.Repeat("*", len(key))
	}
	return key[:8] + strings.Repeat("*", len(key)-8)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
