// Package pg 提供 security.Repository 的 PostgreSQL 实现。
// 显式 WHERE tenant_id 多租户过滤（与内存 1:1）；平台级 Secret TenantID 写 NULL，
// ListSecrets 用 `scope='tenant' AND tenant_id=$1 OR scope='platform'` 全租户可见平台资产。
// Secret 值后端明文存储，List/Get/Create 返回掩码（不泄漏长度/内容）；
// 仅 Resolve 平台级路径返明文（供第三方供应商通道运行时解析）。
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/security"
	secmemory "github.com/aitoys/paas/internal/security/memory"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Store 是 security.Repository 的 PostgreSQL 实现（SecretStore + AuditStore 单 Store）。
type Store struct {
	db *storagepg.DB
}

// NewStore 创建 security PG 仓储。db 必须已完成迁移。
func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

// secCols 与 model.Secret 字段顺序对齐（scan 列顺序必须一致）。
// desc 是 PG 保留字，需双引号。
const secCols = `id, tenant_id, name, type, scope, value, "desc", updated_at`

// scanSec 通过 storagepg.RowScanner 抽象 QueryRow 与 Row 两种 Scan 来源。
// tenant_id 为 NULL（平台级）时扫入 *string，由调用方按 NULL 处理。
func scanSec(r storagepg.RowScanner, s *security.Secret) error {
	var tenantID *string
	if err := r.Scan(&s.ID, &tenantID, &s.Name, &s.Type, &s.Scope, &s.Value, &s.Desc, &s.UpdatedAt); err != nil {
		return err
	}
	if tenantID != nil {
		s.TenantID = *tenantID
	}
	return nil
}

// —— Secret ——

// ListSecrets 返回「该租户的租户级 Secret」+「所有平台级 Secret」（均掩码）。
// 平台级凭证全租户共享（第三方供应商 Key），故 OR scope='platform' 不过滤租户。
// 按 name 升序。
func (s *Store) ListSecrets(ctx context.Context) ([]security.Secret, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	// 平台级 tenant_id 为 NULL，必须 OR scope='platform' 单独取（不能依赖 tenant_id 匹配）。
	q := `SELECT ` + secCols + ` FROM secrets
	      WHERE (scope = 'tenant' AND tenant_id = $1)
	         OR scope = 'platform'
	      ORDER BY name`
	rows, err := s.db.Pool().Query(ctx, q, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]security.Secret, 0)
	for rows.Next() {
		var sec security.Secret
		if err = scanSec(rows, &sec); err != nil {
			return nil, err
		}
		out = append(out, sec.Masked()) // 全部掩码，含平台级
	}
	return out, rows.Err()
}

// GetSecret 按 ID 取（掩码）；租户级跨租户访问 RowsAffected==0 → not found 不泄漏，
// 平台级跨租户可读（与内存一致）。
func (s *Store) GetSecret(ctx context.Context, id string) (security.Secret, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return security.Secret{}, err
	}
	q := `SELECT ` + secCols + ` FROM secrets
	      WHERE id = $1 AND (scope = 'platform' OR tenant_id = $2)`
	row := s.db.Pool().QueryRow(ctx, q, id, tid)
	var sec security.Secret
	if err = scanSec(row, &sec); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return security.Secret{}, fmt.Errorf("密钥不存在: %s", id)
		}
		return security.Secret{}, err
	}
	return sec.Masked(), nil
}

// CreateSecret 明文存储，返回掩码。tenant 级以 ctx 租户写 tenant_id；
// platform 级 tenant_id 写 NULL（全租户共享）。唯一冲突（两个 partial unique index 之一）
// → 与内存同款「已存在」消息。
func (s *Store) CreateSecret(ctx context.Context, sec security.Secret) (security.Secret, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return security.Secret{}, err
	}
	if err := sec.Validate(); err != nil {
		return security.Secret{}, err
	}
	// 平台级无租户归属；tenant 级以 ctx 为准（忽略请求体，防越权写）。
	var tenantArg any
	if sec.Scope == security.ScopePlatform {
		tenantArg = nil
		sec.TenantID = ""
	} else {
		tenantArg = tid
		sec.TenantID = tid
	}
	row := s.db.Pool().QueryRow(ctx, `
INSERT INTO secrets (id, tenant_id, name, type, scope, value, "desc", updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING `+secCols,
		sec.ID, tenantArg, sec.Name, sec.Type, sec.Scope, sec.Value, sec.Desc, sec.UpdatedAt,
	)
	var saved security.Secret
	if err = scanSec(row, &saved); err != nil {
		// 两个 partial unique index 之一冲突 → SQLSTATE 23505。
		if storagepg.IsUniqueViolation(err) {
			if sec.Scope == security.ScopePlatform {
				return security.Secret{}, fmt.Errorf("平台级密钥名已存在: %s", sec.Name)
			}
			return security.Secret{}, fmt.Errorf("密钥名已存在: %s", sec.Name)
		}
		return security.Secret{}, err
	}
	return saved.Masked(), nil
}

// DeleteSecret 删除指定密钥。仅可删本租户的 tenant 级 或 任一平台级（与内存可见性一致）。
// 跨租户访问 tenant 级 → RowsAffected==0 → not found 不泄漏。
func (s *Store) DeleteSecret(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM secrets WHERE id = $1 AND (scope = 'platform' OR tenant_id = $2)`,
		id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("密钥不存在: %s", id)
	}
	return nil
}

// Resolve 按 ID 取**平台级** Secret 明文（供第三方供应商通道运行时解析 API Key）。
// 租户级 Secret 经此路径返回 not found（防绕过掩码读明文）。
// 不强制 ctx 租户（平台级全租户共享，与内存一致；MaaS 运行时调用方不一定带租户）。
func (s *Store) Resolve(ctx context.Context, id string) (security.Secret, error) {
	// 注意：仅取 scope='platform' 行；tenant 级即便 id 匹配也当 not found。
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+secCols+` FROM secrets WHERE id = $1 AND scope = 'platform'`, id)
	var sec security.Secret
	if err := scanSec(row, &sec); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return security.Secret{}, fmt.Errorf("平台级密钥不存在: %s", id)
		}
		return security.Secret{}, err
	}
	return sec, nil // 明文（仅内存传给 Provider，不掩码）
}

// —— Audit ——

// auditCols 与 model.AuditLog 字段顺序对齐。
const auditCols = `id, tenant_id, actor, action, resource_type, resource_id, detail, at`

func scanAudit(r storagepg.RowScanner, l *security.AuditLog) error {
	return r.Scan(&l.ID, &l.TenantID, &l.Actor, &l.Action, &l.ResourceType, &l.ResourceID, &l.Detail, &l.At)
}

// ListAuditLogs 审计查询，resourceType/action 为空表示不限；按时间倒序（最新在前）。
// 全方法强制 tenant 过滤（审计按租户隔离，无平台级共享）。
func (s *Store) ListAuditLogs(ctx context.Context, resourceType, action string) ([]security.AuditLog, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + auditCols + ` FROM audit_logs WHERE tenant_id = $1`
	args := []any{tid}
	if resourceType != "" {
		args = append(args, resourceType)
		q += fmt.Sprintf(" AND resource_type = $%d", len(args))
	}
	if action != "" {
		args = append(args, action)
		q += fmt.Sprintf(" AND action = $%d", len(args))
	}
	q += " ORDER BY at DESC"
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]security.AuditLog, 0)
	for rows.Next() {
		var l security.AuditLog
		if err = scanAudit(rows, &l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// RecordAudit INSERT 一条审计；actor 由调用方注入（handler 从身份 ctx 取 UserID）。
// tenant_id 以 ctx 为准（忽略请求体，防越权写）。只增不删（合规）。
func (s *Store) RecordAudit(ctx context.Context, log security.AuditLog) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	log.TenantID = tid
	if log.At.IsZero() {
		log.At = time.Now()
	}
	_, err = s.db.Pool().Exec(ctx, `
INSERT INTO audit_logs (id, tenant_id, actor, action, resource_type, resource_id, detail, at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		log.ID, log.TenantID, log.Actor, log.Action, log.ResourceType, log.ResourceID, log.Detail, log.At,
	)
	return err
}

// SecretsCount 返回全表密钥数，供 PG seed 判空（表空才灌，幂等）。
// 注意：不经租户过滤，仅启动期 seed 判空用，不暴露给业务层。
func (s *Store) SecretsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM secrets`).Scan(&n)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	return n, nil
}

// AuditsCount 返回全表审计记录数，供 PG seed 判空（表空才灌，幂等）。
// 注意：不经租户过滤，仅启动期 seed 判空用，不暴露给业务层。
func (s *Store) AuditsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs`).Scan(&n)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	return n, nil
}

// SeedIfEmpty 在表空时灌入 seed 真源（启动期用，幂等）。
//
// 直接 SQL INSERT 绕过 CreateSecret（要求 ctx tenant）与 RecordAudit（自动盖时间戳），
// 平台级 Secret（ScopePlatform, TenantID=""）写 NULL tenant_id（与 CreateSecret 一致），
// 保留 seed 的固定 ID/Actor/时间。主键冲突 DO NOTHING。不经租户过滤，全表一次灌完。
func (s *Store) SeedIfEmpty(ctx context.Context) error {
	n, err := s.SecretsCount(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	for _, sec := range secmemory.SeedSecrets() {
		// 平台级写 NULL tenant_id（与 CreateSecret 一致，全租户可见）。
		var tenantArg any
		if sec.Scope == security.ScopePlatform {
			tenantArg = nil
		} else {
			tenantArg = sec.TenantID
		}
		if _, err = s.db.Pool().Exec(ctx,
			`INSERT INTO secrets (id, tenant_id, name, type, scope, value, "desc", updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			 ON CONFLICT (id) DO NOTHING`,
			sec.ID, tenantArg, sec.Name, sec.Type, sec.Scope, sec.Value, sec.Desc, sec.UpdatedAt); err != nil {
			return err
		}
	}
	for _, a := range secmemory.SeedAuditLogs() {
		if _, err = s.db.Pool().Exec(ctx,
			`INSERT INTO audit_logs (id, tenant_id, actor, action, resource_type, resource_id, detail, at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			 ON CONFLICT (id) DO NOTHING`,
			a.ID, a.TenantID, a.Actor, a.Action, a.ResourceType, a.ResourceID, a.Detail, a.At); err != nil {
			return err
		}
	}
	return nil
}
