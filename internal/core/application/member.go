package application

import (
	"context"
	"time"
)

// 应用内角色（应用级权限粒度，对齐 GitHub Repo 角色 model）。
// 与租户级角色（identity.BuiltinRoles）正交：租户角色管「能干什么类型的事」，
// 应用角色管「能在哪个应用干什么事」。restricted 应用写操作需成员角色匹配。
const (
	// AppRoleOwner 应用所有者：全部权限 + 成员管理（受限应用唯一可管成员的角色）。
	AppRoleOwner = "app-owner"
	// AppRoleMaintainer 维护者：可发布/回滚/部署/审批（生产动作），不可管成员。
	AppRoleMaintainer = "app-maintainer"
	// AppRoleDeveloper 开发者：可构建/开发/改配置，**不可发布/回滚/审批**（测试人员典型角色）。
	AppRoleDeveloper = "app-developer"
	// AppRoleViewer 只读成员。
	AppRoleViewer = "app-viewer"
)

// AppRoles 返回全部合法应用内角色（校验 + 前端下拉用）。
func AppRoles() []string {
	return []string{AppRoleOwner, AppRoleMaintainer, AppRoleDeveloper, AppRoleViewer}
}

// ValidAppRole 判断是否合法应用内角色。
func ValidAppRole(r string) bool {
	switch r {
	case AppRoleOwner, AppRoleMaintainer, AppRoleDeveloper, AppRoleViewer:
		return true
	}
	return false
}

// AppAction 是应用级动作标识（enforcement 校验粒度）。
const (
	// AppActionManage 成员管理 + 应用设置/删除（owner 专属）。
	AppActionManage = "manage"
	// AppActionRelease 发布/回滚/promote/审批（maintainer 及以上）。
	AppActionRelease = "release"
	// AppActionWrite 开发态写：构建/流水线触发/工作负载写/绑定/配置（developer 及以上）。
	AppActionWrite = "write"
)

// roleGrants 应用内角色对动作的授权矩阵。owner 通行。
func roleGrants(role, action string) bool {
	if role == AppRoleOwner {
		return true
	}
	switch action {
	case AppActionRelease:
		return role == AppRoleMaintainer
	case AppActionWrite:
		return role == AppRoleMaintainer || role == AppRoleDeveloper
	case AppActionManage:
		return false
	}
	return false
}

// Member 是应用成员授权（租户内用户 × 应用 × 应用内角色）。
type Member struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId,omitempty"` // 由 Repository 从 ctx 写入
	AppID     string    `json:"appId"`
	UserID    string    `json:"userId"`
	UserName  string    `json:"userName,omitempty"` // 冗余展示名（join 派生，非真源）
	Role      string    `json:"role"`               // AppRole* 常量
	CreatedAt time.Time `json:"createdAt,omitempty"`
}

// MemberRepository 是应用成员持久化抽象。
type MemberRepository interface {
	// ListMembers 列出应用全部成员（按 AppID；租户强制过滤）。
	ListMembers(ctx context.Context, appID string) ([]Member, error)
	// ListAllMembers 跨租户列出全部成员（admin 总览）。
	ListAllMembers(ctx context.Context) ([]Member, error)
	// GetMember 取单个成员（appID+userID 唯一）。
	GetMember(ctx context.Context, appID, userID string) (Member, error)
	// AddMember 添加/覆盖成员角色（同 app+user 视为更新）。
	AddMember(ctx context.Context, m Member) error
	// RemoveMember 移除成员。
	RemoveMember(ctx context.Context, appID, userID string) error
	// RemoveAppMembers 删应用时级联清成员（store 删除路径调用）。
	RemoveAppMembers(ctx context.Context, appID string) error
	// MemberRole 查用户在某应用的角色（无成员记录返 ""，不报错——降级路径用）。
	MemberRole(ctx context.Context, appID, userID string) (string, error)
}

// 哨兵错误。
var (
	ErrMemberNotFound = errMemberNotFound{}
	ErrInvalidRole    = errInvalidRole{}
)

type errMemberNotFound struct{}

func (errMemberNotFound) Error() string { return "成员不存在" }

type errInvalidRole struct{}

func (errInvalidRole) Error() string { return "无效的应用内角色" }

// MemberAllowed 判定用户对某应用的某动作是否有权（应用级 enforcement 核心）。
//
// 语义（受限应用 restricted=true 时生效）：
//   - 租户管理员（tenant:admin）与平台超管通行（租户信任边界内全权）。
//   - 其余用户需是应用成员且角色授权该动作；非成员 -> 拒绝（fail-closed）。
//   - isAdmin/isPlatformAdmin 由调用方注入（依赖倒置，本包不依赖 gateway）。
func MemberAllowed(role string, action string) bool {
	return roleGrants(role, action)
}
