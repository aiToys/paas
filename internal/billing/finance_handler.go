// Package billing D5-4 财务深化 REST API：月度预算（租户侧自助 + admin 跨租户）
// 与供应商结算对账（admin 人工登记）。
//
// 租户侧：
//
//	GET    /api/billing/budgets            预算列表 + 当期执行状态（billing:read）
//	POST   /api/billing/budgets            新建预算（billing:quota-manage，与调配额同权限级）
//	PUT    /api/billing/budgets/{id}       改额度/阈值/是否强制（billing:quota-manage）
//	DELETE /api/billing/budgets/{id}       删除预算（billing:quota-manage）
//
// admin 侧：
//
//	GET    /api/admin/budgets              跨租户预算（含当期状态）
//	POST   /api/admin/budgets              代建（tenantId 必填）
//	PUT    /api/admin/budgets/{id}
//	DELETE /api/admin/budgets/{id}
//	GET    /api/admin/token-usage          周期 token 用量（跨租户）
//	GET    /api/admin/settlements          供应商对账视图（估算 vs 人工登记）
//	POST   /api/admin/settlements          登记供应商账单金额（人工，非付款）
//	DELETE /api/admin/settlements/{id}     删除对账记录
//
// 语义诚实：费用是估算（未拆分 prompt/completion、未定价模型成本记 0），
// 对账记录是人工登记凭据而非付款凭证——响应里 CostBasis 字段直接展示口径。
package billing

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
	adminutil "github.com/aitoys/paas/internal/web/admin"
	"github.com/aitoys/paas/pkg/tenant"
)

// ModelPriceResolver / ModelVendorResolver 由装配层桥接 maas 目录（billing 不依赖 maas）。
// nil 时所有模型视为「未定价/未归属」，页面如实标注而不是算成 0 元账单。
type (
	ModelPriceResolver  = ModelPrice
	ModelVendorResolver = ModelVendor
)

// WithModelCatalog 注入模型定价查询器（租户侧预算状态只需单价；供应商归属是平台对账视角）。
func WithModelCatalog(price ModelPriceResolver) HandlerOpt {
	return func(h *Handler) { h.price = price }
}

// WithAdminModelCatalog 注入模型定价与供应商归属查询器（admin 预算/用量/对账用）。
func WithAdminModelCatalog(price ModelPriceResolver, vendor ModelVendorResolver) AdminHandlerOpt {
	return func(h *AdminHandler) { h.price, h.vendor = price, vendor }
}

// BudgetOverview 是预算状态列表响应。
type BudgetOverview struct {
	Period string         `json:"period"`
	Rows   []BudgetStatus `json:"rows"`
	// CostBasis 与行内同源，放在顶层便于列表页统一展示口径说明。
	CostBasis string `json:"costBasis"`
}

// budgetCostBasis 是全平台统一口径文案（顶层与行内一致，避免两处措辞漂移）。
const budgetCostBasis = "估算：按模型输出单价 × token 计算；平台未拆分 prompt/completion，未定价模型成本按 0 计"

// price 为 nil 时返回 ok=false（未定价），调用方据此如实标注。
func (h *Handler) priceOf(model string) (float64, bool) {
	if h.price == nil {
		return 0, false
	}
	return h.price(model)
}

func (h *AdminHandler) priceOf(model string) (float64, bool) {
	if h.price == nil {
		return 0, false
	}
	return h.price(model)
}

// periodParam 取 ?period=YYYY-MM，缺省当期（UTC）。
func periodParam(r *http.Request) string {
	if p := strings.TrimSpace(r.URL.Query().Get("period")); p != "" {
		return p
	}
	return PeriodOf(time.Now())
}

// ---------- 租户侧预算 ----------

func (h *Handler) serveBudgets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermBillingRead) {
			return
		}
		period := periodParam(r)
		h.writeBudgetOverview(w, r, period)
	case http.MethodPost:
		if !h.allow(w, r, PermQuotaManage) {
			return
		}
		h.createBudget(w, r)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// serveBudgetItem 处理 /api/billing/budgets/{id}（PUT/DELETE）。
func (h *Handler) serveBudgetItem(w http.ResponseWriter, r *http.Request) {
	if !h.allow(w, r, PermQuotaManage) {
		return
	}
	id := strings.TrimPrefix(strings.TrimRight(r.URL.Path, "/"), "/api/billing/budgets/")
	if id == "" || strings.Contains(id, "/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	cur, err := h.repo.GetBudget(r.Context(), id)
	if err != nil {
		writeBudgetErr(w, err)
		return
	}
	// 同租户校验：跨租户 404（不泄漏存在性），与其余模块一致。
	if tid, _ := tenant.TenantFrom(r.Context()); cur.TenantID != tid {
		httputil.WriteError(w, http.StatusNotFound, "预算不存在")
		return
	}
	switch r.Method {
	case http.MethodPut:
		var in Budget
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		in.ID = cur.ID
		in.TenantID = cur.TenantID
		in.Scope = cur.Scope
		in.AppID = cur.AppID
		saved, err := h.repo.UpdateBudget(r.Context(), in)
		if err != nil {
			writeBudgetErr(w, err)
			return
		}
		h.recordAudit(r, "update_budget", "budget", saved.ID,
			fmt.Sprintf("预算额度 %.2f → %.2f（enforce=%v）", cur.MonthlyAmount, saved.MonthlyAmount, saved.Enforce))
		httputil.WriteData(w, saved)
	case http.MethodDelete:
		if err := h.repo.DeleteBudget(r.Context(), cur.ID); err != nil {
			writeBudgetErr(w, err)
			return
		}
		h.recordAudit(r, "delete_budget", "budget", cur.ID, fmt.Sprintf("删除预算（原额度 %.2f）", cur.MonthlyAmount))
		httputil.WriteData(w, map[string]string{"id": cur.ID})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) createBudget(w http.ResponseWriter, r *http.Request) {
	var in Budget
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	// 租户侧一律以 ctx 租户为准（忽略请求体 TenantID，防跨租户建预算）。
	in.TenantID = ""
	saved, err := h.repo.CreateBudget(r.Context(), in)
	if err != nil {
		writeBudgetErr(w, err)
		return
	}
	h.recordAudit(r, "create_budget", "budget", saved.ID,
		fmt.Sprintf("新建预算 scope=%s app=%s 额度=%.2f enforce=%v", saved.Scope, saved.AppID, saved.MonthlyAmount, saved.Enforce))
	httputil.WriteDataCreated(w, saved)
}

// writeBudgetOverview 组装预算列表 + 当期状态（一次取用量行，避免逐条查库）。
func (h *Handler) writeBudgetOverview(w http.ResponseWriter, r *http.Request, period string) {
	budgets, err := h.repo.ListBudgets(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	rows, err := h.repo.ListTokenUsage(r.Context(), period)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	out := make([]BudgetStatus, 0, len(budgets))
	for _, b := range budgets {
		st := ComputeBudgetStatus(b, period, filterUsageByBudget(rows, b), h.priceOf)
		st.CostBasis = budgetCostBasis
		out = append(out, st)
	}
	httputil.WriteData(w, BudgetOverview{Period: period, Rows: out, CostBasis: budgetCostBasis})
}

// filterUsageByBudget 按预算范围过滤用量行（tenant=全部；app=仅该 appId）。
func filterUsageByBudget(rows []TokenUsageRow, b Budget) []TokenUsageRow {
	if b.Scope != BudgetScopeApp {
		return rows
	}
	out := make([]TokenUsageRow, 0, len(rows))
	for _, r := range rows {
		if r.AppID == b.AppID {
			out = append(out, r)
		}
	}
	return out
}

// writeBudgetErr 预算错误分流：不存在 404、校验/重复 400、其余 500 脱敏。
func writeBudgetErr(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrBudgetNotFound) {
		httputil.WriteError(w, http.StatusNotFound, "预算不存在")
		return
	}
	httputil.WriteServiceError(w, http.StatusBadRequest, err)
}

// ---------- admin ----------

// serveBudgets 处理 /api/admin/budgets（GET/POST）与 /api/admin/budgets/{id}（PUT/DELETE）。
func (h *AdminHandler) serveBudgets(w http.ResponseWriter, r *http.Request, path string) {
	rest := strings.Trim(strings.TrimPrefix(path, "/api/admin/budgets"), "/")
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			h.adminBudgetList(w, r)
		case http.MethodPost:
			h.adminCreateBudget(w, r)
		default:
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if strings.Contains(rest, "/") {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodPut:
		h.adminUpdateBudget(w, r, rest)
	case http.MethodDelete:
		h.adminDeleteBudget(w, r, rest)
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *AdminHandler) adminBudgetList(w http.ResponseWriter, r *http.Request) {
	period := periodParam(r)
	budgets, err := h.repo.ListAllBudgets(r.Context())
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	budgets = adminutil.FilterTenant(r, budgets, func(b Budget) string { return b.TenantID })
	rows, err := h.repo.ListAllTokenUsage(r.Context(), period)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	out := make([]BudgetStatus, 0, len(budgets))
	for _, b := range budgets {
		scoped := make([]TokenUsageRow, 0, len(rows))
		for _, u := range rows {
			if u.TenantID != b.TenantID {
				continue
			}
			if b.Scope == BudgetScopeApp && u.AppID != b.AppID {
				continue
			}
			scoped = append(scoped, u)
		}
		st := ComputeBudgetStatus(b, period, scoped, h.priceOf)
		st.CostBasis = budgetCostBasis
		out = append(out, st)
	}
	httputil.WriteData(w, BudgetOverview{Period: period, Rows: out, CostBasis: budgetCostBasis})
}

func (h *AdminHandler) adminCreateBudget(w http.ResponseWriter, r *http.Request) {
	var in Budget
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if in.TenantID == "" {
		httputil.WriteError(w, http.StatusBadRequest, "missing tenantId")
		return
	}
	if h.tenants != nil {
		if err := h.tenants.Exists(r.Context(), in.TenantID); err != nil {
			httputil.WriteServiceError(w, http.StatusBadRequest, fmt.Errorf("租户不存在: %s", in.TenantID))
			return
		}
	}
	ctx, rr := adminutil.TenantCtx(r, in.TenantID)
	saved, err := h.repo.CreateBudget(ctx, in)
	if err != nil {
		writeBudgetErr(w, err)
		return
	}
	h.recordAudit(rr, saved.TenantID, "admin:create-budget", "budget", saved.ID,
		fmt.Sprintf("代建预算 scope=%s app=%s 额度=%.2f", saved.Scope, saved.AppID, saved.MonthlyAmount))
	httputil.WriteDataCreated(w, saved)
}

func (h *AdminHandler) adminUpdateBudget(w http.ResponseWriter, r *http.Request, id string) {
	cur, err := h.repo.GetBudget(r.Context(), id)
	if err != nil {
		writeBudgetErr(w, err)
		return
	}
	var in Budget
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	// 归属字段以库内为准（admin 改归属同样应删除后新建，避免出现两条同范围预算）。
	in.ID, in.TenantID, in.Scope, in.AppID = cur.ID, cur.TenantID, cur.Scope, cur.AppID
	ctx, rr := adminutil.TenantCtx(r, cur.TenantID)
	saved, err := h.repo.UpdateBudget(ctx, in)
	if err != nil {
		writeBudgetErr(w, err)
		return
	}
	h.recordAudit(rr, saved.TenantID, "admin:update-budget", "budget", saved.ID,
		fmt.Sprintf("预算额度 %.2f → %.2f（enforce=%v）", cur.MonthlyAmount, saved.MonthlyAmount, saved.Enforce))
	httputil.WriteData(w, saved)
}

func (h *AdminHandler) adminDeleteBudget(w http.ResponseWriter, r *http.Request, id string) {
	cur, err := h.repo.GetBudget(r.Context(), id)
	if err != nil {
		writeBudgetErr(w, err)
		return
	}
	ctx, rr := adminutil.TenantCtx(r, cur.TenantID)
	if err := h.repo.DeleteBudget(ctx, cur.ID); err != nil {
		writeBudgetErr(w, err)
		return
	}
	h.recordAudit(rr, cur.TenantID, "admin:delete-budget", "budget", cur.ID,
		fmt.Sprintf("删除预算（原额度 %.2f）", cur.MonthlyAmount))
	httputil.WriteData(w, map[string]string{"id": cur.ID})
}

// TokenUsageOverview 是周期 token 用量响应。
type TokenUsageOverview struct {
	Period         string          `json:"period"`
	Rows           []TokenUsageRow `json:"rows"`
	TotalTokens    int64           `json:"totalTokens"`
	EstimatedCost  float64         `json:"estimatedCost"`
	UnpricedModels []string        `json:"unpricedModels,omitempty"`
	CostBasis      string          `json:"costBasis"`
}

func (h *AdminHandler) serveTokenUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	period := periodParam(r)
	rows, err := h.repo.ListAllTokenUsage(r.Context(), period)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	rows = adminutil.FilterTenant(r, rows, func(u TokenUsageRow) string { return u.TenantID })
	var total int64
	for _, u := range rows {
		total += u.Tokens
	}
	cost, unpriced := EstimateTokensCost(rows, h.priceOf)
	httputil.WriteData(w, TokenUsageOverview{
		Period: period, Rows: rows, TotalTokens: total,
		EstimatedCost: round2(cost), UnpricedModels: unpriced, CostBasis: budgetCostBasis,
	})
}

// SettlementOverview 是供应商对账视图。
// Rows 是「平台估算 vs 人工登记」的差异表；RecordedTotal/EstimatedTotal 是汇总。
type SettlementOverview struct {
	Period          string             `json:"period"`
	Rows            []VendorSettlement `json:"rows"`
	Records         []SettlementRecord `json:"records"`
	EstimatedTotal  float64            `json:"estimatedTotal"`
	RecordedTotal   float64            `json:"recordedTotal"`
	UnpricedModels  []string           `json:"unpricedModels,omitempty"`
	CostBasis       string             `json:"costBasis"`
	SettlementBasis string             `json:"settlementBasis"`
}

// settlementBasis 说明对账记录的性质（页面直接展示，防止把人工登记当付款凭证）。
const settlementBasis = "对账记录为人工登记的供应商账单金额，平台无供应商账单 API，不代表已付款或供应商已确认"

func (h *AdminHandler) serveSettlements(w http.ResponseWriter, r *http.Request, path string) {
	rest := strings.Trim(strings.TrimPrefix(path, "/api/admin/settlements"), "/")
	if rest == "" {
		switch r.Method {
		case http.MethodGet:
			h.settlementList(w, r)
		case http.MethodPost:
			h.createSettlement(w, r)
		default:
			httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	if r.Method != http.MethodDelete || strings.Contains(rest, "/") {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := rest
	records, err := h.repo.ListSettlements(r.Context(), "")
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	var target *SettlementRecord
	for i := range records {
		if records[i].ID == id {
			target = &records[i]
			break
		}
	}
	if target == nil {
		httputil.WriteError(w, http.StatusNotFound, "对账记录不存在")
		return
	}
	if err := h.repo.DeleteSettlement(r.Context(), id); err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(r, "platform", "admin:delete-settlement", "settlement", id,
		fmt.Sprintf("删除对账记录 vendor=%s period=%s amount=%.2f", target.Vendor, target.Period, target.Amount))
	httputil.WriteData(w, map[string]string{"id": id})
}

func (h *AdminHandler) settlementList(w http.ResponseWriter, r *http.Request) {
	period := periodParam(r)
	rows, err := h.repo.ListAllTokenUsage(r.Context(), period)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	rows = adminutil.FilterTenant(r, rows, func(u TokenUsageRow) string { return u.TenantID })
	records, err := h.repo.ListSettlements(r.Context(), period)
	if err != nil {
		httputil.WriteInternalError(w, err)
		return
	}
	vs := BuildVendorSettlements(rows, records, h.vendorOf, h.priceOf)
	var est, rec float64
	unpriced := map[string]struct{}{}
	for _, v := range vs {
		est += v.EstimatedCost
		rec += v.RecordedAmount
		for _, m := range v.UnpricedModels {
			unpriced[m] = struct{}{}
		}
	}
	list := make([]string, 0, len(unpriced))
	for m := range unpriced {
		list = append(list, m)
	}
	sort.Strings(list)
	httputil.WriteData(w, SettlementOverview{
		Period: period, Rows: vs, Records: records,
		EstimatedTotal: round2(est), RecordedTotal: round2(rec), UnpricedModels: list,
		CostBasis: budgetCostBasis, SettlementBasis: settlementBasis,
	})
}

// settlementInput 是人工对账记录请求体。recordedBy 由服务端填 actor，不收客户端值。
type settlementInput struct {
	Vendor    string  `json:"vendor"`
	Period    string  `json:"period"`
	Amount    float64 `json:"amount"`
	Currency  string  `json:"currency"`
	InvoiceNo string  `json:"invoiceNo"`
	Note      string  `json:"note"`
}

func (h *AdminHandler) createSettlement(w http.ResponseWriter, r *http.Request) {
	var in settlementInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	in.Vendor = strings.TrimSpace(in.Vendor)
	if in.Vendor == "" {
		httputil.WriteError(w, http.StatusBadRequest, "vendor 必填（供应商名，与模型目录 vendor 一致才能对上估算）")
		return
	}
	if in.Period == "" {
		in.Period = PeriodOf(time.Now())
	}
	if in.Amount <= 0 {
		httputil.WriteError(w, http.StatusBadRequest, "amount 必须大于 0")
		return
	}
	if in.Currency == "" {
		in.Currency = "CNY"
	}
	saved, err := h.repo.CreateSettlement(r.Context(), SettlementRecord{
		Vendor: in.Vendor, Period: in.Period, Amount: in.Amount, Currency: in.Currency,
		InvoiceNo: in.InvoiceNo, Note: in.Note, RecordedBy: h.actor(r),
	})
	if err != nil {
		httputil.WriteServiceError(w, http.StatusBadRequest, err)
		return
	}
	h.recordAudit(r, "platform", "admin:create-settlement", "settlement", saved.ID,
		fmt.Sprintf("登记供应商账单 vendor=%s period=%s amount=%.2f %s", saved.Vendor, saved.Period, saved.Amount, saved.Currency))
	httputil.WriteDataCreated(w, saved)
}

func (h *AdminHandler) vendorOf(model string) (string, bool) {
	if h.vendor == nil {
		return "", false
	}
	return h.vendor(model)
}
