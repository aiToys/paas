// 应用成员（应用级权限）的 PostgreSQL 实现。
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/internal/storage/pg"
)

// MemberStore 是 application.MemberRepository 的 PostgreSQL 实现。
type MemberStore struct {
	db *pg.DB
}

// NewMemberStore 创建应用成员 PG 仓储。db 必须已完成迁移（0032）。
func NewMemberStore(db *pg.DB) *MemberStore { return &MemberStore{db: db} } //nolint:gocritic // 与各 store NewStore 同款

const memberSelect = `SELECT m.id, m.tenant_id, m.app_id, m.user_id, u.name, m.role, m.created_at
FROM app_members m LEFT JOIN users u ON u.id = m.user_id AND u.tenant_id = m.tenant_id`

func scanMember(r pg.RowScanner) (*application.Member, error) {
	m := &application.Member{}
	if err := r.Scan(&m.ID, &m.TenantID, &m.AppID, &m.UserID, &m.UserName, &m.Role, &m.CreatedAt); err != nil {
		return nil, err
	}
	return m, nil
}

// ListMembers 列出应用全部成员（租户强制过滤；LEFT JOIN users 带出展示名）。
func (s *MemberStore) ListMembers(ctx context.Context, appID string) ([]application.Member, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Pool().Query(ctx, memberSelect+` WHERE m.app_id=$1 AND m.tenant_id=$2 ORDER BY m.user_id`, appID, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]application.Member, 0, 4)
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// ListAllMembers 跨租户列出全部成员（admin 总览）。LEFT JOIN users 带出展示名。
func (s *MemberStore) ListAllMembers(ctx context.Context) ([]application.Member, error) {
	rows, err := s.db.Pool().Query(ctx, memberSelect+` ORDER BY m.tenant_id, m.app_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]application.Member, 0, 8)
	for rows.Next() {
		m, err := scanMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// GetMember 取单个成员（appID+userID 唯一，租户过滤）。
func (s *MemberStore) GetMember(ctx context.Context, appID, userID string) (application.Member, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return application.Member{}, err
	}
	row := s.db.Pool().QueryRow(ctx, memberSelect+` WHERE m.app_id=$1 AND m.user_id=$2 AND m.tenant_id=$3`, appID, userID, tid)
	m, err := scanMember(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return application.Member{}, application.ErrMemberNotFound
		}
		return application.Member{}, err
	}
	return *m, nil
}

// AddMember 添加/覆盖成员角色（ON CONFLICT (app_id,user_id) DO UPDATE）。校验角色合法 + 应用归属本租户。
func (s *MemberStore) AddMember(ctx context.Context, m application.Member) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	if !application.ValidAppRole(m.Role) {
		return application.ErrInvalidRole
	}
	if m.ID == "" {
		m.ID = fmt.Sprintf("mb-%d", time.Now().UnixNano())
	}
	// 应用归属校验（防跨租户/不存在 appID 挂成员）。
	var hit bool
	if err := s.db.Pool().QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM applications WHERE id=$1 AND tenant_id=$2)`, m.AppID, tid).Scan(&hit); err != nil {
		return err
	}
	if !hit {
		return fmt.Errorf("应用不存在: %s", m.AppID)
	}
	_, err = s.db.Pool().Exec(ctx, `
INSERT INTO app_members (id, tenant_id, app_id, user_id, role, created_at)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (app_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
		m.ID, tid, m.AppID, m.UserID, m.Role, time.Now().UTC())
	return err
}

// RemoveMember 移除成员（租户过滤，不存在返 ErrMemberNotFound）。
func (s *MemberStore) RemoveMember(ctx context.Context, appID, userID string) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	ct, err := s.db.Pool().Exec(ctx,
		`DELETE FROM app_members WHERE app_id=$1 AND user_id=$2 AND tenant_id=$3`, appID, userID, tid)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return application.ErrMemberNotFound
	}
	return nil
}

// RemoveAppMembers 删应用时级联清成员（CASCADE 已设，显式清供内存路径同语义对齐）。
func (s *MemberStore) RemoveAppMembers(ctx context.Context, appID string) error {
	_, err := s.db.Pool().Exec(ctx, `DELETE FROM app_members WHERE app_id=$1`, appID)
	return err
}

// MemberRole 查用户在某应用的角色（无记录返 ""，不报错）。
func (s *MemberStore) MemberRole(ctx context.Context, appID, userID string) (string, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return "", err
	}
	var role string
	err = s.db.Pool().QueryRow(ctx,
		`SELECT role FROM app_members WHERE app_id=$1 AND user_id=$2 AND tenant_id=$3`, appID, userID, tid).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return role, err
}
