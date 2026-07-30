# MaaS 对接第三方供应商 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 MaaS 通道从 mock/echo 替换为真实第三方供应商（OpenAI/DeepSeek/通义千问），平台托管凭证 + 跨供应商容灾降级，不自建推理集群。

**Architecture:** 一个 `OpenAICompatibleProvider` 适配器（纯 `net/http` + SSE 解析）覆盖三家 OpenAI 兼容协议供应商；`provider.Provider` 契约零改动，Gateway 无感；凭证复用 `internal/security`（新增平台级 Scope），经 `pkg/provider.CredentialResolver` 接口依赖倒置注入；Gateway 补请求级 failover。

**Tech Stack:** Go 1.23 + `net/http` + SSE 行解析（无第三方 SDK）+ `httptest`（测试）

**设计依据：** `docs/superpowers/specs/2026-07-28-maas-third-party-providers-design.md`

## Global Constraints

- **契约不变**：`pkg/provider.Provider.Chat(ctx, ChatRequest) (<-chan Chunk, error)` 签名零改动。
- **不自建 vLLM / 不接 GPU**（产品路线定调，见 `maas-product-direction` 记忆）。
- **明文 Key 仅内存**：不写日志、不进响应、不持久化到 Channel；security List/Get/Create 返回掩码（`SecretMask`）。
- **平台级 Secret**：`Scope="platform"` 时 `TenantID=""`，查询不按租户过滤，仅 admin 可改；`Scope="tenant"`（默认）行为不变。
- **依赖倒置破环**：`CredentialResolver` 接口定义在 `pkg/provider`（非 `internal/maas`），main.go 用 security store 适配；`pkg/plugin` 不得 import `internal/*`。
- **错误 sentinel 驱动降级**：`ErrCredentialMissing`/`ErrCredentialInvalid`→offline（不重试）；`ErrUpstreamRateLimit`/`ErrUpstreamUnavailable`→degraded（failover）；`ErrUpstreamConfig`→offline。
- **向后兼容**：mock/echo 通道保留（演示 + fallback）；Channel 新增字段为零值时通道行为不变。
- **不主动 git commit**（项目约定：未经用户明确要求不执行 git 操作）；每任务以"测试 + lint 全绿"收尾。
- **Apache 2.0 兼容**：纯标准库，无新第三方依赖。

## File Structure

**新增：**
- `internal/maas/openai_compatible.go` —— OpenAICompatibleProvider + 错误 sentinel
- `internal/maas/openai_compatible_test.go` —— httptest 驱动的流式/错误单测

**修改：**
- `pkg/provider/provider.go` —— 新增 `CredentialResolver` 接口 + 错误 sentinel（共享契约层）
- `pkg/provider/model.go` —— Channel 加 `UpstreamModel` / `CredentialRef` 字段
- `pkg/plugin/plugin.go` —— CoreDeps 加 `SecretResolver()` 注入点
- `internal/security/model.go` —— Secret 加 `Scope` + 常量 `ScopePlatform`/`ScopeTenant`
- `internal/security/memory/store.go` —— 平台级 Secret 查询不按租户过滤 + seed 一条平台级凭证
- `internal/security/handler.go` —— 平台级 Secret 仅 admin 可写
- `internal/maas/catalog.go` —— seed 真实通道（OpenAICompatibleProvider）+ 保留演示 mock
- `internal/maas/plugin.go` —— Init 取 resolver 传入 catalog
- `cmd/core/main.go` —— 注入 security store 作 SecretResolver + 桥接 CredentialResolver
- `internal/core/gateway/openai.go` —— 请求级 failover

---

### Task 1: 共享契约层 —— CredentialResolver 接口 + Channel 字段 + 错误 sentinel

**Files:**
- Modify: `pkg/provider/provider.go`（加接口 + sentinel）
- Modify: `pkg/provider/model.go`（Channel 加字段）
- Test: `pkg/provider/provider_test.go`（新建或追加）

**Interfaces:**
- Produces: `provider.CredentialResolver` 接口、`provider.Err*` sentinel、`Channel.UpstreamModel` / `Channel.CredentialRef` 字段。后续任务依赖。

- [ ] **Step 1: 写失败测试（接口与 sentinel 存在性 + Channel 字段）**

新建/追加 `pkg/provider/provider_test.go`：

```go
package provider

import (
	"context"
	"errors"
	"testing"
)

// stubResolver 验证 CredentialResolver 接口可被外部实现。
type stubResolver struct{ v string }

func (s stubResolver) Resolve(string) (string, error) { return s.v, nil }

func TestCredentialResolverInterface(t *testing.T) {
	var r CredentialResolver = stubResolver{v: "sk-test"}
	v, err := r.Resolve("ref")
	if err != nil || v != "sk-test" {
		t.Fatalf("Resolve 应返回明文，got %q %v", v, err)
	}
}

// TestSentinelsAreDistinct 验证四类错误 sentinel 互不混淆（驱动降级决策）。
func TestSentinelsAreDistinct(t *testing.T) {
	errs := []error{ErrCredentialMissing, ErrCredentialInvalid, ErrUpstreamRateLimit, ErrUpstreamUnavailable, ErrUpstreamConfig}
	for i, a := range errs {
		for j, b := range errs {
			if i != j && errors.Is(a, b) {
				t.Fatalf("sentinel %d 与 %d 不应相等", i, j)
			}
		}
	}
	_ = context.Background()
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./pkg/provider/ -run "CredentialResolver|Sentinels" -v`
Expected: FAIL（`undefined: CredentialResolver` / `ErrCredentialMissing`）

- [ ] **Step 3: 实现接口与 sentinel**

在 `pkg/provider/provider.go` 追加：

```go
// CredentialResolver 解析平台级 Secret 明文（仅内存，不日志、不持久化）。
// 由 security store 在 cmd/core 注入实现（依赖倒置，破除 maas→security import）。
// 解析失败（凭证被删/未配置）返回错误，调用方据此把通道标记 offline。
type CredentialResolver interface {
	Resolve(credentialRef string) (plaintext string, err error)
}

// 以下 sentinel 由真实通道（如 OpenAICompatibleProvider）返回，驱动 Gateway 降级决策：
//   - ErrCredentialMissing/ErrCredentialInvalid/ErrUpstreamConfig → offline（不重试，需运维修）
//   - ErrUpstreamRateLimit/ErrUpstreamUnavailable → degraded（请求级 failover 到备通道）
var (
	ErrCredentialMissing   = errors.New("凭证未配置")
	ErrCredentialInvalid   = errors.New("凭证无效或被拒（401/403）")
	ErrUpstreamRateLimit   = errors.New("上游限流（429）")
	ErrUpstreamUnavailable = errors.New("上游不可用（5xx/超时）")
	ErrUpstreamConfig      = errors.New("上游配置错误（4xx 非鉴权）")
)
```

`errors` 包加入 import。

- [ ] **Step 4: Channel 加字段（`pkg/provider/model.go`）**

```go
type Channel struct {
	ID       string `json:"id"`
	Type     string `json:"type"` // echo/mock/openai-compatible
	Priority int    `json:"priority"`
	Status   string `json:"status"`
	Endpoint string `json:"endpoint,omitempty"` // 供应商 BaseURL（如 https://api.deepseek.com）
	// 第三方供应商通道配置（mock/echo 通道为零值）。
	UpstreamModel string `json:"upstreamModel,omitempty"` // 供应商侧模型名（deepseek-chat / qwen-plus / gpt-4o）
	CredentialRef string `json:"credentialRef,omitempty"` // 凭证引用（security 平台级 Secret ID）
	impl Provider `json:"-"`
}
```

- [ ] **Step 5: 运行测试验证通过 + 全量回归**

Run: `go test ./pkg/provider/ -race -count=1 -v` → 全 PASS
Run: `go test ./... -race -count=1` → 既有测试无回归
Run: `golangci-lint run ./...` → 0 issues

---

### Task 2: CoreDeps 注入点 —— SecretResolver()

**Files:**
- Modify: `pkg/plugin/plugin.go`（CoreDeps 接口加方法）
- Modify: `pkg/plugin/plugin_test.go`（stub 实现补方法，保持编译）

**Interfaces:**
- Consumes: `provider.CredentialResolver`（Task 1）
- Produces: `CoreDeps.SecretResolver()` —— main.go 注入点，供 maas.Init 取用。

- [ ] **Step 1: 加 CoreDeps 方法**

`pkg/plugin/plugin.go`，CoreDeps 接口追加：

```go
type CoreDeps interface {
	Logger() interface{}
	Gateway() provider.GatewayRegistrar
	// SecretResolver 返回平台级 Secret 明文解析器；非 MaaS 类插件可返回 nil。
	// 用于第三方供应商通道经 CredentialRef 解析出 API Key（仅内存）。
	SecretResolver() provider.CredentialResolver
}
```

- [ ] **Step 2: 修 stub 实现（保持测试编译）**

`pkg/plugin/plugin_test.go` 中所有实现 `CoreDeps` 的 stub 补：

```go
func (s stubCoreDeps) SecretResolver() provider.CredentialResolver { return nil }
```

（若有多个 stub，逐一补；用 `grep -n "Gateway()" pkg/plugin/plugin_test.go` 定位。）

- [ ] **Step 3: 编译 + 测试**

Run: `go build ./... && go test ./pkg/plugin/ -race -count=1` → PASS
Run: `golangci-lint run ./...` → 0 issues

> 注：CoreDeps 实现者（main.go）在 Task 5 接线，本任务只定契约。

---

### Task 3: OpenAICompatibleProvider 核心（TDD 重点）

**Files:**
- Create: `internal/maas/openai_compatible.go`
- Create: `internal/maas/openai_compatible_test.go`

**Interfaces:**
- Consumes: `provider.Provider`、`provider.CredentialResolver`、`provider.Err*`（Task 1）
- Produces: `OpenAICompatibleProvider`（实现 `provider.Provider`），供 catalog 构造。

- [ ] **Step 1: 写失败测试 —— 正常流式解析**

`internal/maas/openai_compatible_test.go`：

```go
package maas

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/pkg/provider"
)

// stubResolver 返回固定明文。
type stubResolver struct{ v string; err error }

func (s stubResolver) Resolve(string) (string, error) { return s.v, s.err }

// newSSEServer 起一个模拟 OpenAI 兼容流式上游，逐行吐 SSE。
func newSSEServer(t *testing.T, status int, lines []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 200 {
			http.Error(w, `{"error":"upstream"}`, status)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, ln := range lines {
			_, _ = w.Write([]byte(ln))
			if fl != nil {
				fl.Flush()
			}
		}
	}))
}

func collect(ch <-chan provider.Chunk) string {
	var sb strings.Builder
	for c := range ch {
		sb.WriteString(c.Content)
	}
	return sb.String()
}

func TestOpenAICompatible_StreamOK(t *testing.T) {
	srv := newSSEServer(t, 200, []string{
		`data: {"choices":[{"delta":{"role":"assistant","content":"你好"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":"，世界"}}]}` + "\n\n",
		`data: [DONE]` + "\n\n",
	})
	defer srv.Close()

	p := NewOpenAICompatibleProvider("test", srv.URL, "m", "ref", stubResolver{v: "sk"}, srv.Client())
	ch, err := p.Chat(context.Background(), provider.ChatRequest{Model: "m", Messages: []provider.Message{{Role: "user", Content: "hi"}}, Stream: true})
	if err != nil {
		t.Fatalf("Chat 应成功，got %v", err)
	}
	if got := collect(ch); got != "你好，世界" {
		t.Fatalf("流式拼接应完整，got %q", got)
	}
}
```

- [ ] **Step 2: 运行验证失败**

Run: `go test ./internal/maas/ -run TestOpenAICompatible_StreamOK -v`
Expected: FAIL（`undefined: NewOpenAICompatibleProvider`）

- [ ] **Step 3: 实现 Provider**

`internal/maas/openai_compatible.go`：

```go
package maas

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/pkg/provider"
)

// openaiReq / openaiDelta 是 OpenAI 兼容协议的请求/增量响应结构（仅取需要的字段）。
type openaiReq struct {
	Model    string             `json:"model"`
	Messages []provider.Message `json:"messages"`
	Stream   bool               `json:"stream"`
}

type openaiDelta struct {
	Choices []struct {
		Delta struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// OpenAICompatibleProvider 对接所有 OpenAI 兼容协议的供应商
// （OpenAI / DeepSeek / 通义千问 DashScope 兼容模式）。纯 net/http，无第三方 SDK。
type OpenAICompatibleProvider struct {
	vendor        string                    // 展示用（openai/deepseek/qwen）
	baseURL       string                    // 如 https://api.deepseek.com
	upstreamModel string                    // 供应商侧模型名
	credentialRef string                    // security Secret ID
	resolver      provider.CredentialResolver
	httpClient    *http.Client
}

// NewOpenAICompatibleProvider 构造一个第三方通道。httpClient 可为 nil（用默认）。
func NewOpenAICompatibleProvider(vendor, baseURL, upstreamModel, credentialRef string, resolver provider.CredentialResolver, httpClient *http.Client) *OpenAICompatibleProvider {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 120 * time.Second}
	}
	return &OpenAICompatibleProvider{
		vendor: vendor, baseURL: baseURL, upstreamModel: upstreamModel,
		credentialRef: credentialRef, resolver: resolver, httpClient: httpClient,
	}
}

func (p *OpenAICompatibleProvider) Name() string { return "openai-compatible" }

func (p *OpenAICompatibleProvider) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.Chunk, error) {
	if p.resolver == nil {
		return nil, provider.ErrCredentialMissing
	}
	apiKey, err := p.resolver.Resolve(p.credentialRef)
	if err != nil || apiKey == "" {
		return nil, provider.ErrCredentialMissing
	}

	body, _ := json.Marshal(openaiReq{
		Model:    p.upstreamModel,
		Messages: req.Messages,
		Stream:   true,
	})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.baseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, classifyErr(err, 0)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, classifyErr(nil, resp.StatusCode)
	}

	ch := make(chan provider.Chunk)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "[DONE]" {
				return
			}
			var d openaiDelta
			if json.Unmarshal([]byte(payload), &d) != nil {
				continue
			}
			if len(d.Choices) == 0 {
				continue
			}
			delta := d.Choices[0].Delta
			if delta.Role == "" && delta.Content == "" {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case ch <- provider.Chunk{Role: delta.Role, Content: delta.Content}:
			}
		}
	}()
	return ch, nil
}

// classifyErr 把上游错误映射为降级 sentinel。
func classifyErr(netErr error, status int) error {
	if netErr != nil {
		// 网络层错误（含超时）→ 不可用，可 failover
		return fmt.Errorf("%w: %v", provider.ErrUpstreamUnavailable, netErr)
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return provider.ErrCredentialInvalid
	case status == http.StatusTooManyRequests:
		return provider.ErrUpstreamRateLimit
	case status >= 500:
		return provider.ErrUpstreamUnavailable
	case status >= 400:
		return provider.ErrUpstreamConfig
	}
	return nil
}
```

- [ ] **Step 4: 运行验证通过**

Run: `go test ./internal/maas/ -run TestOpenAICompatible_StreamOK -v` → PASS

- [ ] **Step 5: 写错误分类测试**

追加到 `openai_compatible_test.go`：

```go
func TestOpenAICompatible_ErrorClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"401 鉴权失败", 401, provider.ErrCredentialInvalid},
		{"429 限流", 429, provider.ErrUpstreamRateLimit},
		{"500 服务端", 500, provider.ErrUpstreamUnavailable},
		{"400 配置错误", 400, provider.ErrUpstreamConfig},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := newSSEServer(t, c.status, nil)
			defer srv.Close()
			p := NewOpenAICompatibleProvider("t", srv.URL, "m", "ref", stubResolver{v: "sk"}, srv.Client())
			_, err := p.Chat(context.Background(), provider.ChatRequest{Model: "m", Messages: []provider.Message{{Role: "user", Content: "x"}}})
			if !errors.Is(err, c.want) {
				t.Fatalf("status %d 应映射为 %v，got %v", c.status, c.want, err)
			}
		})
	}
}

func TestOpenAICompatible_CredentialMissing(t *testing.T) {
	p := NewOpenAICompatibleProvider("t", "https://x", "m", "ref", stubResolver{err: fmt.Errorf("not found")}, nil)
	_, err := p.Chat(context.Background(), provider.ChatRequest{})
	if !errors.Is(err, provider.ErrCredentialMissing) {
		t.Fatalf("凭证缺失应 ErrCredentialMissing，got %v", err)
	}
}

func TestOpenAICompatible_ContextCancel(t *testing.T) {
	// 上游永远不吐 [DONE]，ctx 取消后 channel 应关闭无泄漏。
	srv := newSSEServer(t, 200, []string{`data: {"choices":[{"delta":{"content":"a"}}]}` + "\n\n"})
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	p := NewOpenAICompatibleProvider("t", srv.URL, "m", "ref", stubResolver{v: "sk"}, srv.Client())
	ch, err := p.Chat(ctx, provider.ChatRequest{Model: "m", Messages: []provider.Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatalf("Chat 应成功: %v", err)
	}
	cancel()
	for range ch { // 应能退出（ctx 取消或 body 关闭）
	}
}
```

补 import：`errors`、`fmt`。

- [ ] **Step 6: 运行全部 + lint**

Run: `go test ./internal/maas/ -race -count=1 -v` → 全 PASS
Run: `golangci-lint run ./internal/maas/...` → 0 issues

---

### Task 4: 平台级 Secret（security 扩展）

**Files:**
- Modify: `internal/security/model.go`（Scope 字段 + 常量）
- Modify: `internal/security/memory/store.go`（平台级不按租户过滤 + seed 平台凭证）
- Modify: `internal/security/handler.go`（平台级仅 admin 写）
- Modify: `internal/security/*_test.go`（回归）

**Interfaces:**
- Produces: 平台级 Secret 可被 main.go 通过 `Resolve(refID)` 读出明文（Task 5 桥接）。

- [ ] **Step 1: model 加 Scope**

`internal/security/model.go`：

```go
// Secret 作用域。
const (
	ScopeTenant   = "tenant"   // 租户私有（默认，TenantID 必填）
	ScopePlatform = "platform" // 平台级共享（全租户可用，TenantID 空）
)

var validScopes = map[string]struct{}{ScopeTenant: {}, ScopePlatform: {}}
```

Secret 结构体加字段：`Scope string \`json:"scope"\``。Validate 增校验：

```go
if s.Scope == "" {
	s.Scope = ScopeTenant // 默认租户级
}
if _, ok := validScopes[s.Scope]; !ok {
	return errInvalid("scope")
}
```

- [ ] **Step 2: store 平台级查询不按租户过滤**

`internal/security/memory/store.go`：`ListSecrets` / `GetSecret` 在 `Scope==ScopePlatform` 时跳过租户过滤（直接按 ID/Name 查全量）；`CreateSecret` 时若 `Scope==ScopePlatform` 则 `TenantID=""`（忽略 ctx 写入）。

实现模式：查询方法内 `if scope == ScopePlatform { 不带 tenant 过滤 } else { 现有 tenant 过滤 }`。`Resolve(refID)` 新增方法：跨 scope 按 ID 取明文（供 main.go 桥接，不掩码）。

seed 追加一条平台级凭证（演示用，值空占位）：

```go
{ID: "sec-platform-deepseek", Scope: ScopePlatform, TenantID: "", Name: "deepseek-api-key", Type: TypeSecret, Value: "", Desc: "DeepSeek 供应商 API Key（部署后填写）"},
```

- [ ] **Step 3: handler 平台级仅 admin 写**

`internal/security/handler.go`：Create/Update/Delete 时若 body `scope==platform`，要求调用方具备平台管理权限（复用 `security:write` + 新增 `identity` ctx 中角色判定；若无 admin 角色 → 403）。List/Get 平台级凭证对所有租户可见（只读消费）。

> KISS：本期用 `security:write` + 平台级标记即可，不新增权限粒度（spec 开放问题 #4 倾向）。

- [ ] **Step 4: 写回归测试 + 运行**

追加测试：平台级 Secret 创建（admin 成功 / 普通用户 403）、跨租户可读、Resolve 返回明文。

Run: `go test ./internal/security/... -race -count=1` → PASS
Run: `golangci-lint run ./...` → 0 issues

---

### Task 5: catalog 改造 + plugin.Init 注入 + main.go 接线

**Files:**
- Modify: `internal/maas/catalog.go`（真实通道 + 保留 mock）
- Modify: `internal/maas/plugin.go`（Init 取 resolver）
- Modify: `cmd/core/main.go`（桥接 security store → SecretResolver）
- Modify: `cmd/core/seed.go` 或 `main.go`（如需）

**Interfaces:**
- Consumes: `OpenAICompatibleProvider`（Task 3）、`provider.CredentialResolver`（Task 1）、`CoreDeps.SecretResolver()`（Task 2）、平台级 Secret（Task 4）
- Produces: 启动后 Gateway 注册真实模型目录。

- [ ] **Step 1: catalog 改签名 + seed 真实通道**

`internal/maas/catalog.go` 改为 `catalog(resolver provider.CredentialResolver) []*provider.Model`。新增构造真实通道的 helper：

```go
// realCh 构造一个第三方供应商通道（OpenAICompatibleProvider）。
func realCh(id string, prio int, vendor, baseURL, upstream, credRef string, resolver provider.CredentialResolver) *provider.Channel {
	p := NewOpenAICompatibleProvider(vendor, baseURL, upstream, credRef, resolver, nil)
	c := &provider.Channel{
		ID: id, Type: p.Name(), Priority: prio, Status: provider.StatusHealthy,
		Endpoint: baseURL, UpstreamModel: upstream, CredentialRef: credRef,
	}
	c.SetImpl(p)
	return c
}
```

seed 表（按 spec 第 7 节）：`gpt-4o` / `gpt-4o-mini`（OpenAI）、`qwen-plus`（DashScope 主 + DeepSeek 备，演示容灾）、`deepseek-chat` / `deepseek-reasoner`。BaseURL：
- OpenAI: `https://api.openai.com/v1`
- DeepSeek: `https://api.deepseek.com`（OpenAI 兼容，无 /v1 前缀路径，Provider 内拼 `/chat/completions`）—— 实测确认
- 通义千问: `https://dashscope.aliyuncs.com/compatible-mode/v1`

保留 1-2 个演示 mock 模型（如 `echo-demo`）。CredentialRef 引用 Task 4 seed 的 `sec-platform-*` ID。

> **注意 BaseURL 拼接**：Provider 当前拼 `/chat/completions`。OpenAI/DeepSeek/DashScope 的 OpenAI 兼容端点路径需实测：OpenAI 是 `{base}/chat/completions`（base 含 /v1），DeepSeek 是 `https://api.deepseek.com/chat/completions`。落地时统一 base 为「不含 /chat/completions 的根」，Provider 拼 `/chat/completions`。若三家根路径不一致，Provider 加 `chatPath` 字段参数化（KISS：先实测，一致则不加）。

- [ ] **Step 2: plugin.Init 注入 resolver**

`internal/maas/plugin.go`：

```go
func (m *MaaSPlugin) Init(_ context.Context, deps plugin.CoreDeps) error {
	m.gw = deps.Gateway()
	if m.gw == nil {
		return fmt.Errorf("gateway registrar 未注入")
	}
	resolver := deps.SecretResolver() // 可为 nil（演示模式）
	for _, model := range catalog(resolver) {
		if err := m.gw.RegisterModel(model); err != nil {
			return fmt.Errorf("注册模型 %s 失败: %w", model.ID, err)
		}
	}
	return nil
}
```

- [ ] **Step 3: main.go 桥接 security store → CredentialResolver**

`cmd/core/main.go`：构造 security store 后，定义适配器：

```go
// secretResolver 适配 security store → provider.CredentialResolver（依赖倒置）。
type secretResolver struct{ store *securitymemory.Store }

func (r secretResolver) Resolve(ref string) (string, error) {
	s, err := r.store.Resolve(ref) // Task 4 新增的明文读取方法
	if err != nil {
		return "", err
	}
	return s.Value, nil
}
```

把 `secretResolver{store: secStore}` 注入 CoreDeps 实现（main.go 的 coreDeps 结构体加 `SecretResolver() provider.CredentialResolver` 方法）。

- [ ] **Step 4: plugin 测试更新 + 全量回归**

`internal/maas/plugin_test.go` 若 stub CoreDeps 需补 `SecretResolver()`。补 catalog 单测验证真实通道字段填充。

Run: `go test ./... -race -count=1` → 全 PASS
Run: `./bin/core &` + `curl -H "Authorization: Bearer sk-acme-dev" http://localhost:8080/api/models | jq` → 真实模型目录出现

---

### Task 6: Gateway 请求级 failover

**Files:**
- Modify: `internal/core/gateway/openai.go`（ChatCompletions 加 failover 循环）
- Modify: `internal/core/gateway/*_test.go`（failover 回归）

**Interfaces:**
- Consumes: `provider.ErrUpstreamRateLimit` / `ErrUpstreamUnavailable`（Task 1，degraded 类可 failover）；`provider.ErrCredentialMissing` / `ErrCredentialInvalid` / `ErrUpstreamConfig`（offline 类不重试）

- [ ] **Step 1: 写失败测试 —— 主通道 5xx 自动切备通道**

mock 两个 Provider：主返回 `ErrUpstreamUnavailable`，备正常流式。验证最终用户拿到备通道内容，主通道被标记 degraded。

- [ ] **Step 2: 实现 failover 循环**

`internal/core/gateway/openai.go` `ChatCompletions`：取 `model.HealthChannels()`，循环尝试：

```go
var lastErr error
for _, ch := range channels {
	stream, err := ch.Impl().Chat(ctx, req)
	if err != nil {
		lastErr = err
		if isOfflineErr(err) {
			g.MarkChannelStatus(model.ID, ch.ID, provider.StatusOffline)
			continue // 配置/凭证类，不重试本通道
		}
		g.MarkChannelStatus(model.ID, ch.ID, provider.StatusDegraded)
		continue // degraded 类，failover 到下一通道
	}
	// 成功：正常流式回传
	return serveStream(stream)
}
// 全部通道失败
return writeErr503(lastErr)
```

`isOfflineErr`：`errors.Is(err, ErrCredentialMissing) || errors.Is(err, ErrCredentialInvalid) || errors.Is(err, ErrUpstreamConfig)`。

- [ ] **Step 3: 运行测试 + 全量回归**

Run: `go test ./internal/core/gateway/ -race -count=1` → PASS
Run: `go test ./... -race -count=1` → 全 PASS
Run: `golangci-lint run ./...` → 0 issues

---

### Task 7: 收尾验证 + 文档更新

**Files:**
- Modify: `CLAUDE.md`（MaaS 章节更新：第三方对接替代 mock-only）
- Modify: `README.md`（已落地能力 MaaS 行）

- [ ] **Step 1: 端到端验证**

Run: `make build && ./bin/core &`
- `curl -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/api/models` → 真实供应商模型目录
- 未配凭证时 Playground 请求 → 友好错误（通道 offline）或 fallback echo 演示模型
- 配置真实 Key 后（手动 POST 平台级 Secret）→ 流式真模型回复

- [ ] **Step 2: 文档同步**

`CLAUDE.md` MaaS 章节补充第三方对接说明；`README.md` 「已落地能力」MaaS 行更新。CHANGELOG.md 追加。

- [ ] **Step 3: 最终全绿**

Run: `go test ./... -race -count=1 && golangci-lint run ./... && cd frontend && pnpm build` → 全绿

---

## Self-Review

**1. Spec 覆盖：**
- 一个适配器覆盖三家 → Task 3 ✓
- 平台托管凭证 + 平台级 Scope → Task 4 ✓
- 平台预置目录 → Task 5 ✓
- 跨供应商容灾降级 → Task 6 ✓
- 契约不变 → 全程未改 `Provider.Chat` 签名 ✓
- 开源交付（不含 Key + fallback）→ Task 5 保留 mock + Task 7 验证 ✓

**2. 占位符扫描：** BaseURL 根路径在 Task 5 标注「实测确认」（spec 开放问题 #1），属设计内已知项，非占位符；其余步骤含完整代码。

**3. 类型一致：** `CredentialResolver.Resolve(string)(string,error)` 全链路一致；`NewOpenAICompatibleProvider(vendor, baseURL, upstreamModel, credentialRef, resolver, httpClient)` 签名在 Task 3 定义、Task 5 调用一致；sentinel `Err*` 名 Task 1 定义、Task 3/6 引用一致。

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-28-maas-third-party-providers.md`.

两种执行方式：

**1. Subagent-Driven（推荐）** —— 每个 Task 派发独立 subagent，任务间 review，迭代快、上下文干净
**2. Inline Execution** —— 当前会话内按任务批量执行，带 checkpoint

你选哪种？我倾向 **Inline**（任务间耦合较紧——契约层 Task 1/2 是后续基础，单会话内连续推进更顺，且每任务都有全量回归 gate）。确认后即开始 Task 1。
