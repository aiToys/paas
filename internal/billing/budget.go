// Package billing D5-4 财务深化：预算/限额、按周期 token 计量、供应商结算对账。
//
// 与既有「配额 + 用量快照 + 账单」的关系：
//   - 配额（ResourceQuota）拦「资源数量」，预算（Budget）管「费用金额」；
//   - 既有 ResourceUsage.Counts 是**当前快照**（无周期维度），此前 token 计数只增不减，
//     无法支撑月度预算——本文件追加 TokenUsageRow（按 租户/周期/应用/模型 聚合），
//     meter 每次推理回写，周期取记账时刻的 YYYY-MM；
//   - 费用一律是**估算**：平台未拆分 prompt/completion，按模型输出单价 × token 计；
//     未定价模型计入 tokens 但成本记 0，并在 UnpricedModels 中如实列出（不假装已计价）。
package billing

import (
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"time"
)

// 预算范围。
const (
	BudgetScopeTenant = "tenant"
	BudgetScopeApp    = "app"
)

// 预算状态（派生，不落库）。
const (
	BudgetOK       = "ok"
	BudgetWarn     = "warn"
	BudgetExceeded = "exceeded"
)

// DefaultWarnRatio 是默认预警比例（达到预算 80% 转 warn）。
const DefaultWarnRatio = 0.8

// PeriodOf 返回记账周期（YYYY-MM，UTC）。
// 用 UTC 而非本地时区：周期边界必须与存储/聚合一致，本地时区会让同一时刻在不同副本
// 落到不同月份（容器时区不保证）。
func PeriodOf(t time.Time) string { return t.UTC().Format("2006-01") }

// ErrBudgetNotFound 预算不存在（跨租户统一 not found，不泄漏存在性）。
var ErrBudgetNotFound = errors.New("预算不存在")

// Budget 月度预算（金额上限），范围=租户或单个应用。
//
// Enforce=true 时网关在推理入口检查：当期已超预算则拒绝（429），查询失败 fail-open
// （预算服务不可用不应连带整个推理链路不可用）。
type Budget struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenantId,omitempty"` // ctx 写入；admin 代建时显式指定
	Scope         string    `json:"scope"`              // tenant | app
	AppID         string    `json:"appId,omitempty"`    // scope=app 时必填
	MonthlyAmount float64   `json:"monthlyAmount"`      // 每月上限（元）
	WarnRatio     float64   `json:"warnRatio"`          // 0<r<=1；0 视为 DefaultWarnRatio
	Enforce       bool      `json:"enforce"`            // 超预算是否拒绝推理
	Note          string    `json:"note,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Validate 校验预算字段（memory/PG 双实现同源调用）。
func (b Budget) Validate() error {
	switch b.Scope {
	case BudgetScopeTenant:
		if b.AppID != "" {
			return errors.New("scope=tenant 时不应指定 appId")
		}
	case BudgetScopeApp:
		if b.AppID == "" {
			return errors.New("scope=app 时必须指定 appId")
		}
	default:
		return fmt.Errorf("无效的 scope: %s（支持 tenant/app）", b.Scope)
	}
	if b.MonthlyAmount <= 0 {
		return errors.New("monthlyAmount 必须大于 0")
	}
	if b.WarnRatio < 0 || b.WarnRatio > 1 {
		return errors.New("warnRatio 取值区间为 (0,1]")
	}
	return nil
}

// TokenUsageRow 是 (租户, 周期, 应用, 模型) 维度的 token 用量。
// AppID 空 = 租户级 Key（无法归因到应用）。
type TokenUsageRow struct {
	TenantID string `json:"tenantId,omitempty"`
	Period   string `json:"period"` // YYYY-MM
	AppID    string `json:"appId,omitempty"`
	Model    string `json:"model"`
	Tokens   int64  `json:"tokens"`
}

// ModelPrice 是模型的计费单价查询器（元 / 百万 token）。
// 由装配层桥接 maas 目录注入；返回 ok=false 表示该模型未定价。
type ModelPrice func(modelID string) (yuanPerMillion float64, ok bool)

// ModelVendor 是模型 → 供应商的归属查询器（供应商结算对账用）。
type ModelVendor func(modelID string) (vendor string, ok bool)

// RecordTokens 是 meter 回写适配：周期取记账时刻（UTC 的 YYYY-MM）。
// 失败只记日志——计量落库失败不应连带推理失败（用量是旁路，不是请求前置条件）。
func RecordTokens(w TokenUsageStore, tenantID, appID, model string, tokens int, now time.Time) {
	if tenantID == "" || tokens <= 0 {
		return
	}
	if err := w.AddTokenUsageForTenant(tenantID, PeriodOf(now), appID, model, int64(tokens)); err != nil {
		log.Printf("[billing] 记录 token 用量失败 tenant=%s model=%s tokens=%d: %v", tenantID, model, tokens, err)
	}
}

// EstimateTokensCost 按模型单价估算费用（元）。
// 返回 unpriced：出现在用量里但目录未定价的模型（排序去重），供页面如实标注。
func EstimateTokensCost(rows []TokenUsageRow, price ModelPrice) (cost float64, unpriced []string) {
	seen := map[string]struct{}{}
	for _, r := range rows {
		if r.Tokens <= 0 {
			continue
		}
		if p, ok := price(r.Model); ok && p > 0 {
			cost += float64(r.Tokens) / 1e6 * p
			continue
		}
		seen[r.Model] = struct{}{}
	}
	for m := range seen {
		unpriced = append(unpriced, m)
	}
	sort.Strings(unpriced)
	return cost, unpriced
}

// BudgetStatus 是预算执行状态（读时计算，不落库）。
type BudgetStatus struct {
	Budget         Budget   `json:"budget"`
	Period         string   `json:"period"`
	Tokens         int64    `json:"tokens"`
	EstimatedCost  float64  `json:"estimatedCost"`
	Ratio          float64  `json:"ratio"` // 0~N（可 >1）；预算为 0 时按 0
	State          string   `json:"state"` // ok | warn | exceeded
	UnpricedModels []string `json:"unpricedModels,omitempty"`
	// CostBasis 是估算口径说明（页面直接展示，避免把估算当账单）。
	CostBasis string `json:"costBasis"`
}

// ComputeBudgetStatus 由当期用量行计算预算状态。
// rows 应已按预算范围过滤（tenant=全部行；app=仅该 appId 的行）。
func ComputeBudgetStatus(b Budget, period string, rows []TokenUsageRow, price ModelPrice) BudgetStatus {
	var tokens int64
	for _, r := range rows {
		tokens += r.Tokens
	}
	cost, unpriced := EstimateTokensCost(rows, price)
	st := BudgetStatus{
		Budget: b, Period: period, Tokens: tokens, EstimatedCost: cost,
		UnpricedModels: unpriced,
		State:          BudgetOK,
		CostBasis:      "估算：按模型输出单价 × token 计算；平台未拆分 prompt/completion，未定价模型成本按 0 计",
	}
	if b.MonthlyAmount > 0 {
		st.Ratio = cost / b.MonthlyAmount
	}
	warnAt := b.WarnRatio
	if warnAt <= 0 {
		warnAt = DefaultWarnRatio
	}
	switch {
	case st.Ratio >= 1:
		st.State = BudgetExceeded
	case st.Ratio >= warnAt:
		st.State = BudgetWarn
	}
	return st
}

// SettlementRecord 是供应商结算对账记录（人工录入的供应商侧金额）。
//
// 平台没有供应商账单 API：本记录是**人工登记**的对账凭据，与平台估算并列展示差异，
// 不代表已付款、也不代表供应商认可（页面必须如实标注）。
type SettlementRecord struct {
	ID         string    `json:"id"`
	Vendor     string    `json:"vendor"`
	Period     string    `json:"period"` // YYYY-MM
	Amount     float64   `json:"amount"` // 供应商账单金额（元）
	Currency   string    `json:"currency"`
	InvoiceNo  string    `json:"invoiceNo,omitempty"`
	Note       string    `json:"note,omitempty"`
	RecordedBy string    `json:"recordedBy,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// VendorSettlement 是单个供应商的对账视图行。
type VendorSettlement struct {
	Vendor         string   `json:"vendor"`
	Tokens         int64    `json:"tokens"`
	EstimatedCost  float64  `json:"estimatedCost"`
	UnpricedModels []string `json:"unpricedModels,omitempty"`
	Models         []string `json:"models"`
	RecordedAmount float64  `json:"recordedAmount"` // 同期该供应商对账记录汇总
	RecordCount    int      `json:"recordCount"`
	Diff           float64  `json:"diff"` // RecordedAmount - EstimatedCost（正=供应商更贵）
}

// BuildVendorSettlements 把当期用量按供应商聚合，并叠加人工对账记录。
// 未归属供应商的模型（目录缺失）单列到 vendor="" 行，不被静默丢弃。
func BuildVendorSettlements(rows []TokenUsageRow, records []SettlementRecord, vendor ModelVendor, price ModelPrice) []VendorSettlement {
	byVendor := map[string]*VendorSettlement{}
	models := map[string]map[string]struct{}{}
	for _, r := range rows {
		v, ok := vendor(r.Model)
		if !ok || v == "" {
			v = "" // 目录未收录：保留可见，页面显示「未归属供应商」
		}
		vs := byVendor[v]
		if vs == nil {
			vs = &VendorSettlement{Vendor: v}
			byVendor[v] = vs
			models[v] = map[string]struct{}{}
		}
		vs.Tokens += r.Tokens
		models[v][r.Model] = struct{}{}
		if p, ok := price(r.Model); ok && p > 0 {
			vs.EstimatedCost += float64(r.Tokens) / 1e6 * p
		} else if r.Tokens > 0 {
			vs.UnpricedModels = append(vs.UnpricedModels, r.Model)
		}
	}
	// 只登记了对账记录、当期无用量的供应商也要可见（差异=全额）
	for _, rec := range records {
		vs := byVendor[rec.Vendor]
		if vs == nil {
			vs = &VendorSettlement{Vendor: rec.Vendor}
			byVendor[rec.Vendor] = vs
		}
		vs.RecordedAmount += rec.Amount
		vs.RecordCount++
	}
	out := make([]VendorSettlement, 0, len(byVendor))
	for v, vs := range byVendor {
		if ms := models[v]; ms != nil {
			for m := range ms {
				vs.Models = append(vs.Models, m)
			}
			sort.Strings(vs.Models)
		}
		sort.Strings(vs.UnpricedModels)
		vs.Diff = round2(vs.RecordedAmount - vs.EstimatedCost)
		vs.EstimatedCost = round2(vs.EstimatedCost)
		out = append(out, *vs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Vendor < out[j].Vendor })
	return out
}

// round2 保留两位小数（金额展示；避免浮点尾差在页面上串行）。
func round2(v float64) float64 { return math.Round(v*100) / 100 }
