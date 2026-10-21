// Package memory D5-4：预算 / 周期 token 用量 / 供应商对账的内存实现。
// 与 pg 实现同构（Validate 同源、租户过滤语义一致、排序稳定）。
package memory

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/aitoys/paas/internal/billing"
	"github.com/aitoys/paas/pkg/tenant"
)

// tokenKey 是 token 用量行的内存索引键（与 PG 主键 (tenant_id, period, app_id, model) 同构）。
func tokenKey(tenantID, period, appID, model string) string {
	return tenantID + "|" + period + "|" + appID + "|" + model
}

// —— Budget ——

func (s *Store) ListBudgets(ctx context.Context) ([]billing.Budget, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]billing.Budget, 0, len(s.budgets))
	for _, b := range s.budgets {
		if b.TenantID == tid {
			out = append(out, b)
		}
	}
	sortBudgets(out)
	return out, nil
}

func (s *Store) ListAllBudgets(context.Context) ([]billing.Budget, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]billing.Budget, 0, len(s.budgets))
	for _, b := range s.budgets {
		out = append(out, b)
	}
	sortBudgets(out)
	return out, nil
}

func (s *Store) GetBudget(_ context.Context, id string) (billing.Budget, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.budgets[id]
	if !ok {
		return billing.Budget{}, billing.ErrBudgetNotFound
	}
	return b, nil
}

// CreateBudget 新建预算；同租户同范围重复（tenant 级唯一 / app 级按 appId 唯一）拒绝。
// 重复预算会让「哪条在生效」变得不可判定（enforce 与否产生歧义），故直接 409。
func (s *Store) CreateBudget(ctx context.Context, b billing.Budget) (billing.Budget, error) {
	if err := b.Validate(); err != nil {
		return billing.Budget{}, err
	}
	if b.TenantID == "" {
		tid, err := tenant.IDOrErr(ctx)
		if err != nil {
			return billing.Budget{}, err
		}
		b.TenantID = tid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.budgets {
		if ex.TenantID == b.TenantID && ex.Scope == b.Scope && ex.AppID == b.AppID {
			return billing.Budget{}, fmt.Errorf("该范围已存在预算（同租户同应用只能有一条）")
		}
	}
	s.seq++
	now := time.Now()
	b.ID = fmt.Sprintf("budget-%d", s.seq)
	b.CreatedAt, b.UpdatedAt = now, now
	s.budgets[b.ID] = b
	return b, nil
}

func (s *Store) UpdateBudget(_ context.Context, b billing.Budget) (billing.Budget, error) {
	if err := b.Validate(); err != nil {
		return billing.Budget{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.budgets[b.ID]
	if !ok {
		return billing.Budget{}, billing.ErrBudgetNotFound
	}
	// ID/TenantID/Scope/AppID 不可变：改归属等于换一条预算，应删除后新建（与 PG 一致）。
	cur.MonthlyAmount = b.MonthlyAmount
	cur.WarnRatio = b.WarnRatio
	cur.Enforce = b.Enforce
	cur.Note = b.Note
	cur.UpdatedAt = time.Now()
	s.budgets[b.ID] = cur
	return cur, nil
}

func (s *Store) DeleteBudget(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.budgets[id]; !ok {
		return billing.ErrBudgetNotFound
	}
	delete(s.budgets, id)
	return nil
}

func sortBudgets(bs []billing.Budget) {
	sort.Slice(bs, func(i, j int) bool {
		if bs[i].TenantID != bs[j].TenantID {
			return bs[i].TenantID < bs[j].TenantID
		}
		if bs[i].Scope != bs[j].Scope {
			return bs[i].Scope < bs[j].Scope
		}
		return bs[i].AppID < bs[j].AppID
	})
}

// —— TokenUsage ——

// AddTokenUsageForTenant 供装配层在无租户 ctx 下显式记账（meter 回调自带 tenantID）。
func (s *Store) AddTokenUsageForTenant(tenantID, period, appID, model string, tokens int64) error {
	if tenantID == "" {
		return fmt.Errorf("缺少租户")
	}
	return s.addTokenUsage(tenantID, period, appID, model, tokens)
}

func (s *Store) addTokenUsage(tid, period, appID, model string, tokens int64) error {
	if tokens == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := tokenKey(tid, period, appID, model)
	row := s.tokens[k]
	row.TenantID, row.Period, row.AppID, row.Model = tid, period, appID, model
	row.Tokens += tokens
	if row.Tokens < 0 {
		row.Tokens = 0 // 计数不可为负（负 delta 只用于测试/纠正）
	}
	s.tokens[k] = row
	return nil
}

func (s *Store) ListTokenUsage(ctx context.Context, period string) ([]billing.TokenUsageRow, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, _ := s.listTokenUsage("", period)
	out := make([]billing.TokenUsageRow, 0, len(rows))
	for _, r := range rows {
		if r.TenantID == tid {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *Store) ListAllTokenUsage(_ context.Context, period string) ([]billing.TokenUsageRow, error) {
	return s.listTokenUsage("", period)
}

func (s *Store) listTokenUsage(_ string, period string) ([]billing.TokenUsageRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]billing.TokenUsageRow, 0, len(s.tokens))
	for _, r := range s.tokens {
		if period != "" && r.Period != period {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.TenantID != b.TenantID {
			return a.TenantID < b.TenantID
		}
		if a.Period != b.Period {
			return a.Period < b.Period
		}
		if a.AppID != b.AppID {
			return a.AppID < b.AppID
		}
		return a.Model < b.Model
	})
	return out, nil
}

// —— Settlement ——

func (s *Store) ListSettlements(_ context.Context, period string) ([]billing.SettlementRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]billing.SettlementRecord, 0, len(s.settlements))
	for _, r := range s.settlements {
		if period != "" && r.Period != period {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Period != out[j].Period {
			return out[i].Period > out[j].Period // 新周期在前
		}
		return out[i].Vendor < out[j].Vendor
	})
	return out, nil
}

func (s *Store) CreateSettlement(_ context.Context, rec billing.SettlementRecord) (billing.SettlementRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	rec.ID = fmt.Sprintf("stl-%d", s.seq)
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	s.settlements = append(s.settlements, rec)
	return rec, nil
}

func (s *Store) DeleteSettlement(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.settlements {
		if r.ID == id {
			s.settlements = append(s.settlements[:i], s.settlements[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("对账记录不存在: %s", id)
}
