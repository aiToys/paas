package identity

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// Handler 是 identity 管理 API 的 HTTP 处理器（/api/admin/tenants、/api/admin/users、/api/admin/api-keys、/api/admin/roles）。
// API Key 方法同时挂自助端点 /api/api-keys（auth 守卫，租户隔离见各方法 platformAdmin 分支）。
// 各方法以 http.HandlerFunc 暴露，由 main.go 经 reg.Register 注册（同时登记 OpenAPI）。
type Handler struct {
	repo              Repository
	hashPassword      HashPasswordFn
	passwordValidator PasswordValidatorFn
	// IsPlatformAdmin 判定调用者是否平台超管（main.go 注入 gateway.IsPlatformAdmin）。
	// 平台超管可跨租户管理；普通 tenant-admin 仅限本租户（防越权）。
	IsPlatformAdmin func(*http.Request) bool
	// CallerUserID/CallerRoles 取调用者用户 ID 与角色集（main.go 注入 gateway.UserIDFrom/RolesFrom）。
	// 自助 API Key 端点据此绑定密钥归属 + roles 封顶（只能赋予自身持有的角色，零提权）。
	// 未注入时返空（UserID 空则保留 body 值，roles 求交为空 → 返空集），保守安全。
	CallerUserID func(*http.Request) string
	CallerRoles  func(*http.Request) []string
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

// callerUserID 取调用者用户 ID（main.go 注入 CallerUserID）；未注入返空。
func (h *Handler) callerUserID(r *http.Request) string {
	if h.CallerUserID == nil {
		return ""
	}
	return h.CallerUserID(r)
}

// callerRoles 取调用者角色集（main.go 注入 CallerRoles）；未注入返空。
func (h *Handler) callerRoles(r *http.Request) []string {
	if h.CallerRoles == nil {
		return nil
	}
	return h.CallerRoles(r)
}

// capRoles 把请求角色收敛到调用者自身持有的角色集（求交）。
// 求交为空（含请求未指定）→ 返调用者自身角色（镜像，零提权）：自助密钥权限不超自身。
func capRoles(requested, owned []string) []string {
	ownedSet := make(map[string]bool, len(owned))
	for _, r := range owned {
		ownedSet[r] = true
	}
	var out []string
	for _, r := range requested {
		if ownedSet[r] {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return owned
	}
	return out
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
	id := pathID(r, "/api/admin/tenants/")
	if id == "" {
		httputil.WriteError(w, http.StatusBadRequest, "缺少 id")
		return
	}
	if err := h.repo.DeleteTenant(r.Context(), id); err != nil {
		if errors.Is(err, ErrTenantNotEmpty) {
			httputil.WriteError(w, http.StatusConflict, "租户下仍有用户，请先删除或转移用户后再删除租户")
			return
		}
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
	id := pathID(r, "/api/admin/users/")
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
	id := pathID(r, "/api/admin/users/")
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
	// 非 platform-admin（自助/tenant-admin）：先从 ctx 补全归属（自助场景前端无需传 tenantId/userId），
	// 再做必填校验，避免自助调用因缺字段 400。
	if !h.platformAdmin(r) {
		in.TenantID = callerTenant(r)
		if uid := h.callerUserID(r); uid != "" {
			in.UserID = uid
		}
	}
	if in.TenantID == "" || in.UserID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "tenantId/userId 必填")
		return
	}
	// roles 封顶：只能赋予自身持有角色（零提权：developer 无法创建 tenant-admin 密钥）。
	if !h.platformAdmin(r) {
		in.Roles = capRoles(in.Roles, h.callerRoles(r))
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
	// lastID 取末段，兼容 /api/admin/api-keys/{id}（超管）与 /api/api-keys/{id}（自助）两挂载点。
	id := lastID(r)
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

// pathID 从请求路径取末段 id（如 /api/admin/users/u-1 → u-1）。
func pathID(r *http.Request, prefix string) string {
	p := strings.TrimPrefix(r.URL.Path, prefix)
	// 去掉可能的子路径
	if i := strings.Index(p, "/"); i >= 0 {
		p = p[:i]
	}
	return p
}

// lastID 取路径末段非空 id（如 /api/api-keys/k-1 → k-1），不依赖固定前缀，
// 供同时挂在多个前缀下的资源（如 api-keys 自助 + 超管）使用。
func lastID(r *http.Request) string {
	p := strings.TrimRight(r.URL.Path, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
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
