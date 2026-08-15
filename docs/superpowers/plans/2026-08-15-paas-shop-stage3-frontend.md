# paas-shop 阶段 3：电商前端完善 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** paas-shop 前端补齐搜索/分类过滤、商品详情抽屉、消息中心（bff 事件订阅 + 铃铛），拆组件重组。

**Architecture:** 纯前端改造为主（搜索/详情用既有端点）+ bff 加 NATS 订阅与 `GET /api/events` 端点。组件从 App.vue 拆出到 `frontend/src/components/`，单页无路由形态保持。

**Tech Stack:** Vue 3 + Vite + TypeScript（examples/paas-shop/frontend，npm 无 UI 库）；Go 标准库 + examples 内部包 `internal/natspub`、`internal/observ`。

**Spec:** docs/superpowers/specs/2026-08-15-paas-shop-stage3-frontend-design.md

## Global Constraints

- examples 是独立 module（github.com/aitoys/paas-examples），**禁止 import 任何 `github.com/aitoys/paas/` 平台内部包**。
- 注释语言与现有代码一致（中文）。
- 不执行主仓 git commit/分支操作；产物以未提交文件为准。
- 降级不 5xx：NATS 不可达 `/api/events` 返 `[]`；前端错误走既有 error 展示。
- 不引入 vue-router/pinia/UI 库（单页手写 CSS 形态保持）。

---

### Task 1: bff 事件订阅 + `GET /api/events` 端点

**Files:**
- Modify: `examples/paas-shop/bff/main.go`
- Test: `examples/paas-shop/bff/bff_test.go`（新建）

**Interfaces:**
- Consumes: `internal/natspub`（examples 内部包）的连接方式参照 `recommend/main.go:175-214`（QueueSubscribe group 模式）。
- Produces: `GET /api/events?limit=20` → JSON 数组 `[{type,productId,name,category,receivedAt}]`（最新在前）；`shopEvent` struct + `newEventRing(100)`。

- [ ] **Step 1: 写失败测试**（`bff_test.go`）

```go
package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEventRingPushAndOrder(t *testing.T) {
	r := newEventRing(3)
	now := time.Now()
	for i := 0; i < 5; i++ {
		r.push(shopEvent{Type: "product.created", ProductID: i, Name: "p", At: now, ReceivedAt: now.Add(time.Duration(i) * time.Second)})
	}
	got := r.latest(10)
	if len(got) != 3 { // 环形覆写，只留最近 3 条
		t.Fatalf("want 3, got %d", len(got))
	}
	if got[0].ProductID != 4 { // 最新在前
		t.Fatalf("want newest first, got %d", got[0].ProductID)
	}
}

func TestEventsEndpoint(t *testing.T) {
	r := newEventRing(10)
	now := time.Now()
	r.push(shopEvent{Type: "product.updated", ProductID: 1, Name: "k", At: now, ReceivedAt: now})
	h := &Handler{events: r} // Handler 包装 mux，测试注入
	srv := httptest.NewServer(h.mux)
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/api/events")
	if err != nil {
		t.Fatal(err)
	}
	var evs []shopEvent
	if err := json.NewDecoder(resp.Body).Decode(&evs); err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Type != "product.updated" {
		t.Fatalf("unexpected: %+v", evs)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**（`cd examples && go test ./paas-shop/bff/`，预期编译失败：undefined newEventRing/shopEvent）

- [ ] **Step 3: 实现**

bff/main.go 追加（模式照抄 recommend 的订阅块）：

```go
// shopEvent 是 shop-events 主题的一条事件（product 服务发布的商品变更）。
type shopEvent struct {
	Type      string    `json:"type"`
	ProductID int       `json:"productId"`
	Name      string    `json:"name"`
	Category  string    `json:"category"`
	At        time.Time `json:"at"`
	ReceivedAt time.Time `json:"receivedAt"`
}

// eventRing 固定容量环形缓冲（mutex 保护），存最近 N 条事件供 /api/events 轮询。
type eventRing struct {
	mu   sync.Mutex
	buf  []shopEvent
	next int // 下一个覆写位置
}

func newEventRing(n int) *eventRing { return &eventRing{buf: make([]shopEvent, 0, n)} }

func (r *eventRing) push(e shopEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf) < cap(r.buf) {
		r.buf = append(r.buf, e)
		return
	}
	r.buf[r.next] = e
	r.next = (r.next + 1) % cap(r.buf)
}

// latest 返回最近 n 条，最新在前（倒序）。
func (r *eventRing) latest(n int) []shopEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > len(r.buf) {
		n = len(r.buf)
	}
	out := make([]shopEvent, n)
	for i := 0; i < n; i++ {
		out[i] = r.buf[len(r.buf)-1-i]
	}
	return out
}
```

main() 里 mux 注册 + 订阅启动（`nats.Connect(os.Getenv("NATS_URL"))` 失败仅 log 警告继续）：

```go
events := newEventRing(100)
mux.HandleFunc("/api/events", func(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(events.latest(limit))
})
if natsURL := os.Getenv("NATS_URL"); natsURL != "" {
	if nc, err := nats.Connect(natsURL); err != nil {
		slog.Warn("NATS 连接失败，/api/events 将返回空", "err", err)
	} else {
		defer nc.Close()
		_, err = nc.QueueSubscribe("shop-events", "bff-consumer", func(msg *nats.Msg) {
			var e shopEvent
			if err := json.Unmarshal(msg.Data, &e); err != nil {
				return
			}
			e.ReceivedAt = time.Now()
			events.push(e)
		})
		if err == nil {
			nc.Flush()
			slog.Info("bff 已订阅 shop-events（bff-consumer group）")
		}
	}
}
```

注意：main.go 需补 import `encoding/json`、`strconv`、`sync`、`time`、`github.com/nats-io/nats.go`（recommend 已用同依赖，module 内已有）。测试里的 `Handler{mux}` 结构按实现实际形态调整（若 mux 直接是包级可测函数，导出 `buildMux(events *eventRing) *http.ServeMux` 供测试注入——**推荐此形态**，测试改用 `buildMux`）。

- [ ] **Step 4: 跑测试通过**（`cd examples && go test ./paas-shop/bff/ -v` 全绿）

- [ ] **Step 5: 构建验证**（`cd examples && go build ./... && go vet ./...`）

### Task 2: 前端组件拆分 + 搜索/分类 + 详情抽屉

**Files:**
- Create: `examples/paas-shop/frontend/src/components/SearchBar.vue`
- Create: `examples/paas-shop/frontend/src/components/ProductCard.vue`
- Create: `examples/paas-shop/frontend/src/components/DetailDrawer.vue`
- Create: `examples/paas-shop/frontend/src/components/ChatWindow.vue`
- Modify: `examples/paas-shop/frontend/src/App.vue`（重组：数据编排 + 搜索状态 + 组件组装）

**Interfaces:**
- Consumes: 既有 `GET /api/products?q=&category=&limit=`。
- Produces:
  - `SearchBar.vue`: props `categories: string[]`; emits `search(q: string, category: string)`
  - `ProductCard.vue`: props `product: Product`; emits `click`（App 传 product 打开抽屉）
  - `DetailDrawer.vue`: props `product: Product | null`; emits `close`, `ask(product: Product)`
  - `ChatWindow.vue`: 无 props；`defineExpose({ openWith })`，`openWith(preset?: string)` 打开弹窗并可选预填输入
  - `Product` interface 移到 `src/types.ts`（新建，含 id/name/price/category/stock/description/created_at?）

- [ ] **Step 1: 新建 `src/types.ts`**（Product interface，从 App.vue 抽出）

- [ ] **Step 2: 拆 ChatWindow.vue**——整体迁移 App.vue 现有 chat 弹窗（script 逻辑 + 模板 + 相关 CSS），加 `openWith(preset?: string)`：open=true，若 preset 非空则 `chatInput.value = preset` 并 focus。App.vue 通过 `ref` + `chatRef.value?.openWith(...)` 调用。

- [ ] **Step 3: 新建 SearchBar.vue**——文本输入（防抖 300ms `watch(chatInput)` + setTimeout 清理；回车 clearTimeout 立即 emit）+ 分类 chip 行（「全部」+ props categories，选中态高亮，点击 emit search）。内部持有 q/category 状态，emit 统一 `search(q, category)`。

- [ ] **Step 4: 新建 ProductCard.vue**——抽现有 .card 模板 + CSS；props product，整卡 click emit。

- [ ] **Step 5: 新建 DetailDrawer.vue**——右侧滑出（fixed right, transition transform）；内容：分类 chip / 名称 / 描述 / 大字价格 / 库存 / 创建时间（有则显示）；底部「💬 问客服」按钮 emit ask(product)；ESC（`@keydown.esc` on tabindex 容器或 window listener + onUnmounted 移除）与遮罩 click emit close。CSS 与 chat 弹窗同风格。

- [ ] **Step 6: 重组 App.vue**——保留 products/recommends 加载；新增 `searchQ/searchCategory` 状态，`loadProducts()` 改带 q/category 参数；categories 从全量商品派生（`[...new Set(products.map(p=>p.category))]`，注意在搜索前缓存全量派生——首次加载后存 `allCategories`）；header 加 `<SearchBar>`（结果区上方）+ 卡片区改 `<ProductCard v-for>` + `<DetailDrawer :product="selected" @ask="p => { selected=null; chatRef?.openWith(`${p.name}怎么样`) }" @close="selected=null">` + chat 改 `<ChatWindow ref="chatRef">`；空结果（非 loading 非 error 且 products.length===0）显示「未找到匹配商品」。

- [ ] **Step 7: 构建验证**（`cd examples/paas-shop/frontend && npm run build`，vue-tsc + vite 通过）

### Task 3: 消息中心（NotificationCenter + App 集成）

**Files:**
- Create: `examples/paas-shop/frontend/src/components/NotificationCenter.vue`
- Modify: `examples/paas-shop/frontend/src/App.vue`（header 挂铃铛）

**Interfaces:**
- Consumes: Task 1 的 `GET /api/events?limit=20`。
- Produces: 自包含组件（无 props/emits 契约，App 只挂载）。

- [ ] **Step 1: 新建 NotificationCenter.vue**

```
状态：events: Event[]、unread: number、open: boolean
lastSeen 从 localStorage 'paas:events:lastSeen' 读（无则 0）
onMounted：fetch + setInterval 10s（silent：错误静默，仅 console.debug）
每次 fetch 后：unread = events.filter(e => e.receivedAt > lastSeen).length
点铃铛：open = !open；打开时 lastSeen = now（取最新 receivedAt 与 now 较大者）写 localStorage，unread=0
类型中文映射：product.created→商品上新 / product.updated→商品变更 / product.bulk-seed→批量导入 / 其他→e.type
UI：铃铛按钮（🔔 + v-if unread 红点角标）+ v-if open 下拉面板（absolute，事件列表：类型 badge + 名称/分类 + 相对时间 mm:ss 前，空显示「暂无消息」）
onUnmounted clearInterval
Event interface：{type, productId, name, category, at, receivedAt}
```

相对时间：`Math.floor((Date.now()-new Date(e.receivedAt).getTime())/1000)` 秒，>60 显示 `${Math.floor(s/60)} 分钟前`，否则 `${s} 秒前`。

- [ ] **Step 2: App.vue header 挂载**——chat 按钮左侧加 `<NotificationCenter />`。

- [ ] **Step 3: 构建验证**（`npm run build` 通过）

### Task 4: 全量验证 + 部署 + e2e

**Files:** 无新文件。

- [ ] **Step 1: examples 全量验证**——`cd examples && go build ./... && go vet ./... && go test ./...` 全绿；`cd paas-shop/frontend && npm run build` 通过。
- [ ] **Step 2: 部署**（需用户授权的外发操作，到时确认）：rsync examples → 集群 Gitea（port-forward 13000，凭证 paas-bot/<PAAS_GITEA_BOT_PASSWORD>）→ push → BuildRun bff + frontend（buildArgs SERVICE=bff / SERVICE=frontend，frontend 用 `examples/paas-shop/frontend/Dockerfile` build context=examples——与既有 frontend 镜像构建方式一致）→ Release 部署（service 短名 bff/frontend）。
- [ ] **Step 3: e2e**：
  1. `shop.paas.k8s.dd` 首页加载，搜索「键盘」→ 仅剩机械键盘 X1；清空恢复全量；点分类 chip 过滤生效。
  2. 点任意卡片 → 详情抽屉滑出，字段正确；「问客服」→ chat 弹窗打开且输入框预填「{商品名}怎么样」；ESC 关闭抽屉。
  3. AI 客服多轮回归：「有什么键盘推荐」→ 回答含商品（阶段 2 链路不回归）。
  4. `curl -X PUT` 改一个商品名（经 paas.k8s.dd 或 port-forward 到 product）→ ≤10s 铃铛红点，点开显示「商品变更」事件。
  5. bff Pod 重启后 `/api/events` 返 `[]` 不 5xx（降级验证）。

## Self-Review

- Spec 覆盖：A 搜索（Task 2 Step 3/6）、B 详情（Task 2 Step 5/6）、C 消息中心（Task 1 + Task 3）、D 组件拆分（Task 2）✓；「不做」清单未越界（无购物车/router/SSE）✓。
- 类型一致性：shopEvent 字段 JSON tag（type/productId/name/category/at/receivedAt）与前端 Event interface 一致；SearchBar emits `search(q, category)` 与 App handler 签名一致；ChatWindow expose `openWith` 与 DetailDrawer ask 链路一致。
- 占位符：无 TBD；Task 1 测试的 `Handler{mux}` 已标注推荐 `buildMux` 形态落地。
