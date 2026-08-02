package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/aitoys/paas/internal/core/identity"
)

// LoginRequest 是 POST /api/auth/sessions 的请求体。
type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// AuthResult 是登录/刷新返回的 token 对。
type AuthResult struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int64  `json:"expiresIn"` // access token 有效期（秒）
}

// RefreshRequest 是 POST /api/auth/tokens/refresh 的请求体。
type RefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// UserProfile 对齐 console-admin 的 UserProfile 类型（lib/auth/types.ts）。
type UserProfile struct {
	ID          string   `json:"id"`
	Username    string   `json:"username"`
	Nickname    string   `json:"nickname,omitempty"`
	Avatar      string   `json:"avatar,omitempty"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

// Handler 是 /api/auth/* 与 /api/system/menus 的 HTTP 处理器。
// 各端点以方法形式暴露，由 main.go 挂到对应路由（login/refresh 公开，me/menus/logout 需 BearerAuth）。
type Handler struct {
	idb          identity.Repository
	secret       string
	cookieSecure bool
	limiter      *loginLimiter
}

// NewHandler 创建 auth handler。secret 为 JWT 签名密钥；cookieSecure 控制 session cookie 的
// Secure 位（HTTP 部署 false，配 TLS 后 true）。内部初始化登录限流器（per-IP+per-username）。
func NewHandler(idb identity.Repository, secret string, cookieSecure bool) *Handler {
	return &Handler{idb: idb, secret: secret, cookieSecure: cookieSecure, limiter: newLoginLimiter()}
}

// Login: POST /api/auth/sessions —— 用户名密码换 token 对。
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAuthErr(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeAuthErr(w, http.StatusBadRequest, "用户名与密码必填")
		return
	}
	ip := clientIP(r)
	if ok, retry := h.limiter.allow(ip, req.Username); !ok {
		writeAuthErr(w, http.StatusTooManyRequests,
			fmt.Sprintf("登录尝试过多，请 %d 秒后再试", int(retry.Seconds())+1))
		return
	}
	u, err := h.idb.GetUserByName(r.Context(), req.Username)
	if err != nil {
		h.limiter.recordFailure(ip, req.Username)
		writeAuthErr(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	// 先验密码（不存在/密码错统一 401，不泄漏账号存在性），再检查状态：
	// 密码已验证后提示禁用不增加枚举风险（攻击者需已知密码才能发现账号禁用）。
	if u.PasswordHash == "" || !CheckPassword(u.PasswordHash, req.Password) {
		h.limiter.recordFailure(ip, req.Username)
		writeAuthErr(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if u.Status != identity.StatusActive {
		writeAuthErr(w, http.StatusForbidden, "账号已禁用")
		return
	}
	h.limiter.recordSuccess(ip, req.Username)
	res, err := h.issueTokens(u)
	if err != nil {
		writeAuthErr(w, http.StatusInternalServerError, "签发 token 失败")
		return
	}
	setSessionCookies(w, res.AccessToken, res.RefreshToken, h.cookieSecure)
	writeAuthData(w, res)
}

// Refresh: POST /api/auth/tokens/refresh —— 凭 refresh token 换新 token 对。
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	// 优先读 refresh cookie（浏览器会话）；退化读 body（兼容 SDK 显式调用）。
	refreshToken, _ := refreshFromCookie(r)
	if refreshToken == "" {
		var req RefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err == nil {
			refreshToken = req.RefreshToken
		}
	}
	if refreshToken == "" {
		writeAuthErr(w, http.StatusUnauthorized, "missing refresh token")
		return
	}
	c, err := ParseType(refreshToken, h.secret, TokenRefresh)
	if err != nil {
		writeAuthErr(w, http.StatusUnauthorized, "refresh token 无效")
		return
	}
	// c.Sub 是 userID：按 ID 查实时用户，校验存在性 + 启用状态。
	// 不信任 token 内的 Roles 快照——用 DB 当前角色，使禁用/删除/降权即时生效
	// （旧实现误用 GetUserByName(userID) 必查失败、fallback 跳过状态校验，导致被封禁账号可无限续期）。
	u, err := h.idb.GetUser(r.Context(), c.Tenant, c.Sub)
	if err != nil {
		writeAuthErr(w, http.StatusUnauthorized, "用户不存在或已被移除")
		return
	}
	if u.Status != identity.StatusActive {
		writeAuthErr(w, http.StatusForbidden, "账号已禁用")
		return
	}
	res, err := h.issueTokens(u)
	if err != nil {
		writeAuthErr(w, http.StatusInternalServerError, "签发 token 失败")
		return
	}
	setSessionCookies(w, res.AccessToken, res.RefreshToken, h.cookieSecure)
	writeAuthData(w, res)
}

// Logout: DELETE /api/auth/sessions —— 无状态 JWT，仅返回成功（前端清 token）。
func (h *Handler) Logout(w http.ResponseWriter, _ *http.Request) {
	clearSessionCookies(w, h.cookieSecure)
	writeAuthData(w, map[string]any{})
}

// Me: GET /api/auth/users/me —— 当前用户信息（IsAdmin 映射为 super_admin 触发前端超管短路）。
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	tok, err := BearerToken(r)
	if err != nil {
		writeAuthErr(w, http.StatusUnauthorized, "missing bearer token")
		return
	}
	c, err := ParseType(tok, h.secret, TokenAccess)
	if err != nil {
		writeAuthErr(w, http.StatusUnauthorized, "invalid token")
		return
	}
	profile := UserProfile{
		ID:       c.Sub,
		Username: c.Sub,
		Roles:    c.Roles,
	}
	if u, err := h.idb.GetUser(r.Context(), c.Tenant, c.Sub); err == nil {
		profile.Username = u.Name
		if u.Email != "" {
			profile.Nickname = u.Name
		}
		if u.IsAdmin {
			profile.Roles = []string{"super_admin"}
		}
	}
	// super_admin 或 tenant-admin 给全权限通配（前端超管短路 / Require 放行）
	if isSuperAdmin(profile.Roles) {
		profile.Permissions = []string{"*"}
	} else {
		profile.Permissions = expandPermissions(profile.Roles)
	}
	writeAuthData(w, profile)
}

// Menus: GET /api/system/menus —— 初期静态菜单（对齐 admin 已有视图）。
// P0-3 再做按角色过滤 + PaaS 业务页菜单。
func (h *Handler) Menus(w http.ResponseWriter, _ *http.Request) {
	writeAuthData(w, staticMenus())
}

// issueTokens 签发 access + refresh。
// IsAdmin 用户额外携带 "super_admin" 标记角色：identity 管理 API 据此区分平台超管（跨租户）
// 与普通 tenant-admin（仅本租户），防止任意租户的 tenant-admin 越权管理其他租户。
func (h *Handler) issueTokens(u *identity.User) (AuthResult, error) {
	roles := u.Roles
	if u.IsAdmin {
		roles = appendSuperAdmin(roles)
	}
	now := time.Now()
	access, err := Sign(Claims{
		Sub: u.ID, Tenant: u.TenantID, Roles: roles, Typ: TokenAccess,
		Exp: now.Add(AccessTTL).Unix(),
	}, h.secret)
	if err != nil {
		return AuthResult{}, err
	}
	refresh, err := Sign(Claims{
		Sub: u.ID, Tenant: u.TenantID, Roles: roles, Typ: TokenRefresh,
		Exp: now.Add(RefreshTTL).Unix(),
	}, h.secret)
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{AccessToken: access, RefreshToken: refresh, ExpiresIn: int64(AccessTTL.Seconds())}, nil
}

// appendSuperAdmin 在不含 super_admin 时前置追加（去重，置首便于判定）。
func appendSuperAdmin(roles []string) []string {
	for _, r := range roles {
		if r == "super_admin" {
			return roles
		}
	}
	return append([]string{"super_admin"}, roles...)
}

// isSuperAdmin 判断角色集合是否含平台超管标识。
// admin 基座 isSuperAdmin = roles.includes('super_admin')；core 的 tenant-admin 视同超管。
func isSuperAdmin(roles []string) bool {
	for _, r := range roles {
		if r == "super_admin" || r == "tenant-admin" {
			return true
		}
	}
	return false
}

// expandPermissions 把 core 角色名展开为具体权限标识（供前端 v-permission 指令）。
func expandPermissions(roles []string) []string {
	builtin := identity.BuiltinRoles()
	seen := map[string]struct{}{}
	var out []string
	for _, name := range roles {
		r, ok := builtin[name]
		if !ok {
			continue
		}
		for _, p := range r.Permissions {
			if _, ok := seen[string(p)]; ok {
				continue
			}
			seen[string(p)] = struct{}{}
			out = append(out, string(p))
		}
	}
	return out
}

// —— 响应辅助（core 契约 {data:T}/{error:msg}）——

func writeAuthData(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": v})
}

func writeAuthErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}
