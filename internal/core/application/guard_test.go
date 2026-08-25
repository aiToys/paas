package application

import (
	"testing"
)

// TestMemberAllowedMatrix 验证应用内角色 × 动作授权矩阵（应用级权限核心语义）。
func TestMemberAllowedMatrix(t *testing.T) {
	cases := []struct {
		role, action string
		want         bool
	}{
		{AppRoleOwner, AppActionManage, true},
		{AppRoleOwner, AppActionRelease, true},
		{AppRoleOwner, AppActionWrite, true},
		{AppRoleMaintainer, AppActionManage, false},
		{AppRoleMaintainer, AppActionRelease, true},
		{AppRoleMaintainer, AppActionWrite, true},
		{AppRoleDeveloper, AppActionManage, false},
		{AppRoleDeveloper, AppActionRelease, false}, // 测试人员不可发布——核心诉求
		{AppRoleDeveloper, AppActionWrite, true},
		{AppRoleViewer, AppActionManage, false},
		{AppRoleViewer, AppActionRelease, false},
		{AppRoleViewer, AppActionWrite, false},
		{"", AppActionWrite, false}, // 非成员 fail-closed
		{"", AppActionRelease, false},
	}
	for _, c := range cases {
		if got := MemberAllowed(c.role, c.action); got != c.want {
			t.Errorf("MemberAllowed(%q,%q)=%v want %v", c.role, c.action, got, c.want)
		}
	}
}

// TestValidAppRole 校验角色常量。
func TestValidAppRole(t *testing.T) {
	for _, r := range AppRoles() {
		if !ValidAppRole(r) {
			t.Errorf("AppRoles() 含非法角色 %q", r)
		}
	}
	if ValidAppRole("tenant-admin") || ValidAppRole("") {
		t.Error("租户级角色/空串不应是合法应用角色")
	}
}
