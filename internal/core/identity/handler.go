package identity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
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
	// auditRec 审计记录器（依赖倒置，cmd/core 桥接 security.AuditStore）。未注入则不记审计。
	// identity 写操作（签发/吊销密钥、增删用户/租户）属高敏感操作，必须进审计日志（合规：审计只增不删）。
	auditRec AuditRecorder
	// IsPlatformAdmin 判定调用者是否平台超管（main.go 注入 gateway.IsPlatformAdmin）。
	// 平台超管可跨租户管理；普通 tenant-admin 仅限本租户（防越权）。
	IsPlatformAdmin func(*http.Request) bool
	// PlatformAuthorize 判定调用者是否持某平台权限（D5-2 细分角色；main.go 注入
	// gateway.RolesFromRequest + identity.PlatformGrantsAny 桥接）。未注入时各端点
	// 仅 super_admin 可达（与 D5-2 之前行为一致，fail-closed）。
	// 用途：guard 层特例（如 SecurityWrite 冻结租户、IdentityRead 租户列表）需要
	// handler 层同源放行——只改 guard 不改 handler 会造出「菜单可见/过 guard 但 403」死路径。
	PlatformAuthorize func(*http.Request, Permission) bool
	// CallerUserID/CallerRoles 取调用者用户 ID 与角色集（main.go 注入 gateway.UserIDFrom/RolesFrom）。
	// 自助 API Key 端点据此绑定密钥归属 + roles 封顶（只能赋予自身持有的角色，零提权）。
	// 未注入时返空（UserID 空则保留 body 值，roles 求交为空 → 返空集），保守安全。
	CallerUserID func(*http.Request) string
	CallerRoles  func(*http.Request) []string
	// offboarding 统计租户名下资源（注销预检）。identity 不依赖业务域，由 cmd/core 装配层
	// 跨域聚合后注入。未注入时预检端点 fail-visible 报 503——「0 项」与「未统计」不可混同。
	offboarding OffboardingCounter
}

// OffboardingItem 是注销预检的一项资源统计。
// Blocking=true 表示该项存在时删除租户会被拒（当前仅 users，仓库层非空保护）；
// 其余为提示项：冻结不清理数据面，删除前需显式处理。
type OffboardingItem struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Count    int    `json:"count"`
	Blocking bool   `json:"blocking"`
	Note     string `json:"note,omitempty"`
}

// OffboardingCounter 跨域统计租户资源（依赖倒置，cmd/core 注入）。
type OffboardingCounter func(ctx context.Context, tenantID string) ([]OffboardingItem, error)

// OffboardingReport 是注销预检结果（只读；不触发任何删除）。
type OffboardingReport struct {
	TenantID string            `json:"tenantId"`
	Status   string            `json:"status"`
	Items    []OffboardingItem `json:"items"`
	Blocking bool              `json:"blocking"` // 是否存在阻塞删除的项
	Notes    []string          `json:"notes"`
}

// WithOffboardingCounter 注入注销预检统计器（链式，可选）。
func (h *Handler) WithOffboardingCounter(fn OffboardingCounter) *Handler {
	h.offboarding = fn
	return h
}

// AuditRecorder 审计记录器（依赖倒置，避免 identity->security 反向依赖）。
// cmd/core 桥接 security.AuditStore 实现注入；未注入则不记审计。
type AuditRecorder interface {
	Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error
}

// NewHandler 创建 identity 管理 handler。
func NewHandler(repo Repository) *Handler { return &Handler{repo: repo} }

// WithAudit 注入审计记录器（链式，可选）。
func (h *Handler) WithAudit(a AuditRecorder) *Handler { h.auditRec = a; return h }

// audit 写操作成功后记审计（best-effort，错误不影响主流程；actor 取调用者 UserID）。
// tenant 取 ctx（操作上下文 = actor 所属租户）；超管跨租户操作且 ctx 无 tenant 时归 "platform"
// （audit_logs.tenant_id NOT NULL）。
func (h *Handler) audit(r *http.Request, action, resourceType, resourceID, detail string) {
	if h.auditRec == nil {
		return
	}
	tid, _ := tenant.TenantFrom(r.Context())
	if tid == "" {
		tid = "platform"
	}
	_ = h.auditRec.Record(r.Context(), tid, h.callerUserID(r), action, resourceType, resourceID, detail)
}

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

// platformAllowed 超管或持指定平台权限（D5-2 细分角色）皆可。
// guard 层（cmd/core adminGuard）已按 (method, path) 精确控权，此处是 handler 内第二道
// 粗粒度检查——两个判定必须同源，否则出现「过 guard 但 403」的规则死路径（R5 审计 C1）。
func (h *Handler) platformAllowed(r *http.Request, perm Permission) bool {
	if h.platformAdmin(r) {
		return true
	}
	return h.PlatformAuthorize != nil && h.PlatformAuthorize(r, perm)
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
	// 租户枚举为平台级能力：超管或平台身份只读角色可访问，普通 tenant-admin 仅能感知本租户。
	if !h.platformAllowed(r, PermPlatformIdentityRead) {
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
		httputil.WriteServiceError(w, http.StatusConflict, err)
		return
	}
	h.audit(r, "create", "tenant", in.ID, "创建租户 "+in.Name)
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
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	h.audit(r, "delete", "tenant", id, "删除租户 "+id)
	httputil.WriteData(w, map[string]string{"deleted": id})
}

// —— 租户冻结 / 注销预检（D5）——

// tenantStatusInput 冻结/恢复请求体。冻结必填原因（后台操作必须可追溯）。
type tenantStatusInput struct {
	Reason string `json:"reason"`
}

func (h *Handler) FreezeTenant(w http.ResponseWriter, r *http.Request) {
	h.setTenantStatus(w, r, TenantStatusFrozen)
}

func (h *Handler) UnfreezeTenant(w http.ResponseWriter, r *http.Request) {
	h.setTenantStatus(w, r, TenantStatusActive)
}

// setTenantStatus 冻结/恢复共用实现：平台超管专属，冻结必须给出原因。
// 只改控制面准入状态，不触碰数据面已部署业务（控制面与数据面解耦）。
func (h *Handler) setTenantStatus(w http.ResponseWriter, r *http.Request, status string) {
	// 冻结/恢复属安全处置：超管或平台安全写角色（platformDomains freeze 特例同源）。
	if !h.platformAllowed(r, PermPlatformSecurityWrite) {
		httputil.WriteError(w, http.StatusForbidden, "需要平台管理员权限")
		return
	}
	id := pathID(r, "/api/admin/tenants/")
	if id == "" {
		httputil.WriteError(w, http.StatusBadRequest, "缺少 id")
		return
	}
	var in tenantStatusInput
	// 恢复允许空体；空体解码出 io.EOF 不视为格式错误。
	if err := decodeBody(r, &in); err != nil && !errors.Is(err, io.EOF) {
		httputil.WriteError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if status == TenantStatusFrozen && in.Reason == "" {
		httputil.WriteError(w, http.StatusBadRequest, "冻结租户必须填写原因")
		return
	}
	t, err := h.repo.SetTenantStatus(r.Context(), id, status, in.Reason)
	if err != nil {
		if errors.Is(err, ErrTenantNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "租户不存在")
			return
		}
		httputil.WriteServiceError(w, http.StatusInternalServerError, err)
		return
	}
	action, detail := "unfreeze", "恢复租户 "+id
	if status == TenantStatusFrozen {
		action, detail = "freeze", "冻结租户 "+id+"，原因："+in.Reason
	} else if in.Reason != "" {
		detail += "，原因：" + in.Reason
	}
	h.audit(r, action, "tenant", id, detail)
	httputil.WriteData(w, t)
}

// TenantOffboarding 注销预检：只读列出租户名下资源与阻塞项，不触发任何删除。
func (h *Handler) TenantOffboarding(w http.ResponseWriter, r *http.Request) {
	if !h.platformAdmin(r) {
		httputil.WriteError(w, http.StatusForbidden, "需要平台管理员权限")
		return
	}
	id := pathID(r, "/api/admin/tenants/")
	if id == "" {
		httputil.WriteError(w, http.StatusBadRequest, "缺少 id")
		return
	}
	t, err := h.repo.GetTenant(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "租户不存在")
		return
	}
	if h.offboarding == nil {
		// 返回空清单会让人误以为「没有资源」，宁可报错。
		httputil.WriteError(w, http.StatusServiceUnavailable, "注销预检未接入资源统计，无法给出可信清单")
		return
	}
	items, err := h.offboarding(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusInternalServerError, err)
		return
	}
	if items == nil {
		items = []OffboardingItem{}
	}
	report := OffboardingReport{
		TenantID: id,
		Status:   t.Status,
		Items:    items,
		Notes: []string{
			"冻结只关闭控制面准入（登录 / API Key），数据面已部署业务继续运行。",
			"删除租户需先清空阻塞项；其余资源请按提示逐项下线，平台不代删数据面实例。",
		},
	}
	for _, it := range items {
		if it.Blocking && it.Count > 0 {
			report.Blocking = true
		}
	}
	httputil.WriteData(w, report)
}

// —— Users ——

// ListTenantUsers 列出本租户用户（租户自助，任意已认证用户；供应用成员选择器等场景）。
// 与 ListUsers（admin 跨租户）互补：强制限定调用者租户，不回传 password_hash。
// tenant ctx 缺失时拒绝（ListUsers("") 会返回全租户，禁止透传空串）。
func (h *Handler) ListTenantUsers(w http.ResponseWriter, r *http.Request) {
	tid := callerTenant(r)
	if tid == "" {
		httputil.WriteError(w, http.StatusForbidden, "missing tenant context")
		return
	}
	us, err := h.repo.ListUsers(r.Context(), tid)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	for i := range us {
		us[i].PasswordHash = ""
	}
	httputil.WriteData(w, us)
}

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
				httputil.WriteServiceError(w, http.StatusBadRequest, err)
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
		httputil.WriteServiceError(w, http.StatusConflict, err)
		return
	}
	h.audit(r, "create", "user", u.ID, "创建用户 "+u.ID)
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
	// 平台超管跨租户定位（tenantID 空）。取旧值用于判断本次改动是否需要撤销既有会话。
	lookupTenant := ""
	if !h.platformAdmin(r) {
		lookupTenant = callerTenant(r)
	}
	old, err := h.repo.GetUser(r.Context(), lookupTenant, id)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "用户不存在")
		return
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
				httputil.WriteServiceError(w, http.StatusBadRequest, err)
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
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	h.audit(r, "update", "user", id, "更新用户 "+id)
	// 敏感变更（密码/状态/角色/超管标记）撤销既有会话：被改的凭据不应继续有效。
	// 角色变更按「集合相等」判断，前端重复提交同一组角色不误判。
	resp := map[string]any{"id": id}
	if sensitiveUserChange(*old, u, in.Status) {
		ver, rerr := h.repo.RevokeUserSessions(r.Context(), lookupTenant, id)
		if rerr != nil {
			// 更新已落库：明确告知「已更新但撤销失败」，引导重试，不谎报成功也不谎报未生效。
			httputil.WriteServiceError(w, http.StatusInternalServerError,
				fmt.Errorf("用户已更新，但撤销既有会话失败（请重试）：%w", rerr))
			return
		}
		h.audit(r, "revoke_sessions", "user", id, fmt.Sprintf("敏感变更自动撤销会话（session_version=%d）", ver))
		resp["sessionVersion"] = ver
	}
	httputil.WriteData(w, resp)
}

// sensitiveUserChange 判断本次更新是否触及凭证/权限语义（需撤销已签发会话）。
// Roles 为 nil 表示「不更新角色」（PG 语义），不算变更。
func sensitiveUserChange(old User, next User, statusIn string) bool {
	if next.PasswordHash != "" {
		return true
	}
	if statusIn != "" && statusIn != old.Status {
		return true
	}
	if next.IsAdmin != old.IsAdmin {
		return true
	}
	if next.Roles == nil {
		return false
	}
	oldSet := roleSet(old.Roles)
	nextSet := roleSet(next.Roles)
	if len(oldSet) != len(nextSet) {
		return true
	}
	for r := range nextSet {
		if _, ok := oldSet[r]; !ok {
			return true
		}
	}
	return false
}

func roleSet(roles []string) map[string]struct{} {
	set := make(map[string]struct{}, len(roles))
	for _, r := range roles {
		set[r] = struct{}{}
	}
	return set
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
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	h.audit(r, "delete", "user", id, "删除用户 "+id)
	httputil.WriteData(w, map[string]string{"deleted": id})
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
	// 列表掩码明文 key（不泄漏）+ 回填派生状态（前端不重复实现到期判定）
	now := time.Now()
	for i := range ks {
		ks[i].Key = maskKey(ks[i].Key)
		ks[i].State = ks[i].KeyState(now)
	}
	httputil.WriteData(w, ks)
}

type apiKeyInput struct {
	ID       string   `json:"id"`
	TenantID string   `json:"tenantId"`
	UserID   string   `json:"userId"`
	AppID    string   `json:"appId"` // 可选：绑定应用则用量归因到应用（模型推理计费）
	Roles    []string `json:"roles"`
	// ExpiresAt 可选：RFC3339 到期时间；空=永不过期（D5 生命周期）。
	ExpiresAt string `json:"expiresAt"`
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
		// 应用级 Key 只能由模型绑定流程在后端创建（归属天然正确）；
		// 自助端点忽略前端传入的 appId，防越权绑定他人应用归因。
		in.AppID = ""
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
	var expires *time.Time
	if in.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, in.ExpiresAt)
		if err != nil || !parsed.After(time.Now()) {
			httputil.WriteError(w, http.StatusBadRequest, "expiresAt 需为晚于当前时间的 RFC3339 时间")
			return
		}
		expires = &parsed
	}
	key := "sk-" + randHex(20)
	k := APIKey{
		ID: in.ID, TenantID: in.TenantID, UserID: in.UserID,
		AppID: in.AppID, Roles: in.Roles, Key: key, ExpiresAt: expires, CreatedAt: time.Now(),
	}
	if err := h.repo.CreateAPIKey(r.Context(), k); err != nil {
		httputil.WriteServiceError(w, http.StatusConflict, err)
		return
	}
	h.audit(r, "create", "api_key", k.ID, "签发 API 密钥 "+k.ID)
	httputil.WriteData(w, k) // 创建时返明文一次
}

func (h *Handler) DeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	// lastID 取末段，兼容 /api/admin/api-keys/{id}（超管）与 /api/api-keys/{id}（自助）两挂载点。
	id := lastID(r)
	if id == "" {
		httputil.WriteError(w, http.StatusBadRequest, "缺少 id")
		return
	}
	// 目标 Key 必须可写（本租户 + 管理员或本人；跨租户 404 不泄漏存在性）。
	if _, ok := h.apiKeyWritable(w, r, id); !ok {
		return
	}
	if err := h.repo.DeleteAPIKey(r.Context(), id); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	h.audit(r, "delete", "api_key", id, "吊销 API 密钥 "+id)
	httputil.WriteData(w, map[string]string{"deleted": id})
}

// RevokeUserSessions: POST /api/admin/users/{id}/sessions/revoke —— 撤销该用户已签发的全部会话。
// 平台管理动作：改密之外的显式「踢下线」（如凭证疑似泄漏）。已签发 access token 在网关
// 代次缓存窗口（≤30s）后失效，refresh token 立即拒绝续期。
func (h *Handler) RevokeUserSessions(w http.ResponseWriter, r *http.Request) {
	id := pathID(r, "/api/admin/users/")
	if id == "" {
		httputil.WriteError(w, http.StatusBadRequest, "缺少 id")
		return
	}
	// 普通 tenant-admin 限本租户（跨租户定位返回 404，不泄漏存在性）；平台超管跨租户。
	lookupTenant := ""
	if !h.platformAdmin(r) {
		lookupTenant = callerTenant(r)
	}
	ver, err := h.repo.RevokeUserSessions(r.Context(), lookupTenant, id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	h.audit(r, "revoke_sessions", "user", id, fmt.Sprintf("显式撤销用户会话（session_version=%d）", ver))
	httputil.WriteData(w, map[string]any{"userId": id, "sessionVersion": ver})
}

// apiKeyIDFromPath 取 API Key id，兼容 /api/admin/api-keys/{id}[/...] 与 /api/api-keys/{id}[/...] 两挂载点。
func apiKeyIDFromPath(r *http.Request) string {
	for _, prefix := range []string{"/api/admin/api-keys/", "/api/api-keys/"} {
		if strings.HasPrefix(r.URL.Path, prefix) {
			return pathID(r, prefix)
		}
	}
	return ""
}

// RevokeAPIKey: POST /api/(admin/)api-keys/{id}/revoke —— 软吊销（保留记录供审计）。
// 与 DELETE 的区别：DELETE 移除记录，REVOKE 保留「这个 Key 存在过」的证据。重复吊销幂等。
func (h *Handler) RevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id := apiKeyIDFromPath(r)
	if id == "" {
		httputil.WriteError(w, http.StatusBadRequest, "缺少 id")
		return
	}
	k, ok := h.apiKeyWritable(w, r, id)
	if !ok {
		return
	}
	if k.RevokedAt == nil {
		now := time.Now()
		if err := h.repo.RevokeAPIKey(r.Context(), id, now); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		h.audit(r, "revoke", "api_key", id, "吊销 API 密钥 "+id)
		k.RevokedAt = &now
	}
	k.Key = maskKey(k.Key)
	k.State = k.KeyState(time.Now())
	httputil.WriteData(w, k)
}

type apiKeyExpiryInput struct {
	// ExpiresAt 为 RFC3339 时间；空串 = 取消到期（永不过期）。
	ExpiresAt string `json:"expiresAt"`
}

// SetAPIKeyExpiry: PUT /api/(admin/)api-keys/{id}/expiry —— 设置/取消到期时间。
// 拒绝过去时间：立即失效请走吊销（两种语义在审计上必须可区分）。
func (h *Handler) SetAPIKeyExpiry(w http.ResponseWriter, r *http.Request) {
	id := apiKeyIDFromPath(r)
	if id == "" {
		httputil.WriteError(w, http.StatusBadRequest, "缺少 id")
		return
	}
	if _, ok := h.apiKeyWritable(w, r, id); !ok {
		return
	}
	var in apiKeyExpiryInput
	if err := decodeBody(r, &in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "请求体格式错误")
		return
	}
	var at time.Time
	if in.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, in.ExpiresAt)
		if err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "expiresAt 需为 RFC3339 时间")
			return
		}
		if !parsed.After(time.Now()) {
			httputil.WriteError(w, http.StatusBadRequest, "到期时间必须晚于当前时间（如需立即失效请使用吊销）")
			return
		}
		at = parsed
	}
	if err := h.repo.SetAPIKeyExpiry(r.Context(), id, at); err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	h.audit(r, "set_expiry", "api_key", id, "设置 API 密钥到期时间: "+in.ExpiresAt)
	k, err := h.repo.GetAPIKey(r.Context(), id)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return
	}
	k.Key = maskKey(k.Key)
	k.State = k.KeyState(time.Now())
	httputil.WriteData(w, k)
}

type apiKeyRotateInput struct {
	// ExpiresAt 可选：新 Key 的到期时间（RFC3339）。缺省=不设到期。
	// 故意不继承旧 Key 到期时间：轮换通常发生在到期前，继承只会把期限顺延到同一个日期。
	ExpiresAt string `json:"expiresAt"`
}

// RotateAPIKey: POST /api/(admin/)api-keys/{id}/rotate —— 轮换密钥。
// 新建同租户/用户/应用/角色的 Key（明文只返一次），旧 Key 立即软吊销（保留审计链）。
func (h *Handler) RotateAPIKey(w http.ResponseWriter, r *http.Request) {
	oldID := apiKeyIDFromPath(r)
	if oldID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "缺少 id")
		return
	}
	old, ok := h.apiKeyWritable(w, r, oldID)
	if !ok {
		return
	}
	var in apiKeyRotateInput
	// body 可空（不设到期）；空体不视为错误。
	_ = json.NewDecoder(r.Body).Decode(&in)
	// 已吊销/已到期的 Key 不再轮换：那等于用新凭据复活一个已停用的入口，
	// 语义上应是「新建 Key」而不是「轮换」。
	if err := old.Usable(time.Now()); err != nil {
		httputil.WriteError(w, http.StatusConflict, "旧 Key 已不可用（吊销或到期），请直接新建 Key")
		return
	}
	var expires *time.Time
	if in.ExpiresAt != "" {
		parsed, perr := time.Parse(time.RFC3339, in.ExpiresAt)
		if perr != nil {
			httputil.WriteError(w, http.StatusBadRequest, "expiresAt 需为 RFC3339 时间")
			return
		}
		if !parsed.After(time.Now()) {
			httputil.WriteError(w, http.StatusBadRequest, "到期时间必须晚于当前时间")
			return
		}
		expires = &parsed
	}
	now := time.Now()
	nk := APIKey{
		ID: "k-" + randHex(8), TenantID: old.TenantID, UserID: old.UserID, AppID: old.AppID,
		Roles: old.Roles, Key: "sk-" + randHex(20), ExpiresAt: expires,
		RotatedFrom: old.ID, CreatedAt: now,
	}
	if err := h.repo.CreateAPIKey(r.Context(), nk); err != nil {
		httputil.WriteServiceError(w, http.StatusConflict, err)
		return
	}
	// 新 Key 落库后再吊销旧的：失败窗口里两个 Key 同时可用（可重试收敛），
	// 反向顺序则会出现「旧的已废、新的没建」的不可用窗口。
	if err := h.repo.RevokeAPIKey(r.Context(), old.ID, now); err != nil {
		h.audit(r, "rotate_partial", "api_key", old.ID, "轮换已建新 Key 但旧 Key 吊销失败，请手动吊销: "+nk.ID)
		httputil.WriteServiceError(w, http.StatusInternalServerError,
			fmt.Errorf("新 Key 已创建（%s），但旧 Key 吊销失败，请手动吊销旧 Key：%w", nk.ID, err))
		return
	}
	h.audit(r, "rotate", "api_key", nk.ID, "轮换 API 密钥，旧 Key "+old.ID)
	httputil.WriteData(w, nk) // 明文只返一次
}

// apiKeyWritable 校验调用者对目标 Key 的写权限（删除/吊销/到期/轮换共用）。
// 平台超管放行；持 tenant:admin 者限本租户；其余用户仅限自己名下的 Key。
//
// 为什么必须限「自己名下」：轮换会返回新 Key 的明文，而新 Key 继承旧 Key 的
// 租户/用户/角色——若允许同租户任意成员轮换管理员的 Key，等于直接拿到管理员凭证（提权链）。
// 跨租户一律 404（不泄漏存在性）；同租户越权 403（对调用者已知 ID 如实反馈）。
func (h *Handler) apiKeyWritable(w http.ResponseWriter, r *http.Request, id string) (*APIKey, bool) {
	k, err := h.repo.GetAPIKey(r.Context(), id)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "API Key 不存在")
		return nil, false
	}
	if h.platformAdmin(r) {
		return &k, true
	}
	if k.TenantID != callerTenant(r) {
		httputil.WriteError(w, http.StatusNotFound, "API Key 不存在")
		return nil, false
	}
	if h.isTenantAdmin(r) {
		return &k, true
	}
	if uid := h.callerUserID(r); uid != "" && k.UserID == uid {
		return &k, true
	}
	httputil.WriteError(w, http.StatusForbidden, "只能管理自己创建的 API Key")
	return nil, false
}

// isTenantAdmin 判断调用者是否持有 tenant-admin 角色（本租户内通行）。
func (h *Handler) isTenantAdmin(r *http.Request) bool {
	if h.CallerRoles == nil {
		return false
	}
	for _, role := range h.CallerRoles(r) {
		if role == "tenant-admin" {
			return true
		}
	}
	return false
}

// —— Roles（只读）——

// roleView 是角色列表项（含权限）。
type roleView struct {
	Name        string       `json:"name"`
	Permissions []Permission `json:"permissions"`
}

// ListRoles 列出租户域内置角色 + 平台管理角色（D5-2）。
// 平台角色只在 admin 侧可见，租户成员选择器用的是前端自有枚举，不读此端点。
func (h *Handler) ListRoles(w http.ResponseWriter, r *http.Request) {
	all := make(map[string]Role, len(BuiltinRoles())+len(PlatformRoles()))
	for name, role := range BuiltinRoles() {
		all[name] = role
	}
	for name, role := range PlatformRoles() {
		all[name] = role
	}
	out := make([]roleView, 0, len(all))
	for _, name := range sortedRoleNames(all) {
		r := all[name]
		out = append(out, roleView{Name: r.Name, Permissions: r.Permissions}) //nolint:staticcheck // S1016: Role 字段多于 roleView，显式字面量比整型转换更清晰
	}
	httputil.WriteData(w, out)
}

// sortedRoleNames 稳定排序角色名，避免 map 迭代顺序导致前端列表抖动。
func sortedRoleNames(m map[string]Role) []string {
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
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
