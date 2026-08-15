# paas-shop 阶段 1：后端业务功能 + MQ 链路 + Job + appconfig 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 paas-shop 4 个微服务的业务代码从「能跑」升级到「真实串联 PaaS 的 DB/Cache/MQ/Job/appconfig 五大子系统」，用「商品变更 → 事件 → 异步处理」最短链路演示。

**Architecture:** product 加搜索 + NATS producer；recommend 加 NATS consumer 失效缓存；新建 statsworker CronJob 定时聚合统计回写 appconfig；抽业务配置进 appconfig；补 deploy-paas-shop.sh 部署脚本（仓库缺失）+ shop-mq 应用绑定（脚本缺失）。examples 独立 module，只引 nats.go，不引 paas 内部包。

**Tech Stack:** Go 1.26（标准库 net/http + database/sql pgx）+ github.com/nats-io/nats.go（新增）+ github.com/redis/go-redis/v9（已有）+ bash 部署脚本调平台 REST API。

**Spec:** `docs/superpowers/specs/2026-08-14-paas-shop-stage1-backend-mq-job-design.md`

## Global Constraints

- **examples 独立 module**：所有 Go 改动在 `examples/` 下，`go build ./...` 必须在 `examples/` 目录跑（非主仓根）。只引标准库 + nats.go + 已有 pgx/redis/otel，**禁止 import 任何 `github.com/aitoys/paas/` 平台内部包**（worker 回写 appconfig 走平台 REST API HTTP，非内部包）。
- **NATS_URL 缺失降级**：product/recommend 在 NATS_URL env 为空时必须 log warning 并继续运行（不 panic/不退出），保证未绑 MQ 的最小部署不崩。
- **consumer group 名固定 `recommend-consumer`**：与 setup-paas-shop.sh §4.4 创建的 group 名一致（`examples/scripts/setup-paas-shop.sh:254`）。
- **topic 名固定 `shop-events`**：与 setup-paas-shop.sh §4.3 创建的 topic 一致（`examples/scripts/setup-paas-shop.sh:249`）。
- **构建驱动 buildArgs.SERVICE**：Dockerfile.backend `examples/paas-shop/Dockerfile.backend:13` 已是 `go build ... ./paas-shop/${SERVICE}` 通配，新服务只需进构建循环，不改 Dockerfile。
- **不执行 git 操作**：plan 中所有 commit 步骤仅在用户明确要求时执行；默认只改文件不 commit（用户全局规则）。
- **部署 runbook**：改代码 → 同步集群 Gitea → BuildRun → Release → 验证（[[paas-shop-change-to-deploy-runbook]]）。

---

## File Structure

| 文件 | 动作 | 职责 |
|---|---|---|
| `examples/go.mod` | 改 | 加 `github.com/nats-io/nats.go` 依赖 |
| `examples/go.sum` | 改 | go mod tidy 自动更新 |
| `examples/vendor/github.com/nats-io/` | 新增 | go mod vendor 拉取 |
| `examples/paas-shop/internal/natspub/natspub.go` | 新建 | NATS 连接 + Publish 共享包（降级容错） |
| `examples/paas-shop/internal/natspub/natspub_test.go` | 新建 | 降级 + 发布单测 |
| `examples/paas-shop/product/main.go` | 改 | schema 加 created_at+索引、搜索端点、NATS producer |
| `examples/paas-shop/product/search_test.go` | 新建 | 搜索 SQL 拼接单测 |
| `examples/paas-shop/recommend/main.go` | 改 | NATS QueueSubscribe 失效缓存、RECOMMEND_* 读 env |
| `examples/paas-shop/statsworker/main.go` | 新建 | CronJob 统计聚合 + 回写 appconfig |
| `examples/paas-shop/statsworker/stats_test.go` | 新建 | 聚合逻辑单测 |
| `examples/scripts/deploy-paas-shop.sh` | 新建 | 4 service + 1 cronjob workload 创建 + 绑定 db/cache/mq + appconfig |
| `examples/scripts/setup-paas-shop.sh` | 改 | 补 shop-mq 应用绑定、构建循环加 statsworker、appconfig key |

**包路径约定**：`github.com/aitoys/paas-examples/paas-shop/internal/natspub`、`.../paas-shop/statsworker`（examples module 名见 `examples/go.mod:1`）。

---

## Task 1: 加 nats.go 依赖 + vendor

**Files:**
- Modify: `examples/go.mod`, `examples/go.sum`
- Create: `examples/vendor/github.com/nats-io/`（go mod vendor 生成）

**Interfaces:**
- Produces: `github.com/nats-io/nats.go` 可被 import（`nats.Connect` / `nats.QueueSubscribe` / `nc.Publish`）。后续 task 依赖此。

- [ ] **Step 1: 在 examples module 加依赖**

Run（必须在 `examples/` 目录，非主仓根）:
```bash
cd examples
go get github.com/nats-io/nats.go@latest
go mod tidy
go mod vendor
```
Expected: `examples/go.mod` require 块出现 `github.com/nats-io/nats.go`，`examples/vendor/github.com/nats-io/nats.go/` 目录生成。

- [ ] **Step 2: 验证编译通过**

Run:
```bash
cd examples
go build ./...
```
Expected: 无报错（nats.go 引入但未使用不影响 build，go build 不报 unused import 若无 import）。

- [ ] **Step 3: 验证 vendor 一致**

Run:
```bash
cd examples
go mod verify
```
Expected: `all modules verified`。

- [ ] **Step 4:（可选，仅用户要求时）Commit**

```bash
git add examples/go.mod examples/go.sum examples/vendor
git commit -m "chore(examples): 引入 nats.go 依赖供 paas-shop MQ 链路"
```

---

## Task 2: 新建 natspub 共享包（连接 + Publish + 降级）

**Files:**
- Create: `examples/paas-shop/internal/natspub/natspub.go`
- Test: `examples/paas-shop/internal/natspub/natspub_test.go`

**Interfaces:**
- Consumes: env `NATS_URL`（平台 shop-mq 绑定注入，格式 `nats://<token>@<host>:4222`）。
- Produces:
  - `type Publisher struct{ ... }` — NATS 发布器（NATS_URL 空时为降级 stub）。
  - `func Connect(url string) *Publisher` — 连 NATS，url 空或连接失败返降级 stub（不返 error，调用方无感）。
  - `func (p *Publisher) Publish(subject string, payload []byte) error` — 发布消息；降级 stub 返 nil（静默丢弃）。
  - `func (p *Publisher) Close()` — 优雅关闭（drain + close）；降级 stub no-op。
  - `func (p *Publisher) Connected() bool` — 是否真连 NATS（降级返 false）。

- [ ] **Step 1: 写失败测试 — 降级 stub 在 url 空时不 panic 且 Publish 静默成功**

Create `examples/paas-shop/internal/natspub/natspub_test.go`:
```go
package natspub

import "testing"

func TestPublisherDegradedWhenURLEmpty(t *testing.T) {
	p := Connect("") // NATS_URL 空 → 降级 stub，不 panic 不退出
	if p.Connected() {
		t.Fatal("空 url 不应 Connected")
	}
	// 降级 stub 的 Publish 静默丢弃，返 nil（调用方无感，不阻断业务）
	if err := p.Publish("shop-events", []byte(`{"type":"product.changed"}`)); err != nil {
		t.Fatalf("降级 Publish 应返 nil，got %v", err)
	}
	p.Close() // no-op，不应 panic
}

func TestPublisherDegradedWhenUnreachable(t *testing.T) {
	// 指向不可达地址 → 连接失败也降级，不 panic
	p := Connect("nats://127.0.0.1:1")
	if p.Connected() {
		t.Fatal("不可达地址不应 Connected")
	}
	if err := p.Publish("shop-events", []byte(`x`)); err != nil {
		t.Fatalf("降级 Publish 应返 nil，got %v", err)
	}
	p.Close()
}
```

- [ ] **Step 2: 运行测试确认失败**

Run:
```bash
cd examples
go test ./paas-shop/internal/natspub/ -run TestPublisher -v
```
Expected: FAIL（`Connect` 未定义）。

- [ ] **Step 3: 实现 natspub 包**

Create `examples/paas-shop/internal/natspub/natspub.go`:
```go
// Package natspub 提供 paas-shop 共享的 NATS 发布能力。
//
// 平台创建 dataservice shop-mq(nats) -> 绑定应用 -> NATS_URL 注入 env（nats://<token>@<host>:4222）。
// Connect 在 NATS_URL 空或连接失败时降级为 stub（Connected=false，Publish 静默丢弃），
// 保证未绑 MQ 的最小部署（单测/无 MQ 集群）不崩——向后兼容。
package natspub

import (
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

// Publisher 封装 NATS 连接；degraded=true 时为降级 stub。
type Publisher struct {
	nc       *nats.Conn
	degraded bool
}

// Connect 连 NATS；url 空或失败均降级（不返 error，调用方无感）。
func Connect(url string) *Publisher {
	if url == "" {
		slog.Warn("NATS_URL 未设置，product/recommend 降级运行（MQ 链路不可用）")
		return &Publisher{degraded: true}
	}
	nc, err := nats.Connect(url,
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1), // 永久重连
		nats.Timeout(5*time.Second),
	)
	if err != nil {
		slog.Warn("NATS 连接失败，降级运行", "url", maskURL(url), "err", err)
		return &Publisher{degraded: true}
	}
	slog.Info("NATS 已连接", "url", maskURL(url))
	return &Publisher{nc: nc}
}

// Publish 发布消息；降级 stub 静默丢弃返 nil。
func (p *Publisher) Publish(subject string, payload []byte) error {
	if p.degraded || p.nc == nil {
		return nil
	}
	return p.nc.Publish(subject, payload)
}

// Connected 报告是否真连 NATS（降级返 false）。
func (p *Publisher) Connected() bool {
	return !p.degraded && p.nc != nil && !p.nc.IsClosed()
}

// Close 优雅关闭（drain 等在途消息发完）；降级 stub no-op。
func (p *Publisher) Close() {
	if p.degraded || p.nc == nil {
		return
	}
	_ = p.nc.Drain() // Drain 阻塞等缓冲发完再关
}

// maskURL 隐藏 token 段，保留 host:port 便于日志排查。
func maskURL(url string) string {
	for i := 0; i < len(url)-1; i++ {
		if url[i] == ':' && url[i+1] == '/' {
			if at := indexByte(url, '@'); at > i {
				return url[:i+3] + "***" + url[at:]
			}
		}
	}
	return url
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 4: 运行测试确认通过**

Run:
```bash
cd examples
go test ./paas-shop/internal/natspub/ -v
```
Expected: PASS（2 个测试）。

- [ ] **Step 5:（可选，仅用户要求时）Commit**

```bash
git add examples/paas-shop/internal/natspub/
git commit -m "feat(paas-shop): 新建 natspub 共享包（NATS 发布 + 降级容错）"
```

---

## Task 3: product schema 扩展（created_at + 索引）

**Files:**
- Modify: `examples/paas-shop/product/main.go:25-32`（Product struct）、`examples/paas-shop/product/main.go:85-120`（migrateAndSeed）

**Interfaces:**
- Produces: `Product.CreatedAt time.Time`（JSON 字段 `created_at`）；products 表加 `created_at TIMESTAMPTZ DEFAULT now()` 列 + `idx_products_category` 索引。后续搜索 task 依赖 created_at 排序。

- [ ] **Step 1: Product struct 加 CreatedAt 字段**

Modify `examples/paas-shop/product/main.go`，把 Product struct（L25-32）改为：
```go
type Product struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Price       float64   `json:"price"`
	Category    string    `json:"category"`
	Stock       int       `json:"stock"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}
```

- [ ] **Step 2: migrateAndSeed 建表加 created_at + 索引**

Modify `migrateAndSeed`（L85-120），建表 SQL 改为：
```go
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS products (
		id SERIAL PRIMARY KEY,
		name TEXT NOT NULL,
		price NUMERIC(10,2) NOT NULL,
		category TEXT NOT NULL,
		stock INT NOT NULL DEFAULT 0,
		description TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	if err != nil {
		return fmt.Errorf("建表: %w", err)
	}
	// category 索引加速按分类搜索/过滤
	if _, err := db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_products_category ON products(category)`); err != nil {
		return fmt.Errorf("建索引: %w", err)
	}
	// 存量库补列（已部署 PG 增量，ADD COLUMN IF NOT EXISTS 幂等）
	if _, err := db.ExecContext(ctx, `ALTER TABLE products ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now()`); err != nil {
		return fmt.Errorf("补列: %w", err)
	}
```
（保留原 `SELECT COUNT(*)` 判空 seed 逻辑不变。）

- [ ] **Step 3: seed + 现有 query 的 Scan 列表加 created_at**

现有 seed INSERT（L112）不含 created_at（用 DEFAULT now()），不改。

现有两处 SELECT 需补 created_at 列：
- productsHandler GET（L134）的 SELECT 加 `,created_at`，Scan（L144）加 `&p.CreatedAt`
- productDetailHandler（L188）的 SELECT 加 `,created_at`，Scan（L189）加 `&p.CreatedAt`

改后两处 SELECT/Scan 形如：
```go
rows, err := db.QueryContext(r.Context(), `SELECT id,name,price,category,stock,description,created_at FROM products ORDER BY id`)
// ...
if err := rows.Scan(&p.ID, &p.Name, &p.Price, &p.Category, &p.Stock, &p.Description, &p.CreatedAt); err != nil {
```
详情同理。

- [ ] **Step 4: 验证编译**

Run:
```bash
cd examples
go build ./paas-shop/product/
```
Expected: 无报错。

- [ ] **Step 5:（可选，仅用户要求时）Commit**

```bash
git add examples/paas-shop/product/main.go
git commit -m "feat(paas-shop): product schema 加 created_at + category 索引"
```

---

## Task 4: product 搜索端点（q/category/limit + 分页读 appconfig）

**Files:**
- Modify: `examples/paas-shop/product/main.go`（productsHandler GET 分支 + 新增 buildSearchQuery 函数）
- Test: `examples/paas-shop/product/search_test.go`

**Interfaces:**
- Produces:
  - `func buildSearchQuery(q, category string, limit int) (string, []any)` — 拼动态 SQL（参数化），返回 query + args。后续测试 + handler 依赖。
  - `GET /products?q=&category=&limit=` 端点行为：q→name ILIKE，category→精确，limit→分页（默认读 env `PRODUCT_PAGE_SIZE`，缺省 20，上限 100）。

- [ ] **Step 1: 写失败测试 — buildSearchQuery 动态 SQL 参数化**

Create `examples/paas-shop/product/search_test.go`:
```go
package main

import (
	"reflect"
	"testing"
)

func TestBuildSearchQueryAll(t *testing.T) {
	// 无过滤：全量，limit 生效
	q, args := buildSearchQuery("", "", 20)
	want := "SELECT id,name,price,category,stock,description,created_at FROM products ORDER BY created_at DESC LIMIT $1"
	if q != want {
		t.Fatalf("SQL:\n got: %s\nwant: %s", q, want)
	}
	if !reflect.DeepEqual(args, []any{20}) {
		t.Fatalf("args = %v, want [20]", args)
	}
}

func TestBuildSearchQueryKeyword(t *testing.T) {
	// 关键字：name ILIKE，参数化（防注入）
	q, args := buildSearchQuery("键", "", 20)
	if !contains(q, "name ILIKE $1") {
		t.Fatalf("应含 name ILIKE $1，got %s", q)
	}
	if len(args) != 2 || args[0] != "%键%" || args[1] != 20 {
		t.Fatalf("args 应为 [%%键%%, 20]，got %v", args)
	}
}

func TestBuildSearchQueryCategoryAndKeyword(t *testing.T) {
	q, args := buildSearchQuery("鼠", "外设", 50)
	if !contains(q, "name ILIKE $1") || !contains(q, "category = $2") {
		t.Fatalf("应同时含 name ILIKE + category，got %s", q)
	}
	if len(args) != 3 || args[0] != "%鼠%" || args[1] != "外设" || args[2] != 50 {
		t.Fatalf("args = %v", args)
	}
}

func TestBuildSearchQueryOnlyCategory(t *testing.T) {
	q, args := buildSearchQuery("", "音频", 20)
	if !contains(q, "category = $1") {
		t.Fatalf("应含 category = $1，got %s", q)
	}
	if args[0] != "音频" {
		t.Fatalf("args[0] = %v, want 音频", args[0])
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOfStr(s, sub) >= 0)
}

func indexOfStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: 运行测试确认失败**

Run:
```bash
cd examples
go test ./paas-shop/product/ -run TestBuildSearch -v
```
Expected: FAIL（`buildSearchQuery` 未定义）。

- [ ] **Step 3: 实现 buildSearchQuery + pageSize 读 env**

在 `examples/paas-shop/product/main.go` 加（放 writeJSON 之前）：
```go
// buildSearchQuery 拼商品搜索 SQL（参数化防注入）。q→name ILIKE，category→精确，limit→分页。
func buildSearchQuery(q, category string, limit int) (string, []any) {
	base := "SELECT id,name,price,category,stock,description,created_at FROM products"
	where := ""
	args := []any{}
	if q != "" {
		where += "name ILIKE $" + strconv.Itoa(len(args)+1)
		args = append(args, "%"+q+"%")
	}
	if category != "" {
		if where != "" {
			where += " AND "
		}
		where += "category = $" + strconv.Itoa(len(args)+1)
		args = append(args, category)
	}
	if where != "" {
		base += " WHERE " + where
	}
	base += " ORDER BY created_at DESC LIMIT $" + strconv.Itoa(len(args)+1)
	args = append(args, limit)
	return base, args
}

// pageSizeFromEnv 读 PRODUCT_PAGE_SIZE appconfig 注入的 env，缺省 20，上限 100。
func pageSizeFromEnv(r *http.Request, fallback int) int {
	v := r.URL.Query().Get("limit")
	if v == "" {
		v = os.Getenv("PRODUCT_PAGE_SIZE")
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fallback
	}
	if n > 100 {
		return 100
	}
	return n
}
```

- [ ] **Step 4: productsHandler GET 分支改用 buildSearchQuery**

Modify productsHandler（L131-150）GET 分支，把：
```go
		case http.MethodGet:
			rows, err := db.QueryContext(r.Context(), `SELECT id,name,price,category,stock,description,created_at FROM products ORDER BY id`)
			if err != nil {
```
改为：
```go
		case http.MethodGet:
			q := r.URL.Query().Get("q")
			category := r.URL.Query().Get("category")
			limit := pageSizeFromEnv(r, 20)
			query, args := buildSearchQuery(q, category, limit)
			rows, err := db.QueryContext(r.Context(), query, args...)
			if err != nil {
```
（保留 defer rows.Close() / Scan 循环 / writeJSON 不变，Scan 已含 &p.CreatedAt。）

- [ ] **Step 5: 运行测试确认通过**

Run:
```bash
cd examples
go test ./paas-shop/product/ -v
```
Expected: PASS（4 个搜索测试）。

- [ ] **Step 6: 验证整体编译**

Run:
```bash
cd examples
go build ./paas-shop/product/
```
Expected: 无报错。

- [ ] **Step 7:（可选，仅用户要求时）Commit**

```bash
git add examples/paas-shop/product/main.go examples/paas-shop/product/search_test.go
git commit -m "feat(paas-shop): product 搜索端点（q/category/limit + 分页读 appconfig）"
```

---

## Task 5: product 接入 NATS producer（POST/seed 发事件）

**Files:**
- Modify: `examples/paas-shop/product/main.go`（main 建连接、POST/seed 后发事件、main 退出 Close）

**Interfaces:**
- Consumes: Task 2 的 `natspub.Connect` / `Publish` / `Close`。
- Produces: product 启动建 `var pub *natspub.Publisher`；POST /products 成功后发 `shop-events` subject 消息 `{type:"product.changed",productId,...}`；seed 后发 `product.bulk-seed`。

- [ ] **Step 1: main 建连接 + defer Close**

Modify `examples/paas-shop/product/main.go` main()，在 `slog.Info("product 服务就绪"...`（L60）之前加：
```go
	// NATS producer（shop-mq 绑定注入 NATS_URL；空则降级 stub，不阻断）
	pub = natspub.Connect(os.Getenv("NATS_URL"))
	defer pub.Close()
```
并在文件顶部 `var db *sql.DB`（L34）旁加：
```go
var pub *natspub.Publisher
```
import 块加 `"github.com/aitoys/paas-examples/paas-shop/internal/natspub"`。

- [ ] **Step 2: POST /products 成功后发 product.changed 事件**

Modify productsHandler POST 分支（L161-169），在 `writeJSON(w, http.StatusCreated, p)` 之前加：
```go
		// 发商品变更事件到 shop-events（recommend 订阅失效缓存）
		publishProductEvent("product.changed", p)
```
并新增 helper（放 buildSearchQuery 旁）：
```go
// publishProductEvent 发商品事件到 shop-events topic（NATS 降级时静默丢弃）。
func publishProductEvent(eventType string, p Product) {
	if pub == nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"type":      eventType,
		"productId": p.ID,
		"name":      p.Name,
		"category":  p.Category,
		"at":        time.Now().UTC().Format(time.RFC3339),
	})
	if err := pub.Publish("shop-events", payload); err != nil {
		slog.Warn("发 NATS 事件失败", "type", eventType, "err", err)
	} else {
		slog.Info("发 NATS 事件", "type", eventType, "productId", p.ID)
	}
}
```

- [ ] **Step 3: seed 完成后发 bulk-seed 聚合事件**

Modify migrateAndSeed（L118 `slog.Info("seed 完成"...)` 之后）加：
```go
	if pub != nil {
		payload, _ := json.Marshal(map[string]any{
			"type":  "product.bulk-seed",
			"count": len(seeds),
			"at":    time.Now().UTC().Format(time.RFC3339),
		})
		_ = pub.Publish("shop-events", payload)
	}
```

- [ ] **Step 4: 验证编译**

Run:
```bash
cd examples
go build ./paas-shop/product/
```
Expected: 无报错（`encoding/json` 已 import 在 product，`time` 已 import）。

- [ ] **Step 5:（可选，仅用户要求时）Commit**

```bash
git add examples/paas-shop/product/main.go
git commit -m "feat(paas-shop): product 接入 NATS producer（商品变更/seed 发事件）"
```

---

## Task 6: recommend 接入 NATS consumer（失效缓存）

**Files:**
- Modify: `examples/paas-shop/recommend/main.go`

**Interfaces:**
- Consumes: env `NATS_URL`；Topic `shop-events`；Consumer group `recommend-consumer`（QueueSubscribe 第二参数）。
- Produces: recommend 启动后台 goroutine 订阅 `shop-events`，收到 `product.changed`/`product.bulk-seed` 时 `DEL recommend:*` 失效所有推荐缓存。`cacheTTL` / 推荐数量读 env（RECOMMEND_CACHE_TTL / RECOMMEND_COUNT）。

- [ ] **Step 1: import + 全局 nats 引用**

Modify `examples/paas-shop/recommend/main.go` import 块（L8-21）加：
```go
	"encoding/json"

	"github.com/nats-io/nats.go"
```
（`encoding/json` 已在文件中用，确认已 import。）

- [ ] **Step 2: cacheTTL/推荐数读 env**

Modify 全局变量块（L32-37）+ main 启动逻辑。把固定 `cacheTTL = 5 * time.Minute` 改为 main 里从 env 读：
```go
var (
	rdb        *redis.Client
	httpClient = observ.NewClient()
	productURL string
	cacheTTL   time.Duration // main 里从 RECOMMEND_CACHE_TTL env 读
	recCount   int           // main 里从 RECOMMEND_COUNT env 读
)
```
main()（L39-62）在 productURL 设置后加：
```go
	// 业务配置读 appconfig 注入的 env（缺省值保证未配可用）
	ttlSec := 300
	if v := os.Getenv("RECOMMEND_CACHE_TTL"); v != "" {
		if n, err := strconvAtoi(v); err == nil && n > 0 {
			ttlSec = n
		}
	}
	cacheTTL = time.Duration(ttlSec) * time.Second
	recCount = 3
	if v := os.Getenv("RECOMMEND_COUNT"); v != "" {
		if n, err := strconvAtoi(v); err == nil && n > 0 {
			recCount = n
		}
	}
```
并加 helper（避免与 strconv 冲突）：
```go
func strconvAtoi(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}
```
（import 确认含 `strconv`/`fmt`/`os`，recommend 现有 import 有 os/fmt，加 `strconv`。）

- [ ] **Step 3: pickRandom 用 recCount**

Modify recommendHandler（L121）`recs := pickRandom(all, userID, 3)` 改为 `recs := pickRandom(all, userID, recCount)`。

- [ ] **Step 4: 启动 NATS consumer goroutine 失效缓存**

main() 末尾（srv.ListenAndServe 之前）加：
```go
	// NATS consumer：订阅 shop-events，商品变更时失效推荐缓存（事件驱动一致性）
	go startCacheInvalidator(os.Getenv("NATS_URL"))
```
并新增函数（放 maskRedis 之前）：
```go
// startCacheInvalidator 订阅 shop-events，收到商品变更/seed 事件时 DEL recommend:* 失效缓存。
// NATS_URL 空时降级（不订阅，缓存仅靠 TTL 过期，向后兼容未绑 MQ）。
func startCacheInvalidator(natsURL string) {
	if natsURL == "" {
		slog.Warn("NATS_URL 未设置，recommend 缓存失效仅靠 TTL（MQ 链路不可用）")
		return
	}
	nc, err := nats.Connect(natsURL,
		nats.ReconnectWait(2*time.Second),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		slog.Warn("NATS 连接失败，缓存失效仅靠 TTL", "err", err)
		return
	}
	// QueueSubscribe + group=recommend-consumer：多副本 clustering 分担（与平台 consumer group 名一致）
	_, err = nc.QueueSubscribe("shop-events", "recommend-consumer", func(msg *nats.Msg) {
		var evt struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(msg.Data, &evt); err != nil {
			return
		}
		// product.changed / product.bulk-seed 都触发全量失效（按 category 精细失效留后续）
		if evt.Type == "product.changed" || evt.Type == "product.bulk-seed" {
			iter := rdb.Scan(context.Background(), 0, "recommend:*", 0).Iterator()
			var deleted int64
			for iter.Next(context.Background()) {
				if err := rdb.Del(context.Background(), iter.Val()).Err(); err == nil {
					deleted++
				}
			}
			slog.Info("MQ 事件失效推荐缓存", "type", evt.Type, "deleted", deleted)
		}
	})
	if err != nil {
		slog.Warn("NATS 订阅失败", "err", err)
		return
	}
	slog.Info("recommend 已订阅 shop-events（recommend-consumer group）")
}
```

- [ ] **Step 5: 验证编译**

Run:
```bash
cd examples
go build ./paas-shop/recommend/
```
Expected: 无报错。

- [ ] **Step 6:（可选，仅用户要求时）Commit**

```bash
git add examples/paas-shop/recommend/main.go
git commit -m "feat(paas-shop): recommend 接入 NATS consumer 失效缓存 + 读 appconfig"
```

---

## Task 7: 新建 statsworker（CronJob 统计聚合 + 回写 appconfig）

**Files:**
- Create: `examples/paas-shop/statsworker/main.go`
- Test: `examples/paas-shop/statsworker/stats_test.go`

**Interfaces:**
- Consumes: env `DATABASE_URL`（绑 shop-db）、`PAAS_APPCONFIG_URL`（平台 core base，如 `http://paas-core.paas.svc.cluster.local`）、`PAAS_API_KEY`（写 appconfig 权限，secret 注入）、`PAAS_APP_ID`（默认 `paas-shop`）、`PAAS_ENV_ID`（默认从 ENV，main 里读）、`PAAS_STATS_INTERVAL`（缺省跑一次退出；非空则循环 sleep，供 service 模式可选，CronJob 模式用单次）。
- Produces:
  - `func aggregateStats(ctx context.Context, db *sql.DB) (categorySummary map[string]int, total int, err error)` — 纯函数聚合，可测。
  - `func postAppConfig(baseURL, apiKey, appID, envID, key, value string) error` — 回写 appconfig REST。
  - main 入口 `/svc`：连 DB → aggregateStats → 回写 `STATS_CATEGORY_SUMMARY`（JSON）+ `STATS_TOTAL_PRODUCTS` → 退出（CronJob 单次语义）。

- [ ] **Step 1: 写失败测试 — aggregateStats 聚合逻辑**

Create `examples/paas-shop/statsworker/stats_test.go`:
```go
package main

import (
	"database/sql"
	"testing"
)

func TestAggregateStats(t *testing.T) {
	// 用 in-memory sqlite 不可行（pgx 仅 pg），改用 fakeDB 接口测试纯聚合
	// 这里测 buildSummaryJSON 的序列化
	summary := map[string]int{"外设": 2, "音频": 1}
	total := 3
	out := buildSummaryJSON(summary, total)
	if out["total"] != total {
		t.Fatalf("total = %v, want 3", out["total"])
	}
	cats, ok := out["categories"].(map[string]int)
	if !ok || cats["外设"] != 2 {
		t.Fatalf("categories 错误: %v", out["categories"])
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run:
```bash
cd examples
go test ./paas-shop/statsworker/ -v
```
Expected: FAIL（`buildSummaryJSON` 未定义）。

- [ ] **Step 3: 实现 statsworker**

Create `examples/paas-shop/statsworker/main.go`:
```go
// paas-shop 统计 worker：演示「CronJob 定时任务 + appconfig 双向」。
//
// 平台创建 type=cronjob workload（schedule */10 * * * *）-> schedule 到点拉起 Pod -> 跑本程序 -> 退出。
// 职责：连 product DB 聚合统计（分类商品数 + 总数）-> 回写 appconfig STATS_* -> 退出。
// appconfig 通常只读注入 workload，本 worker 演示「业务回写 appconfig」反向能力。
//
// 环境变量（平台绑定/注入）：
//   DATABASE_URL     - shop-db 绑定注入
//   PAAS_APPCONFIG_URL - 平台 core base（http://paas-core.paas.svc.cluster.local）
//   PAAS_API_KEY     - 写 appconfig 权限的 Key（appconfig secret 注入）
//   PAAS_APP_ID      - 应用 ID（默认 paas-shop）
//   PAAS_ENV_ID      - 环境 ID
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/aitoys/paas-examples/paas-shop/internal/observ"
)

func main() {
	shutdown := observ.Init("paas-shop-statsworker")
	defer shutdown()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		slog.Error("DATABASE_URL 未设置")
		os.Exit(1)
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		slog.Error("pgx open", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	summary, total, err := aggregateStats(ctx, db)
	if err != nil {
		slog.Error("聚合统计失败", "err", err)
		os.Exit(1)
	}
	slog.Info("统计完成", "total", total, "categories", summary)

	// 回写 appconfig（STATS_CATEGORY_SUMMARY + STATS_TOTAL_PRODUCTS）
	base := os.Getenv("PAAS_APPCONFIG_URL")
	apiKey := os.Getenv("PAAS_API_KEY")
	appID := os.Getenv("PAAS_APP_ID")
	if appID == "" {
		appID = "paas-shop"
	}
	envID := os.Getenv("PAAS_ENV_ID")
	if base == "" || apiKey == "" || envID == "" {
		slog.Warn("appconfig 回写跳过（PAAS_APPCONFIG_URL/PAAS_API_KEY/PAAS_ENV_ID 未配）")
		return
	}
	summaryJSON, _ := json.Marshal(buildSummaryJSON(summary, total))
	if err := postAppConfig(base, apiKey, appID, envID, "STATS_CATEGORY_SUMMARY", string(summaryJSON)); err != nil {
		slog.Warn("回写 STATS_CATEGORY_SUMMARY 失败", "err", err)
	}
	if err := postAppConfig(base, apiKey, appID, envID, "STATS_TOTAL_PRODUCTS", fmt.Sprintf("%d", total)); err != nil {
		slog.Warn("回写 STATS_TOTAL_PRODUCTS 失败", "err", err)
	}
	slog.Info("appconfig 回写完成")
}

// aggregateStats 聚合：分类商品数 + 总数。
func aggregateStats(ctx context.Context, db *sql.DB) (map[string]int, int, error) {
	rows, err := db.QueryContext(ctx, `SELECT category, count(*) FROM products GROUP BY category`)
	if err != nil {
		return nil, 0, fmt.Errorf("query: %w", err)
	}
	defer rows.Close()
	summary := map[string]int{}
	total := 0
	for rows.Next() {
		var cat string
		var n int
		if err := rows.Scan(&cat, &n); err != nil {
			return nil, 0, err
		}
		summary[cat] = n
		total += n
	}
	return summary, total, nil
}

// buildSummaryJSON 构造回写的 JSON 结构（可测纯函数）。
func buildSummaryJSON(summary map[string]int, total int) map[string]any {
	return map[string]any{
		"categories": summary,
		"total":      total,
		"at":         time.Now().UTC().Format(time.RFC3339),
	}
}

// postAppConfig 回写一条 appconfig（POST /api/applications/{app}/configs）。
func postAppConfig(baseURL, apiKey, appID, envID, key, value string) error {
	body, _ := json.Marshal(map[string]any{
		"key":   key,
		"value": value,
		"type":  "env",
		"envId": envID,
	})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost,
		baseURL+"/api/applications/"+appID+"/configs", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("appconfig 回写 %s: HTTP %d", key, resp.StatusCode)
	}
	return nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run:
```bash
cd examples
go test ./paas-shop/statsworker/ -v
```
Expected: PASS。

- [ ] **Step 5: 验证整体编译**

Run:
```bash
cd examples
go build ./paas-shop/statsworker/
```
Expected: 无报错。

- [ ] **Step 6:（可选，仅用户要求时）Commit**

```bash
git add examples/paas-shop/statsworker/
git commit -m "feat(paas-shop): 新建 statsworker（CronJob 聚合统计 + 回写 appconfig）"
```

---

## Task 8: 新建 deploy-paas-shop.sh（4 service + 1 cronjob + 绑定 + appconfig）

**Files:**
- Create: `examples/scripts/deploy-paas-shop.sh`

**Interfaces:**
- Consumes: 平台 REST API（`/api/applications/{app}/workloads` + `/bindings` + `/configs`）。REGISTRY env（节点 IP:30050）。
- Produces: 5 个 paas-shop workload（bff/product/recommend/chatbot 4 service + statsworker 1 cronjob）+ shop-db/cache/mq 绑定 + 业务 appconfig key。setup-paas-shop.sh 注释（L3）依赖此脚本。

**前提说明**：此脚本假设 paas-shop 应用 + 数据服务（shop-db/shop-cache/shop-mq）+ 镜像（$REGISTRY/paas-shop/<svc>:tag）已存在。镜像由 setup-paas-shop.sh §9 BuildRun 产出，应用 + 数据服务由更早的手动/其他脚本建。本脚本聚焦 workload + 绑定 + appconfig（仓库当前缺失的部分）。

- [ ] **Step 1: 写 deploy-paas-shop.sh**

Create `examples/scripts/deploy-paas-shop.sh`:
```bash
#!/usr/bin/env bash
# 为 paas-shop 示例应用创建核心工作负载 + 绑定数据服务 + 业务 appconfig。
# 补齐仓库缺失部分：setup-paas-shop.sh 注释依赖本脚本建 paas-shop 4 service + statsworker cronjob。
#
# 前提：paas-shop 应用已建、shop-db/cache/mq 数据服务已建（running）、镜像已推 $REGISTRY/paas-shop/<svc>:tag。
# 幂等：workload 按 name 查重，已存在跳过。
set -uo pipefail
H="Authorization: Bearer sk-acme-admin"
B="http://paas.k8s.dd"
APP="paas-shop"
REGISTRY="${REGISTRY:?设置 REGISTRY 为集群 registry，如 <nodeIP>:30050}"
TAG="${TAG:-latest}"

ENV_TEST=$(curl -s -H "$H" "$B/api/environments" | python3 -c "import sys,json;d=json.load(sys.stdin);es=d.get('data',d if isinstance(d,list) else []);print(next((e['id'] for e in es if e.get('type')=='test'),es[0]['id'] if es else ''))" 2>/dev/null)
echo "ENV_TEST=$ENV_TEST  APP=$APP  REGISTRY=$REGISTRY  TAG=$TAG"

echo "=== 1. 创建 4 个 service workload（bff/product/recommend/chatbot）==="
# name/port 对齐 setup-paas-shop.sh §1 治理注册的服务名（paas-shop-product 等）
for svc in \
  "{\"envId\":\"$ENV_TEST\",\"type\":\"service\",\"name\":\"paas-shop-product\",\"image\":\"$REGISTRY/paas-shop/product:$TAG\",\"replicas\":1,\"port\":80,\"containerPort\":8081}" \
  "{\"envId\":\"$ENV_TEST\",\"type\":\"service\",\"name\":\"paas-shop-recommend\",\"image\":\"$REGISTRY/paas-shop/recommend:$TAG\",\"replicas\":1,\"port\":80,\"containerPort\":8082}" \
  "{\"envId\":\"$ENV_TEST\",\"type\":\"service\",\"name\":\"paas-shop-chatbot\",\"image\":\"$REGISTRY/paas-shop/chatbot:$TAG\",\"replicas\":1,\"port\":80,\"containerPort\":8083}" \
  "{\"envId\":\"$ENV_TEST\",\"type\":\"service\",\"name\":\"paas-shop-bff\",\"image\":\"$REGISTRY/paas-shop/bff:$TAG\",\"replicas\":1,\"port\":80,\"containerPort\":8080}"; do
  NAME=$(echo "$svc" | python3 -c "import sys,json;print(json.load(sys.stdin)['name'])")
  # 查重：已存在跳过
  EXIST=$(curl -s -H "$H" "$B/api/workloads?type=service" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);print('yes' if any(x.get('name')=='$NAME' for x in d) else 'no')" 2>/dev/null)
  if [ "$EXIST" = "yes" ]; then
    echo "  skip (exists): $NAME"
    continue
  fi
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/workloads" -d "$svc" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  workload:',d.get('id'),d.get('name'))" 2>/dev/null
done

echo "=== 2. 创建 statsworker CronJob（*/10 分钟聚合统计回写 appconfig）==="
EXIST=$(curl -s -H "$H" "$B/api/workloads?type=cronjob" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);print('yes' if any(x.get('name')=='paas-shop-statsworker' for x in d) else 'no')" 2>/dev/null)
if [ "$EXIST" != "yes" ]; then
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/workloads" -d \
    "{\"envId\":\"$ENV_TEST\",\"type\":\"cronjob\",\"name\":\"paas-shop-statsworker\",\"image\":\"$REGISTRY/paas-shop/statsworker:$TAG\",\"schedule\":\"*/10 * * * *\"}" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  cronjob:',d.get('id'),d.get('name'))" 2>/dev/null
else
  echo "  skip (exists): paas-shop-statsworker"
fi

echo "=== 3. 绑定数据服务（shop-db/cache/mq -> product/recommend/statsworker）==="
# binding_injector 按 type 注入：db->DATABASE_URL, cache->REDIS_URL, mq->NATS_URL
# 绑定是应用级，注入到应用所有 workload（product/recommend/statsworker 各取所需 env）
for ds in \
  "{\"type\":\"db\",\"name\":\"shop-db\"}" \
  "{\"type\":\"cache\",\"name\":\"shop-cache\"}" \
  "{\"type\":\"mq\",\"name\":\"shop-mq\"}"; do
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/bindings" -d "$ds" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});bs=d.get('bindings',[]);print('  binding:',bs[-1].get('type'),bs[-1].get('name') if bs else 'exists')" 2>/dev/null
done

echo "=== 4. 业务 appconfig key（product/recommend 配置 + statsworker 凭证）==="
for kv in \
  "PRODUCT_PAGE_SIZE:20:env" \
  "RECOMMEND_COUNT:3:env" \
  "RECOMMEND_CACHE_TTL:300:env" \
  "PAAS_APPCONFIG_URL:http://paas-core.paas.svc.cluster.local:env" \
  "PAAS_APP_ID:paas-shop:env" \
  "PAAS_ENV_ID:$ENV_TEST:env" \
  "PAAS_API_KEY:sk-acme-dev:secret"; do
  K="${kv%%:*}"; REST="${kv#*:}"; V="${REST%:*}"; TYPE="${REST##*:}"
  curl -s -o /dev/null -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/configs" -d \
    "{\"key\":\"$K\",\"value\":\"$V\",\"type\":\"$TYPE\",\"envId\":\"$ENV_TEST\"}" && echo "  cfg: $K"
done

echo "=== 5. 等 reconciler 落地 + 验证 ==="
sleep 15
echo "--- paas-shop pods ---"
kubectl get pods -n paas-t-acme -l paas.aitoys/app=paas-shop 2>/dev/null | tail -10
echo "--- statsworker cronjob ---"
kubectl get cronjob paas-shop-statsworker -n paas-t-acme 2>/dev/null | tail -2
echo "=== deploy-paas-shop.sh 完成 ==="
```

- [ ] **Step 2: 加可执行权限**

Run:
```bash
chmod +x examples/scripts/deploy-paas-shop.sh
```

- [ ] **Step 3: shellcheck 静态检查（如可用）**

Run:
```bash
shellcheck examples/scripts/deploy-paas-shop.sh 2>/dev/null || echo "shellcheck 未装，跳过（非阻塞）"
```
Expected: 无 error（warning 可接受）。

- [ ] **Step 4:（可选，仅用户要求时）Commit**

```bash
git add examples/scripts/deploy-paas-shop.sh
git commit -m "feat(paas-shop): 新建 deploy-paas-shop.sh（workload+绑定+appconfig）"
```

---

## Task 9: setup-paas-shop.sh 扩展（构建加 statsworker + 补 shop-mq 绑定）

**Files:**
- Modify: `examples/scripts/setup-paas-shop.sh:137`（构建循环加 statsworker）、新增 shop-mq 应用绑定段

**Interfaces:**
- Consumes: Task 8 的 deploy-paas-shop.sh 建好的应用（应用 + workload 已存在）。
- Produces: 构建循环覆盖 5 个服务（product/recommend/chatbot/bff/statsworker）；shop-mq 应用绑定补全（NATS_URL 注入到 product/recommend）。

- [ ] **Step 1: 构建循环加 statsworker**

Modify `examples/scripts/setup-paas-shop.sh` L137：
```bash
for SVC in product recommend chatbot bff; do
```
改为：
```bash
for SVC in product recommend chatbot bff statsworker; do
```

- [ ] **Step 2: 补 shop-mq 应用绑定（NATS_URL 注入）**

在 §9 DevOps 段之前（L127 `echo "=== §9 DevOps..."` 之前）加新段：
```bash
echo "=== §8.5 数据服务绑定（shop-db/cache/mq 注入 env 到 paas-shop workload）==="
# binding_injector 按 type 注入：db->DATABASE_URL, cache->REDIS_URL, mq->NATS_URL
# 应用级绑定，注入到应用所有 workload（product/recommend/statsworker 各取所需 env）
for ds in \
  '{"type":"db","name":"shop-db"}' \
  '{"type":"cache","name":"shop-cache"}' \
  '{"type":"mq","name":"shop-mq"}'; do
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/bindings" -d "$ds" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});bs=d.get('bindings',[]);print('  binding:',bs[-1].get('type'),bs[-1].get('name') if bs else 'exists')" 2>/dev/null
done
echo "=== §8.5 数据服务绑定完成 ==="
```

**注意**：§4 创建 shop-mq 数据服务在脚本末尾（L234），而 §8.5 绑定在中间。脚本顺序上绑定先于数据服务创建。处理：把 §4 整段（shop-mq 数据服务创建，L234-257）移到 §8.5 之前，或在 §8.5 注释说明「若 shop-mq 尚未建，绑定会失败，请先跑 §4」。**推荐做法**：在 §8.5 开头加注释 `# 注：shop-mq 数据服务在 §4 创建；本段若先跑，shop-mq 绑定会失败，重跑即可（幂等）`。

- [ ] **Step 3: 业务 appconfig key 补到 setup（与 deploy-paas-shop.sh 一致，幂等）**

在 §10 traffic-gen appconfig 段（L167-172）之后加 paas-shop 业务 appconfig（与 Task 8 Step 1 §4 一致的 key 列表，保证无论先跑哪个脚本都配齐）。

- [ ] **Step 4: 验证脚本语法**

Run:
```bash
bash -n examples/scripts/setup-paas-shop.sh
```
Expected: 无语法错误。

- [ ] **Step 5:（可选，仅用户要求时）Commit**

```bash
git add examples/scripts/setup-paas-shop.sh
git commit -m "feat(paas-shop): 构建加 statsworker + 补 shop-mq 绑定 + 业务 appconfig"
```

---

## Task 10: 整体构建验证 + 部署 + e2e

**Files:**
- 无新文件（验证 task）

**Interfaces:**
- 消费前面所有 task 的产物。

- [ ] **Step 1: examples 整体 build + vet + test**

Run:
```bash
cd examples
go build ./...
go vet ./...
go test ./...
```
Expected: 全绿（natspub/product search/statsworker 测试通过，无 vet 警告）。

- [ ] **Step 2: vendor 一致性确认**

Run:
```bash
cd examples
go mod verify
```
Expected: `all modules verified`。

- [ ] **Step 3: 同步代码进集群 Gitea + 构建部署（[[paas-shop-change-to-deploy-runbook]]）**

paas-shop 的 CodeRepo 是 internal 集群 Gitea（paas-bot/paas-shop-examples），改代码后必须先确认同步进 Gitea 再触发 BuildRun。

Run（用户确认部署授权后）:
```bash
# 1. 同步 examples 改动进集群 Gitea（用户手动 git push 到 Gitea，或确认已同步）
# 2. 触发 5 服务构建（setup-paas-shop.sh §9 已含 statsworker）
cd /Users/wangtao/data/github.com/aitoys/paas
./examples/scripts/setup-paas-shop.sh  # 或单独触发 buildruns
# 3. 轮询构建完成 + Release 部署（deploy-paas-shop.sh）
REGISTRY=<nodeIP>:30050 ./examples/scripts/deploy-paas-shop.sh
```

- [ ] **Step 4: e2e 验证（dev 集群 paas-shop）**

Run:
```bash
H="Authorization: Bearer sk-acme-dev"
B="http://paas.k8s.dd"

# 1. 搜索功能：GET /api/products?q=键 应返回匹配商品
curl -s -H "$H" "$B/api/shop/products/products?q=键" | python3 -m json.tool

# 2. MQ→Cache 联动：POST 新商品后 GET /api/recommend 缓存应失效重算
curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/shop/products/products" -d '{"name":"测试商品E2E","price":99,"category":"外设","stock":10}'
sleep 2
curl -s -H "$H" "$B/api/shop/recommend/recommend?userId=e2e" -I 2>/dev/null | grep -i x-cache
# recommend 日志应有「MQ 事件失效推荐缓存 type=product.changed」
kubectl logs -n paas-t-acme -l paas.aitoys/workload=paas-shop-recommend --tail=20 | grep "MQ 事件"

# 3. CronJob + appconfig 回写：等 10 分钟（或手动触发 cronjob）后查 appconfig
kubectl create job --from=cronjob/paas-shop-statsworker -n paas-t-acme statsworker-manual 2>/dev/null
sleep 30
curl -s -H "$H" "$B/api/applications/paas-shop/configs?envId=$ENV_TEST" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);print([c for c in d if c.get('key','').startswith('STATS_')])"
# 应见 STATS_TOTAL_PRODUCTS / STATS_CATEGORY_SUMMARY 被回写

# 4. 降级验证：临时解绑 shop-mq（可选，验证 NATS_URL 空时 product/recommend 不崩）
```
Expected:
- 搜索返回匹配商品（非全量）
- POST 后 recommend 收到 MQ 事件失效缓存（日志可见）
- statsworker 手动触发后 appconfig 出现 STATS_* key
- product/recommend 全程不 panic

- [ ] **Step 5: 记录 memory（[[k8s-always-latest]] 后续）**

部署验证通过后，更新 memory 记录阶段 1 落地（paas-shop MQ 链路 + Job + appconfig 真实串联）。

---

## Self-Review 结果

**1. Spec 覆盖**：
- product 搜索 → Task 3+4 ✓
- product schema created_at+索引 → Task 3 ✓
- product NATS producer → Task 2+5 ✓
- recommend NATS consumer 失效缓存 → Task 6 ✓
- recommend 读 appconfig → Task 6 ✓
- statsworker CronJob 聚合回写 → Task 7 ✓
- appconfig 5 key → Task 8 §4 + Task 9 ✓
- nats.go 依赖 → Task 1 ✓
- deploy-paas-shop.sh（仓库缺） → Task 8 ✓
- shop-mq 应用绑定（脚本缺） → Task 8 §3 + Task 9 §2 ✓
- 构建加 statsworker → Task 9 §1 ✓
- NATS_URL 降级 → Task 2（natspub Connect）+ Task 6（startCacheInvalidator） ✓
- e2e 验证 → Task 10 ✓

**2. Placeholder 扫描**：无 TBD/TODO；所有代码步骤含完整代码块；`<nodeIP>` 是部署时 env 占位（脚本头 REGISTRY env 必填，符合 examples 现有约定）。

**3. 类型一致性**：
- `natspub.Connect(url string) *Publisher` — Task 2 定义，Task 5 product 消费一致 ✓
- `natspub.Publisher.Publish(subject string, payload []byte) error` / `Close()` / `Connected()` — Task 2/5 一致 ✓
- `buildSearchQuery(q, category string, limit int) (string, []any)` — Task 4 定义+测试一致 ✓
- `buildSummaryJSON(summary map[string]int, total int) map[string]any` — Task 7 定义+测试一致 ✓
- consumer group `recommend-consumer`、topic `shop-events`、event type `product.changed`/`product.bulk-seed` 全 plan 一致 ✓

无遗留缺口。
