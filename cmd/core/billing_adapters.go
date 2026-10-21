package main

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/billing"
	"github.com/aitoys/paas/internal/core/gateway"
	"github.com/aitoys/paas/internal/maas"
	"github.com/aitoys/paas/pkg/tenant"
)

// maasCatalogTTL 是模型目录快照的缓存时长。
// 预算/对账页每行都要查单价与供应商，逐行 ListModels 会把一次请求放大成 N 次全表读；
// admin 改价不频繁，60 秒延迟可接受（页面上的费用本来就是估算）。
const maasCatalogTTL = 60 * time.Second

// maasPricingCatalog 把 maas 模型目录适配为 billing 的 ModelPrice/ModelVendor 查询器。
// 目录读取失败时保留上一次快照（比「全部未定价」更接近真实），无快照才退化为空表。
type maasPricingCatalog struct {
	repo maas.Repository

	mu     sync.Mutex
	at     time.Time
	models map[string]modelMeta
}

type modelMeta struct {
	price  float64 // 元 / 百万 token（输出价）
	vendor string
}

func newMaaSPricingCatalog(repo maas.Repository) *maasPricingCatalog {
	return &maasPricingCatalog{repo: repo, models: map[string]modelMeta{}}
}

func (c *maasPricingCatalog) snapshot() map[string]modelMeta {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.at.IsZero() && time.Since(c.at) < maasCatalogTTL {
		return c.models
	}
	if c.repo == nil {
		return c.models
	}
	list, err := c.repo.ListModels(context.Background())
	if err != nil {
		log.Printf("[billing] 读取模型目录失败（沿用上次快照）: %v", err)
		c.at = time.Now() // 失败也退避一轮，避免目录故障时每行都触发一次失败查询
		return c.models
	}
	m := make(map[string]modelMeta, len(list))
	for _, x := range list {
		if x == nil {
			continue
		}
		m[x.ID] = modelMeta{price: x.OutputPrice, vendor: x.Vendor}
	}
	c.models, c.at = m, time.Now()
	return m
}

// Price 返回模型输出单价（元/百万 token）。未收录或单价 <=0 视为未定价。
func (c *maasPricingCatalog) Price(modelID string) (float64, bool) {
	m, ok := c.snapshot()[modelID]
	if !ok || m.price <= 0 {
		return 0, false
	}
	return m.price, true
}

// Vendor 返回模型所属供应商（供应商结算对账的聚合维度）。
func (c *maasPricingCatalog) Vendor(modelID string) (string, bool) {
	m, ok := c.snapshot()[modelID]
	if !ok || m.vendor == "" {
		return "", false
	}
	return m.vendor, true
}

// 编译期断言：适配器满足 billing 的两个查询器签名。
var (
	_ billing.ModelPrice  = (*maasPricingCatalog)(nil).Price
	_ billing.ModelVendor = (*maasPricingCatalog)(nil).Vendor
)

// budgetGateTTL 是预算判定结果的缓存时长。
// 每次推理都扫当期 token 行会让预算闸门成为推理链路的额外负载；护栏容忍 60 秒滞后
// （超限后最多 60 秒才拦住），换来的是稳态下零查询。缓存失效方向是「多放行」而非误拒。
const budgetGateTTL = 60 * time.Second

type budgetGateEntry struct {
	at  time.Time
	err error
}

// budgetGate 实现 gateway.BudgetGate：按租户/应用查当期估算费用，超启用强制的预算则拒绝。
// fail-open：查询失败返 nil（预算服务故障不应连带推理不可用），错误只记日志。
type budgetGate struct {
	repo  billing.Repository
	price billing.ModelPrice
	ttl   time.Duration

	mu    sync.Mutex
	cache map[string]budgetGateEntry
}

func newBudgetGate(repo billing.Repository, price billing.ModelPrice) *budgetGate {
	return &budgetGate{repo: repo, price: price, ttl: budgetGateTTL, cache: map[string]budgetGateEntry{}}
}

func (g *budgetGate) Check(ctx context.Context, tenantID, appID string) error {
	period := billing.PeriodOf(time.Now())
	key := tenantID + "|" + appID + "|" + period

	g.mu.Lock()
	if e, ok := g.cache[key]; ok && time.Since(e.at) < g.ttl {
		g.mu.Unlock()
		return e.err
	}
	g.mu.Unlock()

	err := g.evaluate(ctx, tenantID, appID, period)

	g.mu.Lock()
	if len(g.cache) > 4096 { // 粗粒度上限：防租户数增长后无界膨胀（过期项整体丢弃即可重建）
		g.cache = map[string]budgetGateEntry{}
	}
	g.cache[key] = budgetGateEntry{at: time.Now(), err: err}
	g.mu.Unlock()
	return err
}

func (g *budgetGate) evaluate(ctx context.Context, tenantID, appID, period string) error {
	if g.repo == nil {
		return nil
	}
	tctx := tenant.WithTenant(ctx, tenantID)
	budgets, err := g.repo.ListBudgets(tctx)
	if err != nil {
		log.Printf("[billing] 预算闸门读取预算失败（放行）tenant=%s: %v", tenantID, err)
		return nil
	}
	var enforce []billing.Budget
	for _, b := range budgets {
		if !b.Enforce {
			continue
		}
		if b.Scope == billing.BudgetScopeApp && b.AppID != appID {
			continue // 应用级预算只管自己那个应用
		}
		enforce = append(enforce, b)
	}
	if len(enforce) == 0 {
		return nil
	}
	rows, err := g.repo.ListTokenUsage(tctx, period)
	if err != nil {
		log.Printf("[billing] 预算闸门读取用量失败（放行）tenant=%s: %v", tenantID, err)
		return nil
	}
	for _, b := range enforce {
		scoped := rows
		if b.Scope == billing.BudgetScopeApp {
			scoped = make([]billing.TokenUsageRow, 0, len(rows))
			for _, r := range rows {
				if r.AppID == b.AppID {
					scoped = append(scoped, r)
				}
			}
		}
		if billing.ComputeBudgetStatus(b, period, scoped, g.price).State == billing.BudgetExceeded {
			log.Printf("[billing] 预算超限拒绝推理 tenant=%s app=%s scope=%s budget=%s period=%s",
				tenantID, appID, b.Scope, b.ID, period)
			return gateway.ErrBudgetExceeded
		}
	}
	return nil
}
