// Package pg 提供 identity.Repository 的 PostgreSQL 实现。
// 显式 WHERE tenant_id=$1 过滤（与内存实现 1:1，RLS 留后续）；缺失租户上下文即拒。
// 多值字段（User.Roles / APIKey.Roles）以行存于 *_roles 子表，读时聚合。
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/core/identity"
	"github.com/aitoys/paas/internal/storage/pg"
)

// Store 是 identity.Repository 的 PostgreSQL 实现。
type Store struct {
	db *pg.DB
}

// NewStore 创建 identity PG 仓储。db 必须已完成迁移。
func NewStore(db *pg.DB) *Store { return &Store{db: db} }

// —— Tenant ——

func (s *Store) CreateTenant(ctx context.Context, t identity.Tenant) error {
	_, err := s.db.Pool().Exec(ctx,
		`INSERT INTO tenants (id, name, created_at) VALUES ($1, $2, $3)`,
		t.ID, t.Name, t.CreatedAt)
	if pg.IsUniqueViolation(err) {
		return fmt.Errorf("租户%w: %s", pg.ErrAlreadyExists, t.ID)
	}
	return err
}

func (s *Store) GetTenant(ctx context.Context, id string) (identity.Tenant, error) {
	row := s.db.Pool().QueryRow(ctx,
		`SELECT id, name, created_at FROM tenants WHERE id=$1`, id)
	var t identity.Tenant
	if err := row.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.Tenant{}, fmt.Errorf("租户不存在: %s", id)
		}
		return identity.Tenant{}, err
	}
	return t, nil
}

// —— User ——

func (s *Store) CreateUser(ctx context.Context, u identity.User) error {
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // 已提交时无副作用

	if _, err = tx.Exec(ctx,
		`INSERT INTO users (id, tenant_id, name, email, password_hash, is_admin, status, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		u.ID, u.TenantID, u.Name, u.Email, u.PasswordHash, u.IsAdmin,
		defaultStatus(u.Status), time.Now()); err != nil {
		if pg.IsUniqueViolation(err) {
			return fmt.Errorf("用户%w: %s", pg.ErrAlreadyExists, u.ID)
		}
		return err
	}
	for _, r := range u.Roles {
		if _, err = tx.Exec(ctx,
			`INSERT INTO user_roles (user_id, role) VALUES ($1, $2)`, u.ID, r); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// userColumns 是 users 表的统一读取列（CreateUser 写入字段对齐）。
const userColumns = "id, tenant_id, name, email, password_hash, is_admin, status, created_at"

func defaultStatus(s string) string {
	if s == "" {
		return identity.StatusActive
	}
	return s
}

// scanUser 把一行扫描为 User（角色需另行聚合）。
func scanUser(row pgx.Row) (identity.User, error) {
	var u identity.User
	err := row.Scan(&u.ID, &u.TenantID, &u.Name, &u.Email, &u.PasswordHash, &u.IsAdmin, &u.Status, &u.CreatedAt)
	return u, err
}

func (s *Store) UsersByTenant(ctx context.Context, tenantID string) ([]identity.User, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+userColumns+` FROM users WHERE tenant_id=$1 ORDER BY id`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []identity.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// 批量聚合角色（消除 N+1：一次查询加载全部用户角色，而非逐用户查询）。
	ids := make([]string, len(out))
	for i := range out {
		ids[i] = out[i].ID
	}
	rolesMap, err := s.usersRolesBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Roles = rolesMap[out[i].ID]
	}
	return out, nil
}

// GetUserByName 按登录用户名查（全局唯一）；聚合角色。找不到返回错误。
func (s *Store) GetUserByName(ctx context.Context, name string) (*identity.User, error) {
	u, err := scanUser(s.db.Pool().QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE name=$1`, name))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("用户不存在: %s", name)
		}
		return nil, err
	}
	u.Roles, err = s.userRoles(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUser 取单个用户（租户内隔离）；聚合角色。
func (s *Store) GetUser(ctx context.Context, tenantID, userID string) (*identity.User, error) {
	u, err := scanUser(s.db.Pool().QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE tenant_id=$1 AND id=$2`, tenantID, userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("用户不存在: %s", userID)
		}
		return nil, err
	}
	u.Roles, err = s.userRoles(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (s *Store) userRoles(ctx context.Context, userID string) ([]string, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT role FROM user_roles WHERE user_id=$1 ORDER BY role`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		roles = append(roles, r)
	}
	return roles, rows.Err()
}

// usersRolesBatch 一次性加载多个用户的角色（消除 N+1：List 列表场景下避免每用户一次查询）。
// 返回 userID -> roles 映射；不存在的用户对应 nil 切片。
func (s *Store) usersRolesBatch(ctx context.Context, userIDs []string) (map[string][]string, error) {
	out := make(map[string][]string, len(userIDs))
	if len(userIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT user_id, role FROM user_roles WHERE user_id = ANY($1) ORDER BY user_id, role`, userIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var uid, r string
		if err := rows.Scan(&uid, &r); err != nil {
			return nil, err
		}
		out[uid] = append(out[uid], r)
	}
	return out, rows.Err()
}

// —— APIKey ——

func (s *Store) CreateAPIKey(ctx context.Context, k identity.APIKey) error {
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err = tx.Exec(ctx,
		`INSERT INTO api_keys (id, tenant_id, user_id, app_id, key, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		k.ID, k.TenantID, k.UserID, k.AppID, k.Key, k.CreatedAt); err != nil {
		if pg.IsUniqueViolation(err) {
			return fmt.Errorf("API Key%w", pg.ErrAlreadyExists)
		}
		return err
	}
	for _, r := range k.Roles {
		if _, err = tx.Exec(ctx,
			`INSERT INTO api_key_roles (api_key_id, role) VALUES ($1, $2)`, k.ID, r); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// LookupAPIKey 按 bearer key 解析 (租户, 用户, 角色)。找不到返回错误（不泄漏存在性）。
func (s *Store) LookupAPIKey(ctx context.Context, key string) (identity.APIKey, error) {
	row := s.db.Pool().QueryRow(ctx,
		`SELECT id, tenant_id, user_id, app_id, created_at FROM api_keys WHERE key=$1`, key)
	var k identity.APIKey
	k.Key = key
	// app_id 可空（旧租户级 Key 行 app_id=NULL，migration 0004 增量加列），用 *string 接 NULL。
	var appID *string
	if err := row.Scan(&k.ID, &k.TenantID, &k.UserID, &appID, &k.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.APIKey{}, fmt.Errorf("API Key 无效")
		}
		return identity.APIKey{}, err
	}
	if appID != nil {
		k.AppID = *appID
	}
	roles, err := s.apiKeyRoles(ctx, k.ID)
	if err != nil {
		return identity.APIKey{}, err
	}
	k.Roles = roles
	return k, nil
}

func (s *Store) apiKeyRoles(ctx context.Context, apiKeyID string) ([]string, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT role FROM api_key_roles WHERE api_key_id=$1 ORDER BY role`, apiKeyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			return nil, err
		}
		roles = append(roles, r)
	}
	return roles, rows.Err()
}

// apiKeysRolesBatch 一次性加载多个 API Key 的角色（消除 N+1：List 列表场景）。
func (s *Store) apiKeysRolesBatch(ctx context.Context, keyIDs []string) (map[string][]string, error) {
	out := make(map[string][]string, len(keyIDs))
	if len(keyIDs) == 0 {
		return out, nil
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT api_key_id, role FROM api_key_roles WHERE api_key_id = ANY($1) ORDER BY api_key_id, role`, keyIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kid, r string
		if err := rows.Scan(&kid, &r); err != nil {
			return nil, err
		}
		out[kid] = append(out[kid], r)
	}
	return out, rows.Err()
}

// TenantsCount 返回租户总数，供 seed 判空（表空才灌，幂等）。
func (s *Store) TenantsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM tenants`).Scan(&n)
	return n, err
}

// —— 平台级管理方法（跨租户；handler 强制 tenant:admin）——

func (s *Store) ListTenants(ctx context.Context) ([]identity.Tenant, error) {
	rows, err := s.db.Pool().Query(ctx, `SELECT id, name, created_at FROM tenants ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []identity.Tenant
	for rows.Next() {
		var t identity.Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteTenant(ctx context.Context, id string) error {
	// 单事务 + FOR UPDATE 锁 tenants 行：使「检查非空 + 删除」原子。
	// 并发 CreateUser 的 INSERT users 会对 tenants 行加 FK KEY SHARE 锁，与本事务的 FOR UPDATE 冲突
	// -> 并发建用户等待删除提交后 FK 校验失败（而非"成功后被 CASCADE 静默删"）。
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT true FROM tenants WHERE id=$1 FOR UPDATE`, id).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("租户不存在: %s", id)
		}
		return err
	}
	// 非空保护：有用户拒绝，引导先清用户（防孤儿 + 防误删）
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM users WHERE tenant_id=$1`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: %s", identity.ErrTenantNotEmpty, id)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM tenants WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx) // api_keys FK CASCADE 自动清
}

// ListUsers tenantID 空则全租户。
func (s *Store) ListUsers(ctx context.Context, tenantID string) ([]identity.User, error) {
	q := `SELECT ` + userColumns + ` FROM users`
	args := []any{}
	if tenantID != "" {
		q += ` WHERE tenant_id=$1`
		args = append(args, tenantID)
	}
	q += ` ORDER BY tenant_id, id`
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []identity.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 批量聚合角色（消除 N+1）。
	ids := make([]string, len(out))
	for i := range out {
		ids[i] = out[i].ID
	}
	rolesMap, err := s.usersRolesBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Roles = rolesMap[out[i].ID]
	}
	return out, nil
}

// UpdateUser 改 name/email/is_admin/status + roles（删旧插新）。PasswordHash 非空则一并更新密码。
func (s *Store) UpdateUser(ctx context.Context, u identity.User) error {
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if u.PasswordHash != "" {
		ct, e := tx.Exec(ctx,
			`UPDATE users SET name=$1, email=$2, is_admin=$3, status=$4, password_hash=$5 WHERE id=$6`,
			u.Name, u.Email, u.IsAdmin, defaultStatus(u.Status), u.PasswordHash, u.ID)
		if e != nil {
			return e
		}
		if ct.RowsAffected() == 0 {
			return fmt.Errorf("用户不存在: %s", u.ID)
		}
	} else {
		ct, e := tx.Exec(ctx,
			`UPDATE users SET name=$1, email=$2, is_admin=$3, status=$4 WHERE id=$5`,
			u.Name, u.Email, u.IsAdmin, defaultStatus(u.Status), u.ID)
		if e != nil {
			return e
		}
		if ct.RowsAffected() == 0 {
			return fmt.Errorf("用户不存在: %s", u.ID)
		}
	}
	if u.Roles != nil { // 显式传 roles 才更新
		if _, err := tx.Exec(ctx, `DELETE FROM user_roles WHERE user_id=$1`, u.ID); err != nil {
			return err
		}
		for _, r := range u.Roles {
			if _, err := tx.Exec(ctx, `INSERT INTO user_roles (user_id, role) VALUES ($1, $2)`, u.ID, r); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteUser(ctx context.Context, tenantID, userID string) error {
	q := `DELETE FROM users WHERE id=$1`
	args := []any{userID}
	if tenantID != "" {
		q += ` AND tenant_id=$2`
		args = append(args, tenantID)
	}
	ct, err := s.db.Pool().Exec(ctx, q, args...)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("用户不存在: %s", userID)
	}
	return nil // user_roles FK CASCADE
}

// ListAPIKeys tenantID 空则全租户。
func (s *Store) ListAPIKeys(ctx context.Context, tenantID string) ([]identity.APIKey, error) {
	q := `SELECT id, tenant_id, user_id, app_id, key, created_at FROM api_keys`
	args := []any{}
	if tenantID != "" {
		q += ` WHERE tenant_id=$1`
		args = append(args, tenantID)
	}
	q += ` ORDER BY tenant_id, id`
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []identity.APIKey
	for rows.Next() {
		var k identity.APIKey
		// app_id 可空（旧租户级 Key 行），用 *string 接 NULL。
		var appID *string
		if err := rows.Scan(&k.ID, &k.TenantID, &k.UserID, &appID, &k.Key, &k.CreatedAt); err != nil {
			return nil, err
		}
		if appID != nil {
			k.AppID = *appID
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 批量聚合角色（消除 N+1）。
	ids := make([]string, len(out))
	for i := range out {
		ids[i] = out[i].ID
	}
	rolesMap, err := s.apiKeysRolesBatch(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Roles = rolesMap[out[i].ID]
	}
	return out, nil
}

func (s *Store) DeleteAPIKey(ctx context.Context, id string) error {
	ct, err := s.db.Pool().Exec(ctx, `DELETE FROM api_keys WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("API Key 不存在: %s", id)
	}
	return nil // api_key_roles FK CASCADE
}
