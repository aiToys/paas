// Package pg D5-4：预算 / 周期 token 用量 / 供应商对账的 PostgreSQL 实现。
// 与内存版同构：Validate 同源、租户过滤显式 WHERE、跨租户统一 ErrBudgetNotFound。
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/billing"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Debug：扫描顺序必须与列常量一致（加列务必三处同步：常量 / INSERT / scan）。
const budgetCols = `id, tenant_id, scope, app_id, monthly_amount, warn_ratio, enforce, note, created_at, updated_at`
const settlementCols = `id, vendor, period, amount, currency, invoice_no, note, recorded_by, created_at`

func scanBudget(row pgx.Row) (billing.Budget, error) {
	var b billing.Budget
	err := row.Scan(&b.ID, &b.TenantID, &b.Scope, &b.AppID, &b.MonthlyAmount,
		&b.WarnRatio, &b.Enforce, &b.Note, &b.CreatedAt, &b.UpdatedAt)
	return b, err
}

func (s *Store) listBudgetsWhere(ctx context.Context, where string, args ...any) ([]billing.Budget, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+budgetCols+` FROM billing_budgets `+where+` ORDER BY tenant_id, scope, app_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []billing.Budget{}
	for rows.Next() {
		b, serr := scanBudget(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) ListBudgets(ctx context.Context) ([]billing.Budget, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	return s.listBudgetsWhere(ctx, `WHERE tenant_id=$1`, tid)
}

func (s *Store) ListAllBudgets(ctx context.Context) ([]billing.Budget, error) {
	return s.listBudgetsWhere(ctx, ``)
}

func (s *Store) GetBudget(ctx context.Context, id string) (billing.Budget, error) {
	b, err := scanBudget(s.db.Pool().QueryRow(ctx,
		`SELECT `+budgetCols+` FROM billing_budgets WHERE id=$1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return billing.Budget{}, billing.ErrBudgetNotFound
		}
		return billing.Budget{}, err
	}
	return b, nil
}

func (s *Store) CreateBudget(ctx context.Context, b billing.Budget) (billing.Budget, error) {
	if err := b.Validate(); err != nil {
		return billing.Budget{}, err
	}
	if b.TenantID == "" {
		tid, err := storagepg.TenantOrErr(ctx)
		if err != nil {
			return billing.Budget{}, err
		}
		b.TenantID = tid
	}
	now := time.Now()
	b.ID = fmt.Sprintf("budget-%s-%d", b.TenantID, now.UnixNano())
	b.CreatedAt, b.UpdatedAt = now, now
	// 唯一索引 (tenant_id, scope, app_id) 兜底重复范围：并发下内存检查不可靠，由 DB 裁决。
	if _, err := s.db.Pool().Exec(ctx, `
INSERT INTO billing_budgets (id, tenant_id, scope, app_id, monthly_amount, warn_ratio, enforce, note, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		b.ID, b.TenantID, b.Scope, b.AppID, b.MonthlyAmount, b.WarnRatio, b.Enforce, b.Note, b.CreatedAt, b.UpdatedAt); err != nil {
		if storagepg.IsUniqueViolation(err) {
			return billing.Budget{}, fmt.Errorf("该范围已存在预算（同租户同应用只能有一条）")
		}
		return billing.Budget{}, err
	}
	return b, nil
}

func (s *Store) UpdateBudget(ctx context.Context, b billing.Budget) (billing.Budget, error) {
	if err := b.Validate(); err != nil {
		return billing.Budget{}, err
	}
	// 归属字段（tenant/scope/app）不可变：改归属应删除后新建。
	cur, err := scanBudget(s.db.Pool().QueryRow(ctx, `
UPDATE billing_budgets SET monthly_amount=$2, warn_ratio=$3, enforce=$4, note=$5, updated_at=now()
WHERE id=$1
RETURNING `+budgetCols, b.ID, b.MonthlyAmount, b.WarnRatio, b.Enforce, b.Note))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return billing.Budget{}, billing.ErrBudgetNotFound
		}
		return billing.Budget{}, err
	}
	return cur, nil
}

func (s *Store) DeleteBudget(ctx context.Context, id string) error {
	tag, err := s.db.Pool().Exec(ctx, `DELETE FROM billing_budgets WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return billing.ErrBudgetNotFound
	}
	return nil
}

// —— TokenUsage ——

func (s *Store) AddTokenUsageForTenant(tenantID, period, appID, model string, tokens int64) error {
	if tenantID == "" {
		return fmt.Errorf("缺少租户")
	}
	if tokens == 0 {
		return nil
	}
	// 原子累加：ON CONFLICT 同一主键行串行更新，并发推理不漏计。
	_, err := s.db.Pool().Exec(context.Background(), `
INSERT INTO token_usage (tenant_id, period, app_id, model, tokens, updated_at)
VALUES ($1,$2,$3,$4,$5,now())
ON CONFLICT (tenant_id, period, app_id, model)
DO UPDATE SET tokens = GREATEST(token_usage.tokens + EXCLUDED.tokens, 0), updated_at = now()`,
		tenantID, period, appID, model, tokens)
	return err
}

func (s *Store) listTokenUsage(ctx context.Context, where string, args ...any) ([]billing.TokenUsageRow, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT tenant_id, period, app_id, model, tokens FROM token_usage `+where+
			` ORDER BY tenant_id, period, app_id, model`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []billing.TokenUsageRow{}
	for rows.Next() {
		var r billing.TokenUsageRow
		if err := rows.Scan(&r.TenantID, &r.Period, &r.AppID, &r.Model, &r.Tokens); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ListTokenUsage(ctx context.Context, period string) ([]billing.TokenUsageRow, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	if period == "" {
		return s.listTokenUsage(ctx, `WHERE tenant_id=$1`, tid)
	}
	return s.listTokenUsage(ctx, `WHERE tenant_id=$1 AND period=$2`, tid, period)
}

func (s *Store) ListAllTokenUsage(ctx context.Context, period string) ([]billing.TokenUsageRow, error) {
	if period == "" {
		return s.listTokenUsage(ctx, ``)
	}
	return s.listTokenUsage(ctx, `WHERE period=$1`, period)
}

// —— Settlement ——

func scanSettlement(row pgx.Row) (billing.SettlementRecord, error) {
	var r billing.SettlementRecord
	err := row.Scan(&r.ID, &r.Vendor, &r.Period, &r.Amount, &r.Currency,
		&r.InvoiceNo, &r.Note, &r.RecordedBy, &r.CreatedAt)
	return r, err
}

func (s *Store) ListSettlements(ctx context.Context, period string) ([]billing.SettlementRecord, error) {
	q := `SELECT ` + settlementCols + ` FROM vendor_settlements `
	args := []any{}
	if period != "" {
		q += `WHERE period=$1 `
		args = append(args, period)
	}
	q += `ORDER BY period DESC, vendor`
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []billing.SettlementRecord{}
	for rows.Next() {
		r, serr := scanSettlement(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) CreateSettlement(ctx context.Context, rec billing.SettlementRecord) (billing.SettlementRecord, error) {
	now := time.Now()
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = now
	}
	rec.ID = fmt.Sprintf("stl-%d", now.UnixNano())
	_, err := s.db.Pool().Exec(ctx, `
INSERT INTO vendor_settlements (id, vendor, period, amount, currency, invoice_no, note, recorded_by, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		rec.ID, rec.Vendor, rec.Period, rec.Amount, rec.Currency, rec.InvoiceNo, rec.Note, rec.RecordedBy, rec.CreatedAt)
	if err != nil {
		return billing.SettlementRecord{}, err
	}
	return rec, nil
}

func (s *Store) DeleteSettlement(ctx context.Context, id string) error {
	tag, err := s.db.Pool().Exec(ctx, `DELETE FROM vendor_settlements WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("对账记录不存在: %s", id)
	}
	return nil
}
