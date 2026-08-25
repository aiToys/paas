package application

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aitoys/paas/internal/httputil"
	adminutil "github.com/aitoys/paas/internal/web/admin"
)

// MemberHandler 暴露应用成员 REST API（应用级权限管理面）：
//
//	GET    /api/applications/{id}/members           成员列表
//	POST   /api/applications/{id}/members           添加/更新成员角色
//	DELETE /api/applications/{id}/members/{userID}  移除成员
//	GET    /api/applications/{id}/access            当前用户在本应用的角色与各动作权限（前端按钮显隐）
//
// 受限开关（restricted）经应用更新端点改（见 Handler.ServeHTTP 的 PUT /{id}/restrict）。
// 成员管理本身需应用 owner 或租户管理员（enforcement 经 AppGuard.AppActionManage）。
type MemberHandler struct {
	members MemberRepository
	apps    Repository
	// Authorize 租户级权限校验（application:write）；nil 跳过（测试）。
	Authorize func(r *http.Request, perm string) bool
	// Guard 应用级 enforcement（受限应用成员管理需 owner）；nil 跳过。
	Guard *AppGuard
	// UserLookup 校验用户存在并返回展示名（防悬挂引用）；nil 跳过校验。
	UserLookup func(ctx context.Context, userID string) (name string, ok bool)
	// Audit 审计记录器（成员/restrict 变更是高敏感权限操作，必须审计）；nil 跳过。
	Audit adminutil.AuditRecorder
	// ActorFn 从请求取操作者（审计用）；nil 则空。
	ActorFn func(r *http.Request) string
}

// audit 记审计（best-effort，失败不阻断）。
func (h *MemberHandler) audit(r *http.Request, tenantID, action, resourceID, detail string) {
	if h.Audit == nil {
		return
	}
	actor := ""
	if h.ActorFn != nil {
		actor = h.ActorFn(r)
	}
	_ = h.Audit.Record(r.Context(), tenantID, actor, action, "app_member", resourceID, detail)
}

// allowManage 成员管理权限：受限应用需 app-owner（Guard.manage）；非受限应用仅租户管理员
//（IsAdmin）——非受限时成员表无治理意义，若放行任意 application:write 持有者会形成
//「自封 owner → 开 restrict → 锁死他人」的权限提升链。
func (h *MemberHandler) allowManage(w http.ResponseWriter, r *http.Request, appID string) bool {
	a, err := h.apps.Get(r.Context(), appID)
	if err != nil {
		httputil.WriteServiceError(w, http.StatusNotFound, err)
		return false
	}
	if !a.Restricted {
		// 非受限：仅租户管理员可管成员（初始 owner 授予路径）。
		if h.Guard != nil && h.Guard.IsAdmin != nil && h.Guard.IsAdmin(r) {
			return true
		}
		httputil.WriteError(w, http.StatusForbidden, "forbidden: 非受限应用的成员管理需租户管理员")
		return false
	}
	// 受限：owner（或租户管理员，Guard 内通行）。
	if h.Guard != nil && h.Guard.Allow(r, appID, AppActionManage) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: 需应用所有者（app-owner）权限管理成员")
	return false
}

// NewMemberHandler 创建成员 API handler。
func NewMemberHandler(members MemberRepository, apps Repository) *MemberHandler {
	return &MemberHandler{members: members, apps: apps}
}

// ServeHTTP 分发成员子路由。path 形如 /api/applications/{id}/members[/{userID}]。
func (h *MemberHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/applications/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	// parts: [id, members, ...]
	if len(parts) < 2 || parts[1] != "members" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	appID := parts[0]

	if r.Method == http.MethodGet && len(parts) == 2 {
		ms, err := h.members.ListMembers(r.Context(), appID)
		if err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		httputil.WriteData(w, ms)
		return
	}

	// 写操作：租户级 application:write + 应用级 manage。
	// manage 语义：受限应用需 app-owner；非受限应用（尚无 owner 概念）仅租户管理员可操作——
	// 防权限提升链：developer 自封 owner 再开 restrict 锁死其他成员。
	if h.Authorize != nil && !h.Authorize(r, PermApplicationWrite) {
		httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+PermApplicationWrite)
		return
	}
	if !h.allowManage(w, r, appID) {
		return
	}

	switch {
	case r.Method == http.MethodPost && len(parts) == 2:
		var m Member
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil || m.UserID == "" {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body: userId 必填")
			return
		}
		if !ValidAppRole(m.Role) {
			httputil.WriteError(w, http.StatusBadRequest, "invalid role: "+m.Role)
			return
		}
		// 校验用户存在（防悬挂引用；UserLookup 未注入跳过）。
		if h.UserLookup != nil {
			if _, ok := h.UserLookup(r.Context(), m.UserID); !ok {
				httputil.WriteError(w, http.StatusBadRequest, "用户不存在: "+m.UserID)
				return
			}
		}
		m.AppID = appID
		if err := h.members.AddMember(r.Context(), m); err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, err)
			return
		}
		h.audit(r, m.TenantID, "app_member_add", appID, "user="+m.UserID+" role="+m.Role)
		out, err := h.members.GetMember(r.Context(), appID, m.UserID)
		if err != nil {
			httputil.WriteDataCreated(w, m)
			return
		}
		httputil.WriteDataCreated(w, out)

	case r.Method == http.MethodDelete && len(parts) == 3:
		if err := h.members.RemoveMember(r.Context(), appID, parts[2]); err != nil {
			httputil.WriteServiceError(w, http.StatusNotFound, err)
			return
		}
		h.audit(r, "", "app_member_remove", appID, "user="+parts[2])
		httputil.WriteData(w, map[string]string{"deleted": parts[2]})

	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}
