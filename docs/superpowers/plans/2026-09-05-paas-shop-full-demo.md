# paas-shop 全能力演示（订单闭环 + prod 上线）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** paas-shop 示例新增 order 微服务订单最小闭环 + sweeper CronJob + MQ 双消费组 + 退款审批 workflow + MCP 真实化 + 泳道演示 + promote prod 全套 setup + AdminPanel + 文档站，完整演示平台全部能力。

**Architecture:** examples 侧新增 2 个服务（order :8084 / sweeper CronJob）+ 改造 bff/mcp/frontend；平台侧零代码改动（setup 脚本纯 API 编排）。prod 上线走显式命名 baseline（`-prod` 后缀 + Service 字段跨环境同值）规避 BaselineWorkloadName 无环境维度的撞名问题。

**Tech Stack:** Go 1.26（examples module `github.com/aitoys/paas-examples`，vendor 模式，无新依赖：pgx/nats/prometheus 已有）；Vue3 纯 ref/computed（无 router/pinia/UI 库）；bash setup 脚本（curl 平台 API）。

**Spec:** `docs/superpowers/specs/2026-09-05-paas-shop-full-demo-design.md`

---

## 已验证平台事实（计划编写期间代码级确认，勿再验证）

1. **CreateRelease 按 `(app, env, lane, Service 名)` 匹配基线**（`internal/devops/pg/store.go:794-825`，ServiceID 优先、Service 名兜底）。显式命名 `paas-shop-<svc>-prod`（Name）+ `Service="<svc>"`（与 test 同值）+ `EnvID=env-acme-prod-bj` 的 workload 会被 promote 的 CreateRelease 正确找到并 UpdateImage。单测佐证 `internal/devops/pg/store_test.go:342 TestCreateReleaseExistingWL`。
2. **泳道自动命名与 dpdisc 查询错配一个 `-svc`**：pipeline 自动建泳道 workload 名 = `<appID>-<svc>-svc-<lane>`（`internal/devops/model.go:313 BaselineWorkloadName`），而 `/dp/instances` 的 lane 拼接 = `<service参数>-<lane>`（`internal/dataplane/endpoints.go:90`）。bff dpdisc 传 `paas-shop-<svc>`（现有基线 K8s Service 名，无 `-svc`）→ 拼出 `paas-shop-<svc>-<lane>`。**裁决：走 spec §8 预留分支——泳道演示用显式命名 workload `paas-shop-<svc>-<lane>`（与基线命名同族），dpdisc/examples 零改动天然命中**。平台修复（EndpointsReader 按 lane label 匹配）记遗留（spec §14.2）。CI 流水线自动建的泳道 workload（`-svc-` 命名）dpdisc 不命中会降级基线——CI 泳道演示点是隔离部署非流量分叉，可接受。
3. **环境无 PUT 端点，spec §9⑤ 不需要**：prod-bj/prod-sh 同 `promoteOrder=20`，`NextPromoteTarget` 是 `ORDER BY promote_order ASC, id ASC`——id 字符串序 `"env-acme-prod-bj" < "env-acme-prod-sh"` → prod-bj 天然是 test 的晋升目标。⑤ 改为 e2e 断言（Task 12）。
4. **Secret scope 只有 `tenant|platform`，无 `app`**（`internal/security/model.go:55`）。spec §7「app 级 Secret」改由 **appconfig `type=secret`** 实现同等演示点：`appconfig.NewEncryptedRepo` 落库 `enc:v1:`（`cmd/core/persistence.go:223`）+ reconciler 按 (app, env) 桶注入该环境全部 workload（天然多消费者）。另建一条平台 Secret 实体演示掩码管理面。
5. **tpl-cd 无 promote stage**（`internal/devops/pipeline/templates.go:48-72`：approve→canary→release→baseline）。spec §9 演示流水线 `build→deploy(test)→release→promote` 需经 `POST /api/admin/pipeline-templates`（super_admin，sk-acme-admin 可用）建租户自定义模板（带 `tenantId:"t-acme"`）。promote stage 零参数（依赖前置 release 的 OutReleaseID，自动 NextPromoteTarget）。
6. **Route 需治理 ServiceID**（`internal/governance/model.go:158`）：setup 先 POST /api/services（Name=目标 workload 名）再 POST /api/routes。K8sRouteApplier 按 host 聚合多 path Ingress。
7. **products 表**（product/main.go:147）：`id SERIAL, name, price NUMERIC(10,2), category, stock INT, description, image_url, created_at`。订单金额 cents 需服务端换算 `int(math.Round(price * 100))`（防客户端伪造价格 + 浮点误差）。
8. **workload Create 必须走应用路径** `POST /api/applications/{id}/workloads`（裸 `/api/workloads` POST 不支持）；BuildRun 同理。集群 API：`http://paas.k8s.dd` + `Bearer sk-acme-admin`。
9. **Dockerfile.backend 零改动**：`ARG SERVICE` + `go build -mod=vendor -o /svc ./paas-shop/${SERVICE}`——order/sweeper 目录即服务。examples 无新依赖（pgx/nats.go/prometheus client 已 vendor）。
10. **bff 既有结构**：`fallback map`（product/recommend/chatbot）+ `svcURL`（dpdisc 优先传 `paas-shop-<svc>`）+ `buildMux(events)` 可测模式 + `shopEvent{Type, ProductID int, Name, Category, At, ReceivedAt}` + `subscribeShopEvents`（QueueSubscribe group="bff-consumer"）。proxyTo/proxyPrefixTo 用 copyHeaders 复制全部 header。

## Spec 偏差裁决汇总（执行者须知）

| Spec 原文 | 实际落地 | 依据 |
|-----------|---------|------|
| §9⑤ prod-sh 调 promoteOrder=25 | 删除（无 PUT 端点且天然满足），改 e2e 断言 | 事实 3 |
| §7 Secret scope=app | appconfig type=secret（enc:v1 落库）+ 平台 Secret 实体演示掩码 | 事实 4 |
| §8 泳道经 pipeline 自动命名 | 泳道 workload 显式命名 `paas-shop-<svc>-<lane>` | 事实 2 |
| §9 演示流水线直接创建 | 需先 admin 建租户自定义模板再绑 pipeline | 事实 5 |

## Global Constraints

- **语言**：所有新代码注释中文，与现有代码库一致。
- **域名**：对外材料（文档/脚本注释/Route host）统一 `*.example.com`，不出现内网真实域名（paas.k8s.dd 仅出现在脚本默认 env 值中，头注释说明）。
- **不执行 `git commit`/分支操作**：项目约定「未经用户明确要求不执行 git commit」。本计划各 Task 末尾的 Commit 步骤**仅在用户批准执行方式时一并授权，否则跳过**（留工作区改动待用户处理）。
- **examples 独立 module**：`github.com/aitoys/paas-examples`，禁止 import 主仓包；测试 `cd examples && go test ./paas-shop/...`。
- **平台侧零代码改动**：所有平台能力经 API 编排（setup 脚本），不改 internal/ cmd/。
- **验证命令**：Go 测试在 `examples/` 目录跑；前端构建 `cd frontend && pnpm build`（三套）；平台回归在仓库根 `make test`。
- **端口约定**：product:8081 / recommend:8082 / chatbot:8083 / bff:8080 / mcp:8080 / **order:8084**（新）。
- **集群访问**：`PAAS_API=http://paas.k8s.dd`、`PAAS_TOKEN=sk-acme-admin`（super_admin，可走 /api/admin/*）。

---

### Task 1: order 状态机纯函数（表驱动单测）

**Files:**
- Create: `examples/paas-shop/order/state.go`
- Test: `examples/paas-shop/order/state_test.go`

**Interfaces:**
- Produces: `func Transition(from, action string) (string, error)`——Task 2 的 handler 调用；错误时 handler 返 409。
- 常量：`StatusPending="pending"` / `StatusPaid="paid"` / `StatusShipped="shipped"` / `StatusCompleted="completed"` / `StatusRefunded="refunded"` / `StatusClosed="closed"`；动作 `pay/ship/confirm/refund/close`。

- [ ] **Step 1: 写失败测试**

```go
// examples/paas-shop/order/state_test.go
package main

import "testing"

// TestTransitionLegal 合法迁移全覆盖（spec §3 状态机）。
func TestTransitionLegal(t *testing.T) {
	cases := []struct{ from, action, want string }{
		{StatusPending, "pay", StatusPaid},
		{StatusPaid, "ship", StatusShipped},
		{StatusShipped, "confirm", StatusCompleted},
		{StatusPaid, "refund", StatusRefunded},
		{StatusShipped, "refund", StatusRefunded},
		{StatusPending, "close", StatusClosed},
	}
	for _, c := range cases {
		got, err := Transition(c.from, c.action)
		if err != nil || got != c.want {
			t.Errorf("Transition(%s,%s) = (%s,%v), want (%s,nil)", c.from, c.action, got, err, c.want)
		}
	}
}

// TestTransitionIllegal 非法迁移全量枚举（含终态不可再动 + 乱序动作）。
func TestTransitionIllegal(t *testing.T) {
	statuses := []string{StatusPending, StatusPaid, StatusShipped, StatusCompleted, StatusRefunded, StatusClosed, "bogus"}
	actions := []string{"pay", "ship", "confirm", "refund", "close", "bogus"}
	legal := map[string]bool{
		"pending|pay": true, "paid|ship": true, "shipped|confirm": true,
		"paid|refund": true, "shipped|refund": true, "pending|close": true,
	}
	for _, s := range statuses {
		for _, a := range actions {
			if legal[s+"|"+a] {
				continue
			}
			if got, err := Transition(s, a); err == nil {
				t.Errorf("Transition(%s,%s) 应报错，实际返 %s", s, a, got)
			}
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
cd examples && go test ./paas-shop/order/
```
预期：FAIL（`Transition` 未定义，编译错误）。

- [ ] **Step 3: 最小实现**

```go
// examples/paas-shop/order/state.go
package main

import "fmt"

// 订单状态（spec §3：pending →(pay) paid →(ship) shipped →(confirm) completed；
// pending --超时关单--> closed（sweeper）；paid|shipped →(退款审批流) refunded）。
const (
	StatusPending   = "pending"
	StatusPaid      = "paid"
	StatusShipped   = "shipped"
	StatusCompleted = "completed"
	StatusRefunded  = "refunded"
	StatusClosed    = "closed"
)

// transitions 动作 → 当前状态 → 目标状态（单一真源，非法组合查表不中即拒）。
var transitions = map[string]map[string]string{
	"pay":     {StatusPending: StatusPaid},
	"ship":    {StatusPaid: StatusShipped},
	"confirm": {StatusShipped: StatusCompleted},
	"refund":  {StatusPaid: StatusRefunded, StatusShipped: StatusRefunded},
	"close":   {StatusPending: StatusClosed},
}

// Transition 校验并返回目标状态；非法迁移返错（handler 映射 409）。
func Transition(from, action string) (string, error) {
	if m, ok := transitions[action]; ok {
		if to, ok := m[from]; ok {
			return to, nil
		}
	}
	return "", fmt.Errorf("订单状态 %s 不允许执行 %s", from, action)
}
```

- [ ] **Step 4: 跑测试确认通过**

```bash
cd examples && go test ./paas-shop/order/ -v
```
预期：PASS（2 个测试）。

- [ ] **Step 5: Commit（经用户授权后）**

```bash
git add examples/paas-shop/order/
git commit -m "feat(shop): order 状态机纯函数 + 表驱动单测"
```

---

### Task 2: order 服务完整实现

**Files:**
- Create: `examples/paas-shop/order/main.go`
- Test: `examples/paas-shop/order/main_test.go`

**Interfaces:**
- Consumes: Task 1 的 `Transition`；共享包 `observ`（Init/Handler/Recover/LaneMiddleware/MetricsHandler/NewClient/ServiceFingerprint/LaneOrBase）、`natspub`（Connect/Publish/Stub）、`dpdisc` 不需要（order 无下游调用）。
- Produces: HTTP API（bff Task 4 / mcp Task 5 / sweeper Task 3 消费）：
  - `POST /orders` body `{"userId":"u1","items":[{"productId":1,"qty":2}]}` → 201 `Order`（服务端查价、事务扣库存、库存不足 409）
  - `GET /orders[?status=pending]` → `[]Order`
  - `GET /orders/{id}` → `Order`
  - `POST /orders/{id}/pay|ship|refund|close|confirm` → 200 `Order`（refund/close 事务内回补库存）
  - 变更类端点（POST）要求 header `X-Action-Token` == env `PAAS_ORDER_ACTION_TOKEN`（env 空则不校验，本地 dev 可用）
  - `/healthz`、`/metrics`（`shop_order_created_total` counter + `shop_order_amount_cents` histogram）
- NATS 事件（subject `shop-events`，事务提交后发）：`{"type":"order.created|order.paid|order.shipped|order.refunded|order.closed","orderId","userId","status","amount","at"}`——bff Task 4 消费。

- [ ] **Step 1: 写失败测试（buildMux 可测模式，参照 bff_test.go）**

```go
// examples/paas-shop/order/main_test.go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTokenRequired 变更端点缺 X-Action-Token 拒 401、带 token 放行（直测中间件，不触 DB）。
func TestTokenRequired(t *testing.T) {
	actionToken = "test-token"
	defer func() { actionToken = "" }()
	called := false
	h := requireToken(func(http.ResponseWriter, *http.Request) { called = true })
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodPost, "/orders", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("缺 token 应 401，got %d", w.Code)
	}
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/orders", nil)
	req2.Header.Set("X-Action-Token", "test-token")
	h(w2, req2)
	if !called {
		t.Fatal("带 token 应放行到后端 handler")
	}
}

// TestOrderJSONContract Order 序列化契约（bff/frontend 消费的字段名锁定）。
func TestOrderJSONContract(t *testing.T) {
	b, err := json.Marshal(Order{ID: "ORD-1", UserID: "u1", Status: StatusPending, TotalCents: 9900,
		Items: []Item{{ProductID: 1, Name: "机械键盘", Qty: 1, PriceCents: 9900}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"id":"ORD-1"`, `"userId":"u1"`, `"status":"pending"`, `"totalCents":9900`, `"productId":1`, `"priceCents":9900`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("Order JSON 应含 %s，got %s", key, b)
		}
	}
}

// TestNewOrderID ID 形态 ORD-<后10位数字>（文档/frontend 展示依赖前缀）。
func TestNewOrderID(t *testing.T) {
	id := newOrderID()
	if !strings.HasPrefix(id, "ORD-") || len(id) != 14 {
		t.Fatalf("ID 形态应 ORD-+10位，got %s", id)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
cd examples && go test ./paas-shop/order/
```
预期：FAIL（`buildMux`/`Order`/`actionToken`/`newOrderID` 未定义）。

- [ ] **Step 3: 实现 main.go（完整）**

```go
// examples/paas-shop/order/main.go
// order 服务：paas-shop 订单最小闭环（下单事务扣库存 → 支付/发货/确认 → 退款/超时关单）。
// 事件经 NATS shop-events 主题广播（事务提交后发，at-most-once）；金额单位分（cents）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/aitoys/paas-examples/paas-shop/internal/natspub"
	"github.com/aitoys/paas-examples/paas-shop/internal/observ"
)

// Item 订单行（快照商品名/价格，防商品改价影响历史订单展示）。
type Item struct {
	ProductID  int    `json:"productId"`
	Name       string `json:"name"`
	Qty        int    `json:"qty"`
	PriceCents int    `json:"priceCents"`
}

// Order 订单聚合。TotalCents 服务端按商品现价计算（防客户端伪造）。
type Order struct {
	ID         string    `json:"id"`
	UserID     string    `json:"userId"`
	Status     string    `json:"status"`
	TotalCents int       `json:"totalCents"`
	Items      []Item    `json:"items"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type createReq struct {
	UserID string `json:"userId"`
	Items  []struct {
		ProductID int `json:"productId"`
		Qty       int `json:"qty"`
	} `json:"items"`
}

// 业务指标（Grafana 演示：下单 QPS + 订单金额分布）。
var (
	orderCreated = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "shop_order_created_total", Help: "下单成功总数",
	})
	orderAmount = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "shop_order_amount_cents", Help: "订单金额分布（分）",
		Buckets: []float64{1000, 5000, 10000, 50000, 100000, 500000},
	})
)

func init() { prometheus.MustRegister(orderCreated, orderAmount) }

var actionToken string // env PAAS_ORDER_ACTION_TOKEN；空=不校验（本地 dev）

// newOrderID 生成 ORD-<unixnano 后 10 位>（spec §3）。
func newOrderID() string {
	return fmt.Sprintf("ORD-%010d", time.Now().UnixNano()%1e10)
}

func main() {
	shutdown := observ.Init("paas-shop-order")
	defer shutdown()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		slog.Error("DATABASE_URL 必填（与商品同库）")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		slog.Error("PG 连接失败", "err", err, "dsn", maskDSN(dsn))
		os.Exit(1)
	}
	defer db.Close()
	if err := migrate(ctx, db); err != nil {
		slog.Error("迁移失败", "err", err)
		os.Exit(1)
	}
	np := natspub.Connect(os.Getenv("NATS_URL")) // 降级 stub 不阻断
	actionToken = os.Getenv("PAAS_ORDER_ACTION_TOKEN")

	h := observ.Recover(observ.Handler("order", observ.LaneMiddleware(buildMux(db, np))))
	srv := &http.Server{Addr: ":8084", Handler: h, ReadHeaderTimeout: 10 * time.Second}
	slog.Info("order 就绪", "addr", ":8084")
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server 退出", "err", err)
	}
}

// migrate 建表幂等（与 product 服务同库，ALTER 兼容追加）。
func migrate(ctx context.Context, db *pgxpool.Pool) error {
	_, err := db.Exec(ctx, `CREATE TABLE IF NOT EXISTS orders (
		id          TEXT PRIMARY KEY,
		user_id     TEXT NOT NULL,
		status      TEXT NOT NULL,
		total_cents INT NOT NULL,
		items       JSONB NOT NULL,
		created_at  TIMESTAMPTZ DEFAULT now(),
		updated_at  TIMESTAMPTZ DEFAULT now()
	)`)
	return err
}

// buildMux 构造路由（db/np 参数化供测试注入 nil——鉴权层不触 DB）。
func buildMux(db *pgxpool.Pool, np *natspub.Publisher) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		observ.MetricsHandler().ServeHTTP(w, r)
	})
	mux.HandleFunc("/orders", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			requireToken(handleCreate(db, np))(w, r)
		case http.MethodGet:
			handleList(db)(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/orders/", func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/orders/")
		parts := strings.Split(rest, "/")
		if len(parts) == 1 && r.Method == http.MethodGet {
			handleGet(db)(w, r, parts[0])
			return
		}
		// POST /orders/{id}/{action}
		if len(parts) == 2 && r.Method == http.MethodPost {
			requireToken(handleAction(db, np, parts[0], parts[1]))(w, r)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	})
	return mux
}

// requireToken 变更类端点鉴权：X-Action-Token == env（env 空跳过，本地 dev）。
func requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if actionToken != "" && r.Header.Get("X-Action-Token") != actionToken {
			http.Error(w, "invalid action token", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Paas-Service", observ.ServiceFingerprint())
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// handleCreate 下单：服务端查价 + 单事务扣库存（条件更新防超卖）+ 提交后发事件。
func handleCreate(db *pgxpool.Pool, np *natspub.Publisher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req createReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UserID == "" || len(req.Items) == 0 {
			http.Error(w, "userId 和 items 必填", http.StatusBadRequest)
			return
		}
		for _, it := range req.Items {
			if it.ProductID <= 0 || it.Qty <= 0 {
				http.Error(w, "productId/qty 必须为正", http.StatusBadRequest)
				return
			}
		}
		o, err := createOrder(r.Context(), db, np, req)
		if err != nil {
			if strings.Contains(err.Error(), "库存不足") {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			slog.Error("下单失败", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		orderCreated.Inc()
		orderAmount.Observe(float64(o.TotalCents))
		writeJSON(w, http.StatusCreated, o)
	}
}

// createOrder 事务：锁商品行读现价 → INSERT 订单 → 条件更新扣库存（rows=0 回滚 409）。
func createOrder(ctx context.Context, db *pgxpool.Pool, np *natspub.Publisher, req createReq) (Order, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	items := make([]Item, 0, len(req.Items))
	total := 0
	for _, it := range req.Items {
		var name string
		var price float64
		err := tx.QueryRow(ctx,
			`SELECT name, price FROM products WHERE id=$1 FOR UPDATE`, it.ProductID).Scan(&name, &price)
		if err != nil {
			return Order{}, fmt.Errorf("商品不存在: %d", it.ProductID)
		}
		cents := int(math.Round(price * 100))
		items = append(items, Item{ProductID: it.ProductID, Name: name, Qty: it.Qty, PriceCents: cents})
		total += cents * it.Qty
	}

	o := Order{ID: newOrderID(), UserID: req.UserID, Status: StatusPending, TotalCents: total, Items: items}
	itemsJSON, _ := json.Marshal(items)
	if _, err := tx.Exec(ctx,
		`INSERT INTO orders(id,user_id,status,total_cents,items) VALUES($1,$2,$3,$4,$5)`,
		o.ID, o.UserID, o.Status, o.TotalCents, itemsJSON); err != nil {
		return Order{}, err
	}
	for _, it := range items {
		tag, err := tx.Exec(ctx,
			`UPDATE products SET stock = stock - $1 WHERE id = $2 AND stock >= $1`, it.Qty, it.ProductID)
		if err != nil {
			return Order{}, err
		}
		if tag.RowsAffected() == 0 {
			return Order{}, fmt.Errorf("库存不足: 商品 %s 剩余不足 %d", it.Name, it.Qty)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, err
	}
	publishEvent(np, "order.created", o)
	// 记录行版本（GET 返回用 updated_at 由 DB 默认值生成，此处回读省略——create 响应够用）。
	return o, nil
}

// handleAction 统一动作端点：事务内 FOR UPDATE 锁订单 → 状态机校验 →
// refund/close 回补库存 → 更新状态 → 提交后发事件。
func handleAction(db *pgxpool.Pool, np *natspub.Publisher, id, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := map[string]bool{"pay": true, "ship": true, "confirm": true, "refund": true, "close": true}[action]; !ok {
			http.Error(w, "unknown action", http.StatusNotFound)
			return
		}
		o, err := applyAction(r.Context(), db, np, id, action)
		if err != nil {
			if strings.Contains(err.Error(), "不允许") || strings.Contains(err.Error(), "不存在") {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			slog.Error("订单动作失败", "id", id, "action", action, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, o)
	}
}

// applyAction sweeper 复用（close）；回补库存按 items 快照数量。
func applyAction(ctx context.Context, db *pgxpool.Pool, np *natspub.Publisher, id, action string) (Order, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return Order{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var o Order
	var itemsJSON []byte
	err = tx.QueryRow(ctx,
		`SELECT id,user_id,status,total_cents,items,created_at,updated_at FROM orders WHERE id=$1 FOR UPDATE`,
		id).Scan(&o.ID, &o.UserID, &o.Status, &o.TotalCents, &itemsJSON, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return Order{}, fmt.Errorf("订单不存在: %s", id)
	}
	_ = json.Unmarshal(itemsJSON, &o.Items)

	to, err := Transition(o.Status, action)
	if err != nil {
		return Order{}, err
	}
	// 退款/关单回补库存（spec §3：refunded/closed 均回补）。
	if action == "refund" || action == "close" {
		for _, it := range o.Items {
			if _, err := tx.Exec(ctx,
				`UPDATE products SET stock = stock + $1 WHERE id = $2`, it.Qty, it.ProductID); err != nil {
				return Order{}, err
			}
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE orders SET status=$1, updated_at=now() WHERE id=$2`, to, id); err != nil {
		return Order{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Order{}, err
	}
	o.Status = to
	publishEvent(np, "order."+action, o)
	return o, nil
}

// publishEvent 事务提交后广播（at-most-once 可接受，spec §3）。
func publishEvent(np *natspub.Publisher, typ string, o Order) {
	if np == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"type": typ, "orderId": o.ID, "userId": o.UserID, "status": o.Status,
		"amount": o.TotalCents, "at": time.Now().Format(time.RFC3339),
	})
	np.Publish("shop-events", payload)
}

func handleList(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(r.Context(),
			`SELECT id,user_id,status,total_cents,items,created_at,updated_at FROM orders`+
				whereStatus(r.URL.Query().Get("status"))+` ORDER BY created_at DESC LIMIT 100`,
			statusArg(r.URL.Query().Get("status")))
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		list := []Order{}
		for rows.Next() {
			o, err := scanOrder(rows)
			if err != nil {
				continue
			}
			list = append(list, o)
		}
		writeJSON(w, http.StatusOK, list)
	}
}

func whereStatus(status string) string {
	if status != "" {
		return ` WHERE status=$1`
	}
	return ``
}
func statusArg(status string) any {
	if status != "" {
		return status
	}
	return nil
}

func handleGet(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request, id string) {
		row := db.QueryRow(r.Context(),
			`SELECT id,user_id,status,total_cents,items,created_at,updated_at FROM orders WHERE id=$1`, id)
		o, err := scanOrder(row)
		if err != nil {
			http.Error(w, "订单不存在", http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, o)
	}
}

// scanner pgx.Row/Rows 共同接口（Get 单行 / List 多行复用）。
type scanner interface{ Scan(dest ...any) error }

func scanOrder(s scanner) (Order, error) {
	var o Order
	var itemsJSON []byte
	if err := s.Scan(&o.ID, &o.UserID, &o.Status, &o.TotalCents, &itemsJSON, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return Order{}, err
	}
	_ = json.Unmarshal(itemsJSON, &o.Items)
	return o, nil
}

// maskDSN 日志脱敏（与 product 服务同款）。
func maskDSN(dsn string) string {
	if i := strings.Index(dsn, "@"); i > 0 {
		return dsn[i:]
	}
	return dsn
}
```

- [ ] **Step 4: 跑测试确认通过 + vet**

```bash
cd examples && go test ./paas-shop/order/ -v && go vet ./paas-shop/order/
```
预期：3 个测试 PASS。

- [ ] **Step 5: 本地手工冒烟（可选，有本地 PG 时）**

```bash
cd examples/paas-shop/order && PAAS_DB=postgres://... go run . &
curl -s localhost:8084/healthz
curl -s -X POST localhost:8084/orders -H 'Content-Type: application/json' \
  -d '{"userId":"u1","items":[{"productId":1,"qty":1}]}'   # 预期 201（无需 token，env 未配）
```

- [ ] **Step 6: Commit（经用户授权后）**

```bash
git add examples/paas-shop/order/
git commit -m "feat(shop): order 服务——事务扣库存/状态机/事件广播/业务指标/Action-Token"
```

---

### Task 3: sweeper CronJob（超时关单）

**Files:**
- Create: `examples/paas-shop/sweeper/main.go`

**Interfaces:**
- Consumes: Task 2 的 `applyAction(ctx, db, np, id, "close")`（**注意：applyAction 在 order 包内，sweeper 是独立 main 包不能 import——改为 sweeper 内嵌同款 SQL 逻辑**。裁决：sweeper 独立实现扫描+关单（约 60 行），不复用 order 包符号，避免 examples 内跨 main 包导出重构）。
- env：`DATABASE_URL`（必填）、`NATS_URL`、`ORDER_TTL_MINUTES`（默认 30；test 部署配 2 演示立等可取）、`PAAS_ORDER_ACTION_TOKEN`（直调 order API 不需要——sweeper 直连 DB，不走 HTTP）。

- [ ] **Step 1: 实现（CronJob 单次跑完退出，参照 statsworker 模式）**

```go
// examples/paas-shop/sweeper/main.go
// sweeper：定时关单 CronJob——扫 pending 且超 TTL 的订单，事务内置 closed + 回补库存 + 发 order.closed。
// 平台 CronJob 语义：单次跑完退出（schedule 控制周期）；ORDER_TTL_MINUTES test=2（演示立等可取）/prod=30。
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aitoys/paas-examples/paas-shop/internal/natspub"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		slog.Error("DATABASE_URL 必填")
		os.Exit(1)
	}
	ttl := 30 * time.Minute
	if v := os.Getenv("ORDER_TTL_MINUTES"); v != "" {
		if d, err := time.ParseDuration(v + "m"); err == nil {
			ttl = d
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		slog.Error("PG 连接失败", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	np := natspub.Connect(os.Getenv("NATS_URL"))

	n, err := sweep(ctx, db, np, ttl)
	if err != nil {
		slog.Error("关单失败", "err", err)
		os.Exit(1)
	}
	slog.Info("关单完成", "closed", n, "ttl", ttl)
}

// sweep 扫描超时 pending 订单逐个事务关闭（复用 order.Transition 同款语义：仅 pending 可 close）。
func sweep(ctx context.Context, db *pgxpool.Pool, np *natspub.Publisher, ttl time.Duration) (int, error) {
	rows, err := db.Query(ctx,
		`SELECT id FROM orders WHERE status='pending' AND created_at < now() - $1::interval`, ttl.String())
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	closed := 0
	for _, id := range ids {
		if err := closeOrder(ctx, db, np, id); err != nil {
			slog.Warn("单笔关单失败（跳过）", "id", id, "err", err)
			continue
		}
		closed++
	}
	return closed, nil
}

// closeOrder 事务：锁订单校验 pending → 回补库存 → 置 closed → 提交后发事件。
// （独立于 order 服务实现：CronJob 直连 DB，不经 HTTP/Action-Token。）
func closeOrder(ctx context.Context, db *pgxpool.Pool, np *natspub.Publisher, id string) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var status string
	var totalCents int
	var itemsJSON []byte
	if err := tx.QueryRow(ctx,
		`SELECT status,total_cents,items FROM orders WHERE id=$1 FOR UPDATE`, id).
		Scan(&status, &totalCents, &itemsJSON); err != nil {
		return err
	}
	if status != "pending" { // 已被支付/退款并发变更，跳过
		return nil
	}
	var items []struct {
		ProductID int `json:"productId"`
		Qty       int `json:"qty"`
	}
	_ = json.Unmarshal(itemsJSON, &items)
	for _, it := range items {
		if _, err := tx.Exec(ctx,
			`UPDATE products SET stock = stock + $1 WHERE id = $2`, it.Qty, it.ProductID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE orders SET status='closed', updated_at=now() WHERE id=$1`, id); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{
		"type": "order.closed", "orderId": id, "status": "closed",
		"amount": totalCents, "at": time.Now().Format(time.RFC3339),
	})
	np.Publish("shop-events", payload)
	return nil
}
```

> **关单幂等**：status 非 pending 直接跳过（返回 nil 不计 closed）。

- [ ] **Step 2: 编译 + vet**

```bash
cd examples && go build ./paas-shop/sweeper/ && go vet ./paas-shop/sweeper/
```
预期：无输出（成功）。CronJob 无单测（纯 SQL 编排，e2e Task 12 覆盖）。

- [ ] **Step 3: Commit（经用户授权后）**

```bash
git add examples/paas-shop/sweeper/
git commit -m "feat(shop): sweeper 超时关单 CronJob——pending 超 TTL 事务关单+回补库存+事件"
```

---

### Task 4: bff 改造（order 代理 + 双消费组 + /api/stats）

**Files:**
- Modify: `examples/paas-shop/bff/main.go`
- Test: `examples/paas-shop/bff/bff_test.go`（追加测试）

**Interfaces:**
- Consumes: Task 2 的 order API（`/orders*`）；dpdisc 不变（`paas-shop-order` 传名即可命中基线/泳道）。
- Produces（frontend Task 6 消费）：
  - `ANY /api/orders*` 代理 order 服务（POST 附加 `X-Action-Token`，env `PAAS_ORDER_ACTION_TOKEN` 未配则不附加）
  - `GET /api/stats` → `{"orders":N,"gmvCents":N,"byStatus":{"pending":N,...}}`（stats-consumer 内存累计）
- shopEvent 结构扩展（前端 Task 6 消费）：追加 `OrderID string`、`Amount int`、`Status string`、`UserID string` 字段（json omitempty，商品事件零影响）。

- [ ] **Step 1: 写失败测试（追加到 bff_test.go）**

```go
// bff_test.go 追加

// TestStatsEndpoint /api/stats 返回 stats-consumer 累计结构（初始零值也 200）。
func TestStatsEndpoint(t *testing.T) {
	stats := newStatsAgg()
	ts := httptest.NewServer(buildMuxWithStats(newEventRing(10), stats))
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/stats")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/stats 应 200，got %d", resp.StatusCode)
	}
	var s map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"orders", "gmvCents", "byStatus"} {
		if _, ok := s[key]; !ok {
			t.Errorf("stats 应含 %s，got %v", key, s)
		}
	}
}

// TestStatsAggConsume order.created 事件累计订单数/GMV/状态分布。
func TestStatsAggConsume(t *testing.T) {
	a := newStatsAgg()
	a.consume([]byte(`{"type":"order.created","orderId":"ORD-1","amount":9900,"status":"pending"}`))
	a.consume([]byte(`{"type":"order.paid","orderId":"ORD-1","amount":9900,"status":"paid"}`))
	if a.snapshot().Orders != 1 {
		t.Fatalf("order.created 应 +1，got %d", a.snapshot().Orders)
	}
	if a.snapshot().GMVCents != 9900 {
		t.Fatalf("GMV 应 9900，got %d", a.snapshot().GMVCents)
	}
	if a.snapshot().ByStatus["paid"] != 1 {
		t.Fatalf("状态分布应 paid=1，got %v", a.snapshot().ByStatus)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
cd examples && go test ./paas-shop/bff/
```
预期：FAIL（`newStatsAgg`/`buildMuxWithStats` 未定义）。

- [ ] **Step 3: 实现 bff 改动**

main.go 逐处修改（完整代码块）：

**(3a) fallback 加 order + env 覆盖**（`var fallback` 块与 main env 段）：

```go
	fallback = map[string]string{
		"product":   "http://paas-shop-product:8081",
		"recommend": "http://paas-shop-recommend:8082",
		"chatbot":   "http://paas-shop-chatbot:8083",
		"order":     "http://paas-shop-order:8084",
	}
```

main() env 段追加：

```go
	if v := os.Getenv("ORDER_SERVICE_URL"); v != "" {
		fallback["order"] = v
	}
```

**(3b) shopEvent 扩字段**：

```go
// shopEvent 是 shop-events 主题的一条事件（商品变更 + 订单生命周期）。
type shopEvent struct {
	Type      string    `json:"type"`
	ProductID int       `json:"productId"`
	Name      string    `json:"name"`
	Category  string    `json:"category"`
	OrderID   string    `json:"orderId,omitempty"` // 订单事件
	UserID    string    `json:"userId,omitempty"`
	Status    string    `json:"status,omitempty"`
	Amount    int       `json:"amount,omitempty"` // 订单金额（分）
	At        time.Time `json:"at"`
	ReceivedAt time.Time `json:"receivedAt"`
}
```

**(3c) statsAgg 聚合器 + newStatsAgg**（新类型，文件尾追加）：

```go
// statsAgg stats-consumer 消费组的内存累计（GMV 演示：常驻进程才能不丢事件，spec §4）。
type statsAgg struct {
	mu       sync.Mutex
	Orders   int            `json:"orders"`
	GMVCents int            `json:"gmvCents"`
	ByStatus map[string]int `json:"byStatus"`
}

func newStatsAgg() *statsAgg { return &statsAgg{ByStatus: map[string]int{}} }

// consume 处理一条 shop-events 消息：order.created 累计订单数/GMV，状态事件更新分布。
func (a *statsAgg) consume(data []byte) {
	var e shopEvent
	if err := json.Unmarshal(data, &e); err != nil || !strings.HasPrefix(e.Type, "order.") {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch e.Type {
	case "order.created":
		a.Orders++
		a.GMVCents += e.Amount
	}
	if e.Status != "" {
		a.ByStatus[e.Status]++
	}
}

// snapshot 深拷贝防 race（返回给外部的对象一律 clone）。
func (a *statsAgg) snapshot() statsAgg {
	a.mu.Lock()
	defer a.mu.Unlock()
	byStatus := make(map[string]int, len(a.ByStatus))
	for k, v := range a.ByStatus {
		byStatus[k] = v
	}
	return statsAgg{Orders: a.Orders, GMVCents: a.GMVCents, ByStatus: byStatus}
}
```

**(3d) buildMux → buildMuxWithStats**（保留 buildMux 兼容旧测试，内部委托）：

```go
// buildMux 兼容入口（旧测试引用；stats 用空聚合器）。
func buildMux(events *eventRing) *http.ServeMux {
	return buildMuxWithStats(events, newStatsAgg())
}

// buildMuxWithStats 构造 bff 路由（events 通知中心 + stats 经营统计）。
func buildMuxWithStats(events *eventRing, stats *statsAgg) *http.ServeMux {
	mux := http.NewServeMux()
	// ...既有 /healthz /metrics /api/products* /api/recommend /images/ /api/chat /api/events 原样保留...
	mux.HandleFunc("/api/orders", proxyOrder("/orders"))
	mux.HandleFunc("/api/orders/", proxyOrderPrefix())
	mux.HandleFunc("/api/stats", func(w http.ResponseWriter, _ *http.Request) {
		writeJSONStats(w, stats.snapshot())
	})
	return mux
}

// proxyOrder 订单代理：POST 追加 X-Action-Token（前端不感知，spec §3）。
func proxyOrder(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		proxyOrderReq(w, r, path)
	}
}

// proxyOrderPrefix /api/orders/{id}[/{action}] → order /orders/{id}[/{action}]。
func proxyOrderPrefix() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		proxyOrderReq(w, r, "")
	}
}

// proxyOrderReq 统一订单转发（svcURL 泳道感知 + token 注入 + 指纹头）。
func proxyOrderReq(w http.ResponseWriter, r *http.Request, fixedPath string) {
	target := svcURL(r, "order") + "/orders"
	if fixedPath == "" {
		target += strings.TrimPrefix(r.URL.Path, "/api/orders")
	} else {
		target += ""
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	req.URL.RawQuery = r.URL.RawQuery
	copyHeaders(req.Header, r.Header)
	if tok := os.Getenv("PAAS_ORDER_ACTION_TOKEN"); tok != "" && r.Method != http.MethodGet {
		req.Header.Set("X-Action-Token", tok)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		slog.Error("order 转发失败", "target", target, "err", err)
		http.Error(w, "order unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	copyHeaders(w.Header(), resp.Header)
	w.Header().Set("X-Paas-Lane", observ.LaneOrBase(r.Context()))
	w.Header().Set("X-Paas-Bff", observ.ServiceFingerprint())
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func writeJSONStats(w http.ResponseWriter, s statsAgg) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s)
}
```

> 注意：`/api/orders`（无尾斜杠）走 `proxyOrder("/orders")`：POST /api/orders → order /orders；GET 列表同路径。`/api/orders/`（有尾斜杠）走 prefix 透传子路径。Go ServeMux 最长前缀匹配区分两者。

**(3e) main() 组装**：

```go
	events := newEventRing(100)
	stats := newStatsAgg()
	subscribeShopEvents(events, stats)
	h := observ.Recover(observ.Handler("bff", observ.LaneMiddleware(buildMuxWithStats(events, stats))))
```

**(3f) subscribeShopEvents 双消费组**：

```go
// subscribeShopEvents 订阅 shop-events：bff-consumer（通知中心环形缓冲）+
// stats-consumer（GMV 内存累计）两个独立 QueueSubscribe group，各自全量消费（spec §4）。
func subscribeShopEvents(events *eventRing, stats *statsAgg) {
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		return
	}
	nc, err := nats.Connect(natsURL)
	if err != nil {
		slog.Warn("NATS 连接失败，/api/events 与 /api/stats 将返回空", "err", err)
		return
	}
	if _, err := nc.QueueSubscribe("shop-events", "bff-consumer", func(msg *nats.Msg) {
		var e shopEvent
		if err := json.Unmarshal(msg.Data, &e); err != nil {
			return
		}
		e.ReceivedAt = time.Now()
		events.push(e)
	}); err != nil {
		slog.Warn("bff-consumer 订阅失败", "err", err)
	}
	if _, err := nc.QueueSubscribe("shop-events", "stats-consumer", func(msg *nats.Msg) {
		stats.consume(msg.Data)
	}); err != nil {
		slog.Warn("stats-consumer 订阅失败", "err", err)
	}
	nc.Flush()
	slog.Info("bff 已订阅 shop-events（bff-consumer + stats-consumer 双消费组）")
}
```

- [ ] **Step 4: 跑全部 bff 测试确认通过**

```bash
cd examples && go test ./paas-shop/bff/ -v
```
预期：既有测试 + 2 个新测试全 PASS。

- [ ] **Step 5: Commit（经用户授权后）**

```bash
git add examples/paas-shop/bff/
git commit -m "feat(shop): bff 订单代理+token 注入+双消费组(stats GMV)+/api/stats"
```

---

### Task 5: mcp 真实化（删内存假订单）

**Files:**
- Modify: `examples/paas-shop/mcp/main.go`

**Interfaces:**
- Consumes: order API `GET /orders/{id}` / `POST /orders/{id}/refund`（HTTP 直调，带 `X-Action-Token`）。
- env：`ORDER_URL`（默认 `http://paas-shop-order:8084`）、`PAAS_ORDER_ACTION_TOKEN`。
- Produces: MCP 工具 `query_order` / `refund_order` 签名不变（workflow `shop-ticket-triage` 与退款 workflow 消费，参数 `orderId`）。

- [ ] **Step 1: 删除内存订单数据**

定位 `examples/paas-shop/mcp/main.go` 中 `ORD-1001`~`ORD-1003` 的内存 map（orders/refunds 变量及其初始化），整段删除。

- [ ] **Step 2: query_order/refund_order 改 HTTP 直调**

在 mcp/main.go 中新增（替换原内存实现；toolSchemas 的 args 定义不变）：

```go
// orderURL order 服务地址（默认集群内基线 Service；泳道无关——mcp 是平台 AI 层单实例，
// 退款/query 演示走 test 订单，spec §6 环境边界）。
func orderURL() string {
	if v := os.Getenv("ORDER_URL"); v != "" {
		return v
	}
	return "http://paas-shop-order:8084"
}

// callOrder 调 order 服务（GET 查询 / POST 动作带 X-Action-Token）。
func callOrder(ctx context.Context, method, path string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, orderURL()+path, nil)
	if err != nil {
		return 0, nil, err
	}
	if method != http.MethodGet {
		if tok := os.Getenv("PAAS_ORDER_ACTION_TOKEN"); tok != "" {
			req.Header.Set("X-Action-Token", tok)
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}
```

`query_order` 工具实现（tools/call 分发处）：

```go
	// query_order：HTTP 直调 order 服务查真实订单（404 归一为工具错误消息）。
	code, body, err := callOrder(ctx, http.MethodGet, "/orders/"+argString(args, "orderId"))
	if err != nil || code == http.StatusNotFound {
		result := "订单不存在或查询失败: " + argString(args, "orderId")
		// 按 mcp 既有错误返回约定输出（isError 或 content text，跟随现文件风格）
		return toolText(ctx, result)
	}
	return toolText(ctx, string(body))
```

`refund_order` 工具实现：

```go
	// refund_order：HTTP 直调 order 退款（状态机拒绝 409 时透出原因）。
	code, body, err := callOrder(ctx, http.MethodPost, "/orders/"+argString(args, "orderId")+"/refund")
	if err != nil {
		return toolText(ctx, "退款请求失败: "+err.Error())
	}
	if code != http.StatusOK {
		return toolText(ctx, "退款被拒绝: "+string(body))
	}
	return toolText(ctx, string(body))
```

> `toolText` 若 mcp 现文件无此辅助函数，按现文件的 tools/call 返回构造方式内联（读 mcp/main.go 279 行的既有返回模式照抄格式）；`argString` 已有（string/float64 兼容）。

- [ ] **Step 3: 编译 + 既有测试回归**

```bash
cd examples && go build ./paas-shop/mcp/ && go vet ./paas-shop/mcp/ && go test ./paas-shop/...
```
预期：全部 PASS（mcp 无单测，编译即验证；e2e 在 Task 12）。

- [ ] **Step 4: Commit（经用户授权后）**

```bash
git add examples/paas-shop/mcp/
git commit -m "feat(shop): mcp query_order/refund_order 真实化——HTTP 直调 order 服务"
```

---

### Task 6: frontend（AdminPanel + 环境徽标 + 下单支付 + hostname 染色）

**Files:**
- Modify: `examples/paas-shop/frontend/src/types.ts`（追加 Order/Stats 类型）
- Modify: `examples/paas-shop/frontend/src/lane.ts`（hostname 环境推断）
- Modify: `examples/paas-shop/frontend/src/App.vue`（环境徽标 + 泳道横幅 + AdminPanel 视图切换）
- Create: `examples/paas-shop/frontend/src/components/AdminPanel.vue`
- Modify: `examples/paas-shop/frontend/src/components/DetailDrawer.vue`（下单/支付按钮）

**Interfaces:**
- Consumes: bff `GET /api/orders*`、`POST /api/orders/{id}/pay|ship|close`（经 laneFetch 自动带泳道）、`GET /api/stats`、`GET /api/events`、product `GET /api/products`、`POST /api/products`。
- Produces: `lane.ts` 导出 `detectEnv(hostname): {kind:'prod'|'test'|'lane', lane?:string}`。

- [ ] **Step 1: types.ts 追加类型**

```ts
// types.ts 追加
export interface OrderItem {
  productId: number
  name: string
  qty: number
  priceCents: number
}
export interface Order {
  id: string
  userId: string
  status: 'pending' | 'paid' | 'shipped' | 'completed' | 'refunded' | 'closed'
  totalCents: number
  items: OrderItem[]
  createdAt: string
  updatedAt: string
}
export interface ShopStats {
  orders: number
  gmvCents: number
  byStatus: Record<string, number>
}
```

- [ ] **Step 2: lane.ts 追加 hostname 环境推断**

```ts
// lane.ts 追加（现有 laneFetch 不动）
// detectEnv 按 hostname 推断环境：生产/测试/泳道（三套域名方案，spec §10）。
// shop.example.com → prod；shop.test.example.com → test；<lane>.shop.test.example.com → lane。
export function detectEnv(hostname: string): { kind: 'prod' | 'test' | 'lane'; lane?: string } {
  const host = hostname.split(':')[0]
  if (host === 'shop.example.com') return { kind: 'prod' }
  if (host === 'shop.test.example.com') return { kind: 'test' }
  const m = host.match(/^([a-z0-9-]+)\.shop\.test\.example\.com$/)
  if (m) return { kind: 'lane', lane: m[1] }
  return { kind: 'test' } // 本地 dev/未知域名按测试基线
}
```

同时扩展既有 lane 提取逻辑：若 `detectEnv().kind === 'lane'`，`currentLane()` 返回该 lane（hostname 首段优先于手动输入）。

- [ ] **Step 3: App.vue 环境徽标 + 视图切换**

App.vue 顶栏（现有 header 泳道输入框旁）追加：

```vue
<script setup lang="ts">
// 追加导入与状态
import AdminPanel from './components/AdminPanel.vue'
import { detectEnv } from './lane'
const env = detectEnv(window.location.hostname)
const view = ref<'shop' | 'admin'>('shop')
</script>

<template>
  <!-- header 内追加：环境徽标（红生产/绿测试/蓝泳道，spec §11）+ 运营入口 -->
  <span class="env-badge" :class="env.kind">{{ env.kind === 'lane' ? `泳道 ${env.lane}` : env.kind === 'prod' ? '生产' : '测试' }}</span>
  <button @click="view = view === 'shop' ? 'admin' : 'shop'">
    {{ view === 'shop' ? '运营视图' : '返回商店' }}
  </button>
  <!-- 泳道横幅（仅 lane 环境） -->
  <div v-if="env.kind === 'lane'" class="lane-banner">泳道演示环境 {{ env.lane }} —— 流量已染色</div>
  <!-- 主区切换 -->
  <AdminPanel v-if="view === 'admin'" />
  <template v-else><!-- 既有商品/搜索/聊天主体 --></template>
</template>

<style scoped>
.env-badge { padding: 2px 10px; border-radius: 10px; font-size: 12px; color: #fff; }
.env-badge.prod { background: #e5484d; }
.env-badge.test { background: #30a46c; }
.env-badge.lane { background: #3e63dd; }
.lane-banner { background: #eef4ff; color: #3e63dd; padding: 6px 16px; font-size: 13px; }
</style>
```

- [ ] **Step 4: AdminPanel.vue 三 tab（概览/订单/商品）**

```vue
<!-- components/AdminPanel.vue：运营视图（spec §11）。无 router/pinia，跟随现有纯 ref 模式。 -->
<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { laneFetch } from '../lane'
import type { Order, ShopStats, Product } from '../types'

const tab = ref<'overview' | 'orders' | 'products'>('overview')
const stats = ref<ShopStats>({ orders: 0, gmvCents: 0, byStatus: {} })
const orders = ref<Order[]>([])
const statusFilter = ref('')
const products = ref<Product[]>([])
let timer: number | undefined

async function refresh() {
  const [s, o] = await Promise.all([
    laneFetch('/api/stats').then(r => r.json()),
    laneFetch('/api/orders' + (statusFilter.value ? `?status=${statusFilter.value}` : '')).then(r => r.json()),
  ])
  stats.value = s
  orders.value = o
}

async function act(id: string, action: 'ship' | 'close') {
  await laneFetch(`/api/orders/${id}/${action}`, { method: 'POST' })
  await refresh()
}

// 退款引导层：运营动作发生在 PaaS 控制台工作流（spec §6 叙事），这里只复制订单号。
const refundHint = ref('')
function copyRefund(id: string) {
  navigator.clipboard?.writeText(id)
  refundHint.value = `订单号 ${id} 已复制——请到平台控制台「智能体 → 工作流」运行 shop-refund-approval 完成退款审批`
}

async function loadProducts() {
  products.value = await laneFetch('/api/products').then(r => r.json())
}

onMounted(() => {
  refresh(); loadProducts()
  timer = window.setInterval(refresh, 10000) // 10s 轮询，onUnmounted 清理
})
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <div class="admin">
    <nav>
      <button :class="{ active: tab === 'overview' }" @click="tab = 'overview'">概览</button>
      <button :class="{ active: tab === 'orders' }" @click="tab = 'orders'">订单</button>
      <button :class="{ active: tab === 'products' }" @click="tab = 'products'">商品</button>
    </nav>

    <section v-if="tab === 'overview'">
      <div class="cards">
        <div class="card"><b>{{ stats.orders }}</b><span>订单总数</span></div>
        <div class="card"><b>¥{{ (stats.gmvCents / 100).toFixed(2) }}</b><span>GMV</span></div>
        <div class="card" v-for="(n, s) in stats.byStatus" :key="s"><b>{{ n }}</b><span>{{ s }}</span></div>
      </div>
    </section>

    <section v-if="tab === 'orders'">
      <select v-model="statusFilter" @change="refresh">
        <option value="">全部状态</option>
        <option v-for="s in ['pending','paid','shipped','completed','refunded','closed']" :key="s" :value="s">{{ s }}</option>
      </select>
      <p v-if="refundHint" class="hint">{{ refundHint }}</p>
      <table>
        <tr><th>订单号</th><th>状态</th><th>金额</th><th>商品</th><th>操作</th></tr>
        <tr v-for="o in orders" :key="o.id">
          <td>{{ o.id }}</td>
          <td><span class="st" :class="o.status">{{ o.status }}</span></td>
          <td>¥{{ (o.totalCents / 100).toFixed(2) }}</td>
          <td>{{ o.items.map(i => `${i.name}×${i.qty}`).join('、') }}</td>
          <td>
            <button v-if="o.status === 'paid'" @click="act(o.id, 'ship')">发货</button>
            <button v-if="o.status === 'pending'" @click="act(o.id, 'close')">关单</button>
            <button v-if="o.status === 'paid' || o.status === 'shipped'" @click="copyRefund(o.id)">退款</button>
          </td>
        </tr>
      </table>
    </section>

    <section v-if="tab === 'products'">
      <table>
        <tr><th>商品</th><th>价格</th><th>库存</th><th>分类</th></tr>
        <tr v-for="p in products" :key="p.id">
          <td>{{ p.name }}</td><td>¥{{ p.price }}</td><td>{{ p.stock }}</td><td>{{ p.category }}</td>
        </tr>
      </table>
    </section>
  </div>
</template>
```

> 库存行内编辑/上新按钮：读 DetailDrawer 现有 POST /api/products 模式补一个「上新」表单行（名称/价格/库存/分类 + 提交），样式跟随文件既有风格。此处为最小实现——若执行时发现 Product 类型字段名不同（如 `image_url`），以 `types.ts` 现有定义为准对齐。

- [ ] **Step 5: DetailDrawer.vue 下单/支付**

抽屉底部按钮区追加（复用既有 laneFetch 与样式类）：

```vue
<script setup lang="ts">
// 追加
import { laneFetch } from '../lane'
const orderMsg = ref('')
async function buy() {
  const resp = await laneFetch('/api/orders', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ userId: 'guest', items: [{ productId: props.product.id, qty: 1 }] }),
  })
  orderMsg.value = resp.ok ? '下单成功，请在运营视图或订单列表支付' : `下单失败：${await resp.text()}`
}
async function payLatest() {
  const list = await laneFetch('/api/orders?status=pending').then(r => r.json())
  if (!list.length) { orderMsg.value = '无待支付订单'; return }
  const resp = await laneFetch(`/api/orders/${list[0].id}/pay`, { method: 'POST' })
  orderMsg.value = resp.ok ? `订单 ${list[0].id} 已支付` : '支付失败'
}
</script>
<template>
  <!-- 既有详情内容后追加 -->
  <div class="actions">
    <button @click="buy">立即下单</button>
    <button @click="payLatest">支付最新订单</button>
    <p v-if="orderMsg">{{ orderMsg }}</p>
  </div>
</template>
```

- [ ] **Step 6: 构建验证**

```bash
cd examples/paas-shop/frontend && npm run build
```
预期：构建成功零 TS 错误（examples 前端为独立 vite 项目，以其实际 package.json scripts 为准——若命令不同按 `npx vite build`）。

- [ ] **Step 7: Commit（经用户授权后）**

```bash
git add examples/paas-shop/frontend/
git commit -m "feat(shop): AdminPanel 三 tab + 环境徽标/泳道横幅 + 下单支付 + hostname 染色"
```

---

### Task 7: setup 脚本 part 1——共享基础（test 侧资源 + Secret + mq 登记）

**Files:**
- Create: `examples/paas-shop/setup.sh`（本 Task 建骨架 + part1；Task 8/9 追加函数）

**Interfaces:**
- env：`PAAS_API`（默认 `http://paas.k8s.dd`）、`PAAS_TOKEN`（默认 `sk-acme-admin`）、`APP_ID`（默认查名 `paas-shop` 的应用 ID，脚本自动解析）。
- Produces: 幂等 setup 脚本（重复执行不重复创建——先查后建）。

- [ ] **Step 1: 脚本骨架 + 工具函数 + part1**

```bash
#!/usr/bin/env bash
# setup.sh —— paas-shop 演示资源初始化（spec §9 七步 + 演示编排）。
# 幂等：先查后建，重复执行安全。域名统一 *.example.com（对外演示材料不泄漏内网地址）；
# 本机访问演示域名：curl -H 'Host: shop.example.com' http://<集群入口>/ 或 hosts 解析。
set -euo pipefail

PAAS_API="${PAAS_API:-http://paas.k8s.dd}"
PAAS_TOKEN="${PAAS_TOKEN:-sk-acme-admin}"
APP_ID="${APP_ID:-}"
TENANT_ID="t-acme"
ENV_TEST="env-acme-test"
ENV_PROD="env-acme-prod-bj"
ACTION_TOKEN="${ACTION_TOKEN:-paas-shop-order-token-2026}"

api() { curl -sS -H "Authorization: Bearer $PAAS_TOKEN" "$@"; }
jget() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null || true; }

# resolve_app_id 按名解析应用 ID（APP_ID 未显式指定时）。
resolve_app_id() {
  if [ -z "$APP_ID" ]; then
    APP_ID=$(api "$PAAS_API/api/applications" | jget "[0]['id']") # 列表首项=paas-shop（单演示应用）
  fi
  echo "APP_ID=$APP_ID"
}

# ensure_data_service 幂等建数据服务（同名跳过）。
ensure_data_service() { # kind name engine envId extra_spec_json
  local exists
  exists=$(api "$PAAS_API/api/dataservices?kind=$1" | jget "[? \"$1\"=='x'][0]['id']" 2>/dev/null)
  # 简化：按名过滤
  exists=$(api "$PAAS_API/api/dataservices?kind=$1" | python3 -c "
import sys,json
for d in json.load(sys.stdin)['data']:
    if d['name']=='$2': print(d['id']); break" 2>/dev/null || true)
  if [ -n "$exists" ]; then echo "  已存在 $2 ($exists)"; return 0; fi
  api -X POST "$PAAS_API/api/dataservices" -H "Content-Type: application/json" \
    -d "{\"kind\":\"$1\",\"name\":\"$2\",\"envId\":\"$4\",\"spec\":{\"engine\":\"$3\"$5}}" | jget "['data']['id']" >/dev/null && echo "  已创建 $2"
}

# ensure_workload 幂等建 workload（按 app+env+lane+service 查重）。
ensure_workload() { # json_file
  local payload; payload=$(cat "$1")
  local name; name=$(echo "$payload" | jget "['name']")
  local exists
  exists=$(api "$PAAS_API/api/applications/$APP_ID/workloads" | python3 -c "
import sys,json
for w in json.load(sys.stdin)['data']:
    if w['name']=='$name': print('yes'); break" 2>/dev/null || true)
  if [ "$exists" = "yes" ]; then echo "  已存在 workload $name"; return 0; fi
  api -X POST "$PAAS_API/api/applications/$APP_ID/workloads" \
    -H "Content-Type: application/json" -d "$payload" >/dev/null && echo "  已创建 workload $name"
}

# ensure_appconfig 幂等 upsert 应用配置（app×env×key 查重）。
ensure_appconfig() { # envId key value type
  local exists
  exists=$(api "$PAAS_API/api/applications/$APP_ID/configs?envId=$1" | python3 -c "
import sys,json
for c in json.load(sys.stdin)['data']:
    if c['key']=='$2': print('yes'); break" 2>/dev/null || true)
  local body="{\"appId\":\"$APP_ID\",\"envId\":\"$1\",\"key\":\"$2\",\"value\":\"$3\",\"type\":\"$4\"}"
  if [ "$exists" = "yes" ]; then
    api -X POST "$PAAS_API/api/applications/$APP_ID/configs" -H "Content-Type: application/json" -d "$body" >/dev/null
    echo "  已更新配置 $2"
  else
    api -X POST "$PAAS_API/api/applications/$APP_ID/configs" -H "Content-Type: application/json" -d "$body" >/dev/null
    echo "  已创建配置 $2"
  fi
}

echo "== part 1: test 侧共享资源（Secret/Action-Token/MQ 登记）=="
resolve_app_id

# 1. 订单操作令牌：appconfig type=secret（静态加密 enc:v1 落库 + 按环境桶注入全部 workload）
ensure_appconfig "$ENV_TEST" "PAAS_ORDER_ACTION_TOKEN" "$ACTION_TOKEN" "secret"

# 2. 平台 Secret 实体（演示掩码管理面；scope=tenant）
sec_exists=$(api "$PAAS_API/api/security/secrets" | python3 -c "
import sys,json
for s in json.load(sys.stdin)['data']:
    if s['name']=='paas-shop-order-action': print('yes'); break" 2>/dev/null || true)
if [ "$sec_exists" != "yes" ]; then
  api -X POST "$PAAS_API/api/security/secrets" -H "Content-Type: application/json" \
    -d "{\"name\":\"paas-shop-order-action\",\"type\":\"secret\",\"scope\":\"tenant\",\"value\":\"$ACTION_TOKEN\",\"desc\":\"paas-shop 订单操作令牌（演示静态加密）\"}" >/dev/null
  echo "  已创建平台 Secret paas-shop-order-action"
fi

# 3. MQ topic + 双消费组登记（管理面对齐真实业务流，spec §4）
#    topic 需挂 shop MQ 实例：先解析 test 环境 mq 实例 ID。
MQ_ID=$(api "$PAAS_API/api/dataservices?kind=mq" | python3 -c "
import sys,json
for d in json.load(sys.stdin)['data']:
    if d['envId']=='$ENV_TEST': print(d['id']); break" 2>/dev/null || true)
if [ -n "$MQ_ID" ]; then
  TOPIC_ID=$(api "$PAAS_API/api/mq-topics?mqId=$MQ_ID" | python3 -c "
import sys,json
for t in json.load(sys.stdin)['data']:
    if t['name']=='shop-events': print(t['id']); break" 2>/dev/null || true)
  if [ -z "$TOPIC_ID" ]; then
    TOPIC_ID=$(api -X POST "$PAAS_API/api/mq-topics" -H "Content-Type: application/json" \
      -d "{\"mqId\":\"$MQ_ID\",\"name\":\"shop-events\"}" | jget "['data']['id']")
    echo "  已登记 topic shop-events"
  fi
  for grp in bff-consumer stats-consumer; do
    cg=$(api "$PAAS_API/api/consumer-groups?topicId=$TOPIC_ID" | python3 -c "
import sys,json
for g in json.load(sys.stdin)['data']:
    if g['name']=='$grp': print('yes'); break" 2>/dev/null || true)
    if [ "$cg" != "yes" ]; then
      api -X POST "$PAAS_API/api/consumer-groups" -H "Content-Type: application/json" \
        -d "{\"topicId\":\"$TOPIC_ID\",\"name\":\"$grp\"}" >/dev/null
      echo "  已登记消费组 $grp"
    fi
  done
else
  echo "  [跳过] test 环境无 mq 实例（shop-nats）——请先确认数据服务"
fi
```

> Topic/ConsumerGroup 请求体字段名以 `/openapi.json` 实际 schema 为准——执行时先 `curl $PAAS_API/openapi.json | python3 -m json.tool | grep -A 20 messaging` 核对，不符则按 schema 调整字段（ messaging.Topic{name, mqId} / ConsumerGroup{topicId, name} 为预期形态）。

- [ ] **Step 2: 语法检查**

```bash
bash -n examples/paas-shop/setup.sh
```
预期：无输出。

- [ ] **Step 3: Commit（经用户授权后）**

```bash
git add examples/paas-shop/setup.sh
git commit -m "feat(shop): setup 脚本 part1——Action-Token/Secret/MQ 双消费组登记"
```

---

### Task 8: setup 脚本 part 2——prod 全套（spec §9 七步）

**Files:**
- Modify: `examples/paas-shop/setup.sh`（追加 part2 函数与主流程）

**Interfaces:**
- Consumes: Task 7 的 `ensure_*` 工具函数；`images` API（解析各服务最新镜像）。
- 前置：**镜像必须已构建推送**（部署 Task 11 先跑 order/sweeper 构建，或复用现有服务镜像）。prod baseline 的 image 从现有 test workload 的同 Service 字段 workload 读取（`imageRef` 优先）。

- [ ] **Step 1: 追加 part2**

```bash
echo "== part 2: prod 全套（spec §9）=="

# ① prod 数据服务（环境级数据隔离）
ensure_data_service db shop-db-prod postgres "$ENV_PROD" ',"version":"15","size_gb":"20"'
ensure_data_service cache shop-redis-prod redis "$ENV_PROD" ',"size_gb":"1"'
ensure_data_service mq shop-nats-prod nats "$ENV_PROD" ',"size_gb":"1"'

# 应用双绑定（bind type 按平台契约：dataservice/<id>）
for ds in shop-db-prod shop-redis-prod shop-nats-prod; do
  DS_ID=$(api "$PAAS_API/api/dataservices" | python3 -c "
import sys,json
for d in json.load(sys.stdin)['data']:
    if d['name']=='$ds': print(d['id']); break" 2>/dev/null || true)
  if [ -n "$DS_ID" ]; then
    api -X POST "$PAAS_API/api/applications/$APP_ID/bindings" -H "Content-Type: application/json" \
      -d "{\"type\":\"dataservice\",\"name\":\"$ds\"}" >/dev/null 2>&1 || true
  fi
done
echo "  双绑定完成（BindingInjector 自动注入 prod 桶 appconfig）"

# ② prod baseline workload：6 服务 + 2 CronJob（显式命名 -prod 后缀规避 Service 撞名，事实 1）
#    image 取自 test 同 Service workload 的 imageRef（同源复用）。
prod_svc_image() { # svc_name
  api "$PAAS_API/api/applications/$APP_ID/workloads" | python3 -c "
import sys,json
for w in json.load(sys.stdin)['data']:
    if w.get('service')=='$1' and w.get('envId')=='$ENV_TEST' and w.get('type')=='service':
        print(w.get('imageRef') or w['image']); break" 2>/dev/null
}
for svc in product recommend order chatbot bff frontend; do
  IMG=$(prod_svc_image "$svc")
  [ -z "$IMG" ] && { echo "  [警告] $svc 无 test 镜像可复用，跳过"; continue; }
  cat > /tmp/wl-prod.json <<EOF
{
  "appId": "$APP_ID", "envId": "$ENV_PROD", "laneId": "default",
  "service": "$svc", "type": "service", "name": "paas-shop-$svc-prod",
  "image": "$IMG", "replicas": 1,
  "port": $( [ "$svc" = "bff" ] && echo 8080 || ([ "$svc" = "frontend" ] && echo 80 || ([ "$svc" = "product" ] && echo 8081 || ([ "$svc" = "recommend" ] && echo 8082 || ([ "$svc" = "chatbot" ] && echo 8083 || echo 8084))))),
  "resources": {"cpuRequest": "100m", "cpuLimit": "500m", "memRequest": "128Mi", "memLimit": "256Mi"}
}
EOF
  ensure_workload /tmp/wl-prod.json
done

# CronJob：sweeper-prod（TTL=30min）+ statsworker-prod（恢复部署）
cron_image() { # buildArgs SERVICE
  api "$PAAS_API/api/applications/$APP_ID/images" | python3 -c "
import sys,json
for i in json.load(sys.stdin)['data']:
    if i.get('status')=='ready' and '$1' in i.get('tag',''): print(i['registry']+':'+i['tag']); break" 2>/dev/null
}
SWEEPER_IMG=$(cron_image sweeper)
[ -n "$SWEEPER_IMG" ] && cat > /tmp/wl-sweeper-prod.json <<EOF
{"appId":"$APP_ID","envId":"$ENV_PROD","laneId":"default","service":"sweeper","type":"cronjob",
 "name":"paas-shop-sweeper-prod","image":"$SWEEPER_IMG","schedule":"*/5 * * * *",
 "resources":{"cpuRequest":"50m","cpuLimit":"200m","memRequest":"64Mi","memLimit":"128Mi"}}
EOF
[ -n "$SWEEPER_IMG" ] && ensure_workload /tmp/wl-sweeper-prod.json

# ③ prod 域名 Route：shop.example.com / → frontend-prod、/api → bff-prod（先建治理 Service 再 Route）
ensure_gov_service() { # name
  local exists
  exists=$(api "$PAAS_API/api/services" | python3 -c "
import sys,json
for s in json.load(sys.stdin)['data']:
    if s['name']=='$1': print(s['id']); break" 2>/dev/null || true)
  if [ -n "$exists" ]; then echo "$exists"; return 0; fi
  api -X POST "$PAAS_API/api/services" -H "Content-Type: application/json" \
    -d "{\"name\":\"$1\"}" | jget "['data']['id']"
}
SVC_FE=$(ensure_gov_service paas-shop-frontend-prod)
SVC_BFF=$(ensure_gov_service paas-shop-bff-prod)
ensure_route() { # name host path serviceId
  local exists
  exists=$(api "$PAAS_API/api/routes" | python3 -c "
import sys,json
for r in json.load(sys.stdin)['data']:
    if r['name']=='$1': print('yes'); break" 2>/dev/null || true)
  [ "$exists" = "yes" ] && { echo "  已存在 Route $1"; return 0; }
  api -X POST "$PAAS_API/api/routes" -H "Content-Type: application/json" \
    -d "{\"name\":\"$1\",\"host\":\"$2\",\"path\":\"$3\",\"serviceId\":\"$4\",\"methods\":[\"ANY\"],\"enabled\":true}" >/dev/null
  echo "  已创建 Route $1（$2$3）"
}
ensure_route shop-prod-root shop.example.com / "$SVC_FE"
ensure_route shop-prod-api shop.example.com /api "$SVC_BFF"
# test 域名同构（对齐三套域名方案 spec §10）
SVC_FE_T=$(ensure_gov_service paas-shop-frontend)
SVC_BFF_T=$(ensure_gov_service paas-shop-bff)
ensure_route shop-test-root shop.test.example.com / "$SVC_FE_T"
ensure_route shop-test-api shop.test.example.com /api "$SVC_BFF_T"

# ④ bff prod 环境配置（按环境隔离，spec §9④）
ensure_appconfig "$ENV_PROD" "PRODUCT_SERVICE_URL" "http://paas-shop-product-prod:8081" "env"
ensure_appconfig "$ENV_PROD" "RECOMMEND_SERVICE_URL" "http://paas-shop-recommend-prod:8082" "env"
ensure_appconfig "$ENV_PROD" "CHATBOT_SERVICE_URL" "http://paas-shop-chatbot-prod:8083" "env"
ensure_appconfig "$ENV_PROD" "ORDER_SERVICE_URL" "http://paas-shop-order-prod:8084" "env"
ensure_appconfig "$ENV_PROD" "PAAS_ORDER_ACTION_TOKEN" "$ACTION_TOKEN" "secret"

# ⑤ 发布顺序：无操作（事实 3：prod-bj 天然晋升目标，e2e 断言覆盖）

# ⑥ 备份记录（backup 前端入口演示）
DB_PROD_ID=$(api "$PAAS_API/api/dataservices" | python3 -c "
import sys,json
for d in json.load(sys.stdin)['data']:
    if d['name']=='shop-db-prod': print(d['id']); break" 2>/dev/null || true)
if [ -n "$DB_PROD_ID" ]; then
  api -X POST "$PAAS_API/api/backups" -H "Content-Type: application/json" \
    -d "{\"resourceId\":\"$DB_PROD_ID\",\"name\":\"shop-db-prod 每日备份\"}" >/dev/null 2>&1 || true
  echo "  备份记录已建（shop-db-prod）"
fi

echo "== setup part 2 完成 =="
```

> 执行时核对点：① `bindings` 请求体 `{type,name}` 是否为平台契约（看 openapi.json application bindings POST schema）；② cron_image 的 tag 匹配模式（BuildRun buildArgs.SERVICE 是否进 tag——若不进，改为取「该 SERVICE 最近一次构建的 OutImage」或直接手工传 `SWEEPER_IMG` env）；③ sweeper prod TTL=30 经 appconfig `ORDER_TTL_MINUTES=30` 注入（ensure_appconfig "$ENV_PROD" ORDER_TTL_MINUTES 30 env），test 配 2。执行者发现不符时以 openapi.json 实测为准修正脚本，并在脚本头注释记录偏差。

- [ ] **Step 2: 语法检查**

```bash
bash -n examples/paas-shop/setup.sh
```

- [ ] **Step 3: Commit（经用户授权后）**

```bash
git add examples/paas-shop/setup.sh
git commit -m "feat(shop): setup part2——prod 数据服务/显式 baseline/Route 三域名/环境配置/备份"
```

---

### Task 9: setup 脚本 part 3——流水线模板 + 退款 workflow

**Files:**
- Modify: `examples/paas-shop/setup.sh`（追加 part3）

**Interfaces:**
- Produces: 租户自定义模板 `tpl-shop-release`（build→deploy(test)→release→promote）+ 绑定 pipeline `shop-release`；workflow `shop-refund-approval`（start→llm→approve→tool→end）。
- Consumes: workflow 引用既有 agent `shop-assistant`（按名解析 ID）与 mcp 工具（query_order/refund_order，按名解析 toolID）。

- [ ] **Step 1: 追加 part3**

```bash
echo "== part 3: 演示流水线 + 退款审批 workflow =="

# 1. 租户自定义模板（super_admin 经 admin 端点；带 tenantId）
TPL_EXISTS=$(api "$PAAS_API/api/admin/pipeline-templates" | python3 -c "
import sys,json
for t in json.load(sys.stdin)['data']:
    if t['id']=='tpl-shop-release': print('yes'); break" 2>/dev/null || true)
if [ "$TPL_EXISTS" != "yes" ]; then
  api -X POST "$PAAS_API/api/admin/pipeline-templates" -H "Content-Type: application/json" -d @- <<'EOF' >/dev/null
{
  "id": "tpl-shop-release", "name": "商店发布流水线（test→prod 晋升演示）",
  "kind": "cd", "tenantId": "t-acme",
  "description": "构建 → 部署测试基线 → 打版本 → 晋升生产（promote 自动找 -prod 显式基线）",
  "stages": [
    {"name": "构建", "type": "build"},
    {"name": "部署测试", "type": "deploy", "params": {"envId": "{{app.env.test}}", "lane": "default", "imageSource": "priorBuild", "strategy": "rolling"}},
    {"name": "发布版本", "type": "release", "params": {"versionStrategy": "auto-increment"}},
    {"name": "晋升生产", "type": "promote"}
  ]
}
EOF
  echo "  已创建模板 tpl-shop-release"
fi

# 2. 绑定 pipeline（app 作用域；service 参数在 run 触发时经 paramOverrides/inputs 传）
PIPE_EXISTS=$(api "$PAAS_API/api/applications/$APP_ID/pipelines" | python3 -c "
import sys,json
for p in json.load(sys.stdin)['data']:
    if p['name']=='shop-release': print('yes'); break" 2>/dev/null || true)
if [ "$PIPE_EXISTS" != "yes" ]; then
  api -X POST "$PAAS_API/api/applications/$APP_ID/pipelines" -H "Content-Type: application/json" \
    -d "{\"name\":\"shop-release\",\"kind\":\"cd\",\"templateId\":\"tpl-shop-release\",\"trigger\":\"manual\"}" >/dev/null
  echo "  已创建 pipeline shop-release（手动触发）"
fi

# 3. 退款审批 workflow（spec §6：llm 建议 → approve 人工 → tool 真实退款）
AGENT_ID=$(api "$PAAS_API/api/agents" | python3 -c "
import sys,json
for a in json.load(sys.stdin)['data']:
    if a['name']=='shop-assistant': print(a['id']); break" 2>/dev/null || true)
TOOL_ID=$(api "$PAAS_API/api/tools" | python3 -c "
import sys,json
for t in json.load(sys.stdin)['data']:
    if 'mcp' in t.get('type','') or t.get('serverUrl',''): print(t['id']); break" 2>/dev/null || true)
WF_EXISTS=$(api "$PAAS_API/api/workflows" | python3 -c "
import sys,json
for w in json.load(sys.stdin)['data']:
    if w['name']=='shop-refund-approval': print('yes'); break" 2>/dev/null || true)
if [ "$WF_EXISTS" != "yes" ] && [ -n "$AGENT_ID" ] && [ -n "$TOOL_ID" ]; then
  cat > /tmp/wf.json <<EOF
{
  "name": "shop-refund-approval",
  "desc": "退款审批：LLM 分析理由 → 人工审批 → MCP 真实退款（spec §6）",
  "enabled": true,
  "nodes": [
    {"id": "start", "type": "start", "name": "开始", "nextId": "analyze"},
    {"id": "analyze", "type": "llm", "name": "分析退款理由", "nextId": "approve",
     "config": {"agentId": "$AGENT_ID",
       "inputTemplate": "订单 {{inputs.orderId}} 申请退款，理由：{{inputs.reason}}。请给出退款建议与金额摘要（50 字内）。"}},
    {"id": "approve", "type": "approve", "name": "人工审批", "nextId": "refund",
     "config": {"message": "确认退款订单 {{inputs.orderId}}？拒绝请中止运行（abort）"}},
    {"id": "refund", "type": "tool", "name": "执行退款", "nextId": "end",
     "config": {"toolId": "$TOOL_ID", "toolName": "refund_order",
       "args": {"orderId": "{{inputs.orderId}}"}}},
    {"id": "end", "type": "end", "name": "结束"}
  ]
}
EOF
  api -X POST "$PAAS_API/api/workflows" -H "Content-Type: application/json" -d @/tmp/wf.json >/dev/null
  echo "  已创建 workflow shop-refund-approval"
fi

echo "== setup 完成 =="
```

> 执行时核对：① workflow node type 常量（`start/llm/approve/tool/end`——以 `internal/ai/workflow/model.go` NodeType 常量实际值为准，若为 `NodeTypeStart="start"` 等字符串则一致）；② tool 解析逻辑按 mcp serverUrl 精确匹配（若有多个工具，改为按 name 含 shop/mcp 过滤）；③ pipeline trigger 字段形态（`"manual"` vs 枚举值，看 PipelineTrigger 模型）。

- [ ] **Step 2: 语法检查**

```bash
bash -n examples/paas-shop/setup.sh && chmod +x examples/paas-shop/setup.sh
```

- [ ] **Step 3: Commit（经用户授权后）**

```bash
git add examples/paas-shop/setup.sh
git commit -m "feat(shop): setup part3——shop-release 流水线模板 + 退款审批 workflow"
```

---

### Task 10: 文档站「示例演示」章节

**Files:**
- Create: `frontend/docs/guide/demo.md`
- Modify: `frontend/docs/.vitepress/config.mts`（sidebar guide 区追加入口）

**Interfaces:** 无代码接口；内容约束=spec §12（30 分钟演示旅程 + 三套域名表 + 术语业界对齐 + `*.example.com`）。

- [ ] **Step 1: 写 demo.md（30 分钟演示旅程脚本）**

```markdown
# 示例演示：paas-shop 全能力旅程

paas-shop 是平台的完整能力演示应用（电商域）……30 分钟走完「下单 → 事件流 → 关单 → 退款审批 → 泳道对比 → promote 上线 → 环境对比」。

## 三套域名

| 域名 | 环境 | 用途 |
|------|------|------|
| shop.example.com | 生产环境（Production） | 生产流量 |
| shop.test.example.com | 测试环境（Staging/Test） | 日常联调 |
| feat-order-v2.shop.test.example.com | 泳道（全链路灰度，Feature Lane） | 变更验证 |

> 演示域名经 Ingress host 匹配；本机访问 `curl -H 'Host: shop.example.com' http://<集群入口>/` 或配 hosts。

## 旅程（每步含平台控制台停留点）

1. **下单**（3min）：shop.test.example.com 商品详情下单 → 库存扣减。
   停留：控制台「工作负载」看 order 实例；「可观测 → 链路追踪」看 bff→order 调用链。
2. **事件流**（3min）：通知中心滚 order.created；概览 tab GMV 变化（bff 内 stats-consumer 消费组）。
   停留：「资源中心 → shop-nats → 消息主题」看 shop-events + 双消费组登记。
3. **超卖防护**（2min）：库存清零后下单 → 409（事务条件更新防超卖）。
4. **超时关单**（3min）：不支付的订单 2 分钟后 sweeper CronJob 关单 + 库存回补。
   停留：「工作负载」CronJob 类型 + Pod 周期创建 + 实时日志。
5. **退款审批**（5min）：运营视图订单 tab 点「退款」→ 复制订单号 → 控制台「智能体 → 工作流」运行 shop-refund-approval（inputs: orderId/reason）→ LLM 建议 → approve → MCP 真实退款（订单 refunded + 库存回补）。拒绝 = abort。
   停留：「审计日志」看退款操作记录。
6. **泳道对比**（5min）：feat-order-v2 域名下单（v2 响应带预计送达）vs 测试基线（v1）。
   停留：「泳道管理」页；trace 的 paas.lane 属性；日志按 lane 过滤；只部署了变更的 order 服务，其余降级基线。
7. **promote 上线**（6min）：sk-acme-dev 触发 shop-release 被 prod:write 拦（403 演示）→ sk-acme-admin 通过：构建→部署测试→版本→晋升生产。
   停留：流水线运行详情；shop.example.com 独立库下单（测试数据不可见=环境级隔离）。
8. **收尾**（3min）：reconcile 对账全绿；Secret 落库 enc:v1（静态加密）；备份记录页。

## 进阶指南

- 变更管理：变更分支 → 泳道验证 → 集成批次（发布火车 Release Train）→ approve 随车发布 → promote。
- 金丝雀：canary stage 并行验证（确认放量/零风险终止）。
```

（执行时按 spec §12 术语约束展开为完整文档——上表为骨架，每步的「停留点」必须写具体控制台菜单路径。）

- [ ] **Step 2: config.mts sidebar 追加**

guide 区 items 追加 `{ text: '示例演示', link: '/guide/demo' }`。

- [ ] **Step 3: 构建验证**

```bash
cd frontend && pnpm build
```
预期：docs 构建成功（VitePress 死链检查通过）。

- [ ] **Step 4: Commit（经用户授权后）**

```bash
git add frontend/docs/
git commit -m "docs: 示例演示章节——30 分钟全能力旅程 + 三套域名 + 进阶指南"
```

---

### Task 11: 本地全量回归 + 部署 + setup 执行

**Files:** 无新文件（构建/部署/编排）。

- [ ] **Step 1: examples 全量测试 + 平台回归 + 前端构建**

```bash
cd examples && go test ./... && go vet ./...
cd .. && make test && make build
cd frontend && pnpm build
```
预期：全绿（平台零代码改动，make test 应零差异通过）。

- [ ] **Step 2: 同步 Gitea（改代码→部署 runbook 第 2 步）**

```bash
# examples/paas-shop 内容同步到集群 Gitea paas-bot/paas-shop（既有 runbook）
# （order/sweeper 新目录 + bff/mcp/frontend 改动 + vendor 不变）
```
按记忆 runbook：git push 到 Gitea remote（凭证 paas-bot / paas-gitea-bot-2026）。

- [ ] **Step 3: 构建 order/sweeper 镜像（应用路径 BuildRun + buildArgs.SERVICE）**

```bash
api -X POST "$PAAS_API/api/applications/$APP_ID/buildruns" -H "Content-Type: application/json" \
  -d '{"branch":"main","buildArgs":{"SERVICE":"order"}}'
# 轮询至 succeeded；同款再跑 SERVICE=sweeper；bff/mcp/frontend 改动也各重建一次
```

- [ ] **Step 4: 重建 test workload 镜像（order 首次部署 test）**

```bash
# order 首部署：POST /api/applications/$APP_ID/workloads（name=paas-shop-order, service=order,
# envId=env-acme-test, port=8084, image=<刚构建镜像>）
# bff/mcp/frontend：既有 workload 经 Release/UpdateImage 滚动到新镜像
```

- [ ] **Step 5: 部署平台（常驻授权）+ 跑 setup**

```bash
./scripts/deploy-k8s.sh          # 平台无改动，此步主要为保持集群最新（常驻授权）
bash examples/paas-shop/setup.sh  # part1/2/3 全量（幂等）
```

- [ ] **Step 6: 验证 setup 产物**

```bash
api "$PAAS_API/api/applications/$APP_ID/workloads" | python3 -m json.tool | grep '"name"'
# 预期：test 6 服务 + sweeper/statsworker CronJob + prod 6 服务 + sweeper-prod/statsworker-prod
curl -s -H 'Host: shop.example.com' http://<集群入口>/api/products | head -c 200   # prod 域名通
curl -s -H 'Host: shop.test.example.com' http://<集群入口>/api/products | head -c 200
```

- [ ] **Step 7: Commit（经用户授权后；无平台代码改动则无 commit）**

---

### Task 12: e2e 演示旅程断言 + 对账收尾

**Files:** 无新文件（验证命令序列；发现 bug 则回对应 Task 修复）。

- [ ] **Step 1: e2e 七步断言（spec §13）**

```bash
B="http://<集群入口>"; T="sk-acme-admin"; H="Authorization: Bearer $T"
# 域名经 Host 头：TEST='Host: shop.test.example.com'；PROD='Host: shop.example.com'

# 1. test 下单（库存扣减）+ 事件 + stats
ORDER=$(curl -s -H "$H" -H "$TEST" -H 'Content-Type: application/json' \
  -d '{"userId":"e2e","items":[{"productId":1,"qty":1}]}' "$B/api/orders")
echo "$ORDER" | grep '"status":"pending"'            # 断言 201 pending
curl -s -H "$H" -H "$TEST" "$B/api/events" | grep order.created
curl -s -H "$H" -H "$TEST" "$B/api/stats" | grep gmvCents

# 2. 超卖 → 409（先把某商品库存买空，再下单）
# 3. sweeper 2min 关单 → 库存回补 → order.closed 事件（test ORDER_TTL_MINUTES=2）
# 4. 退款 workflow：POST /api/workflows/{id}/runs {"orderId","reason"} → 轮询 runs/{rid}
#    至 paused（approve）→ POST approve → 轮询 succeeded → 订单 refunded + 库存回补
# 5. 泳道：显式建 workload（name=paas-shop-order-feat-order-v2, laneId=feat-order-v2,
#    service=order, env=test）→ Host: feat-order-v2.shop.test.example.com 下单
#    → 响应/指纹头见泳道实例；trace 带 paas.lane
# 6. promote：sk-acme-dev 触发 shop-release 预期 403 → sk-acme-admin 通过 → 轮询 run
#    → prod 域名独立库下单（test 订单在 prod 不可见）
# 7. 收尾断言：
curl -s -H "$H" "$PAAS_API/api/notifications" >/dev/null
kubectl exec -n paas-t-acme deploy/paas-core-pg -- psql -U paas -d paas -tc \
  "SELECT count(*) FROM app_config_items WHERE key='PAAS_ORDER_ACTION_TOKEN' AND value LIKE 'enc:v1:%'"
# 预期 ≥2（test+prod 两桶，静态加密断言）
./scripts/reconcile.sh   # 预期全绿
```

- [ ] **Step 2: 演示前检查清单**

- [ ] 三套域名 curl 全通（生产/测试/泳道）
- [ ] 控制台各停留点页面可打开（工作负载 CronJob/泳道/工作流/审计/备份/消息主题）
- [ ] `PAAS_DISABLE_DEMO_SEED` 环境不受影响（演示资源非 demo seed，setup 独立）

- [ ] **Step 3: 收尾汇报**

向用户汇报：e2e 七步结果、reconcile 状态、演示旅程就绪、遗留项（spec §14 + 本计划偏差裁决中的平台遗留 2 项）。

---

## 自审记录

1. **Spec 覆盖**：§3 order（Task 1/2）✓；§4 MQ 双消费组+管理面（Task 4/7）✓；§5 sweeper（Task 3）✓；§6 退款 workflow（Task 9）✓；§7 MCP 真实化+Secret（Task 5/7）✓；§8 泳道（Task 12 e2e + 事实 2 裁决）✓；§9 七步（Task 8，⑤按事实 3 裁决删除）✓；§10 域名（Task 8 Route + Task 10 文档）✓；§11 frontend（Task 6）✓；§12 文档（Task 10）✓；§13 测试（各 Task TDD + Task 11/12）✓；§14 遗留（偏差裁决节）✓。
2. **占位扫描**：Task 5 `toolText`、Task 7/8/9 的请求体字段名均标注「以 openapi.json 实测为准」的核对步骤——这是对 API schema 的防漂移措施而非占位（setup 脚本属外部编排，无法在写计划时保证集群 schema 完全一致）。代码步骤均有完整代码。
3. **类型一致性**：`Transition`（Task 1 定义 / Task 2 使用）；`Order.items []Item{productId,name,qty,priceCents}`（Task 2 Go / Task 6 TS / Task 4 shopEvent 扩展对齐）；`statsAgg{orders,gmvCents,byStatus}`（Task 4 Go / Task 6 ShopStats TS 字段名一致）；`newStatsAgg/buildMuxWithStats`（Task 4 测试与实现一致）。
4. **发现并修正**：① Task 3 初稿复用 order 包 `applyAction`——独立 main 包不可 import，改为 sweeper 内嵌 SQL（Step 1 注明裁决）；② Task 2/3 初稿含未使用 import（math/rand、strconv、sync/atomic、fmt、pgx）与 `var _` 占位行——Go 未使用 import 直接编译失败，已全部清除；③ Task 8 死变量 `BK_EXISTS`（引用未定义 `$DS_ID`）与 `SWEeper_IMG` 命名不规范——已清理/统一为 `SWEEPER_IMG`。
