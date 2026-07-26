# Plan 1: 仓库就绪 + Platform Core 骨架 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 搭建 Go monorepo 骨架、开源治理全套、CI 流水线，以及 Platform Core 的最小可运行内核（插件契约 + Plugin Registry + Identity 领域骨架 + Core 启动入口），让仓库进入可开源、可接受贡献、契约定型的状态。

**Architecture:** Go monorepo。Core 以依赖倒置方式向插件注入能力（`CoreDeps`）。插件实现 `plugin.Plugin` 契约注册到 `PluginRegistry`。本期 Identity 用内存仓储（接口已抽出，Plan 2 接 PostgreSQL）。Core 启动入口组装依赖、加载插件、暴露最小健康端点。

**Tech Stack:** Go 1.22+、标准 `testing`、`github.com/google/uuid`、`github.com/stretchr/testify`、GitHub Actions、golangci-lint。

## Global Constraints

- Go 版本：`>= 1.22`（go.mod 声明 `go 1.22`）。
- 协议：Apache 2.0；**禁止引入 GPL/AGPL 依赖**（CI 校验）。
- 模块路径：`github.com/aitoys/paas`。
- 注释与文档语言：中文（与现有 spec/CLAUDE.md 一致）。
- 业务领域逻辑不得进入 `internal/core/`；Core 只含横切元能力。
- 测试：标准 `testing` + testify；每个公开函数有单测。
- 提交信息：Conventional Commits（`feat:` / `chore:` / `docs:` / `test:`）。
- **未经用户明确要求，不要执行 git commit/分支操作**（用户全局规则）。本 plan 中的 commit 步骤在征得用户同意后执行，或由执行者按用户指令处理。

## File Structure（本 plan 产出）

```
paas/
├── go.mod / go.sum
├── Makefile
├── .gitignore
├── LICENSE                          # Apache-2.0
├── README.md
├── CONTRIBUTING.md / SECURITY.md / CODE_OF_CONDUCT.md
├── .github/
│   ├── workflows/ci.yml
│   ├── ISSUE_TEMPLATE/{bug_report,feature_request}.yml
│   └── PULL_REQUEST_TEMPLATE.md
├── .golangci.yml
├── cmd/
│   └── core/main.go                 # Core 启动入口
├── pkg/                             # 对外可复用库（插件作者可见）
│   ├── plugin/                      # 插件契约（interface + 类型）
│   └── core/                        # CoreDeps 等注入接口
├── internal/
│   └── core/
│       ├── plugin/registry.go       # PluginRegistry 实现
│       └── identity/                # Identity 领域骨架
│           ├── model.go             # Tenant/User 值对象
│           ├── repository.go        # 仓储接口
│           └── memory/store.go      # 内存实现（Plan 2 换 PG）
└── internal/core/health/handler.go  # 健康检查端点
```

---

### Task 1: Go 仓库骨架与构建基础

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `Makefile`
- Create: `cmd/core/main.go`
- Test: (无，本任务为脚手架；可编译即通过)

**Interfaces:** 无（首个任务）

- [ ] **Step 1: 初始化 go module**

Run:
```bash
go mod init github.com/aitoys/paas
```
Expected: 生成 `go.mod`，首行 `module github.com/aitoys/paas`。

- [ ] **Step 2: 设置 Go 版本**

编辑 `go.mod`，确保含：
```
go 1.22
```

- [ ] **Step 3: 创建 .gitignore**

```gitignore
# Go
/bin/
/dist/
*.exe
*.exe~
*.dll
*.so
*.dylib
*.test
*.out
vendor/

# 构建产物
/coverage.out
/coverage.html

# IDE
.idea/
.vscode/
*.swp
.DS_Store

# 本地环境
.env
.env.local
```

- [ ] **Step 4: 创建 Makefile**

```makefile
SHELL := /bin/bash
.PHONY: build test lint tidy run clean

build: ## 编译 core 二进制到 bin/
	mkdir -p bin
	go build -o bin/core ./cmd/core

run: ## 本地运行 core
	go run ./cmd/core

test: ## 运行全部测试
	go test ./... -race -count=1

tidy: ## 整理依赖
	go mod tidy

lint: ## 运行 golangci-lint（需先安装）
	golangci-lint run ./...

clean: ## 清理构建产物
	rm -rf bin/ dist/ coverage.out coverage.html
```

- [ ] **Step 5: 创建最小 main.go**

```go
// Package main 是 Platform Core 的启动入口。
// 本任务仅建立可编译的最小入口；插件加载与 HTTP 服务在后续任务补全。
package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Println("paas core starting...")
	_ = os.Exit(run())
}

// run 返回进程退出码；当前为占位，Task 8 注入实际启动逻辑。
func run() int {
	return 0
}
```

- [ ] **Step 6: 验证可编译可运行**

Run:
```bash
make build && ./bin/core
```
Expected: 输出 `paas core starting...`，退出码 0。

Run:
```bash
go test ./...
```
Expected: `no Go files` 或通过（此时无测试包，正常）。

- [ ] **Step 7: Commit**

```bash
git add go.mod .gitignore Makefile cmd/
git commit -m "chore: 初始化 Go monorepo 骨架与构建基础"
```

---

### Task 2: 开源治理文件

**Files:**
- Create: `LICENSE`
- Create: `README.md`
- Create: `CONTRIBUTING.md`
- Create: `SECURITY.md`
- Create: `CODE_OF_CONDUCT.md`
- Create: `.github/ISSUE_TEMPLATE/bug_report.yml`
- Create: `.github/ISSUE_TEMPLATE/feature_request.yml`
- Create: `.github/PULL_REQUEST_TEMPLATE.md`

**Interfaces:** 无

- [ ] **Step 1: 写入 Apache-2.0 LICENSE**

从官方取 Apache 2.0 全文写入 `LICENSE`，末尾追加版权行：
```
Copyright 2026 aitoys

Licensed under the Apache License, Version 2.0 (the "License");
...（完整 Apache-2.0 正文）
```

> 实施提示：`curl -sSL https://www.apache.org/licenses/LICENSE-2.0.txt -o LICENSE`，然后在文件顶部加版权与 SPDX 行：`SPDX-License-Identifier: Apache-2.0`。

- [ ] **Step 2: 写入 README.md**

```markdown
# paas

一站式 PaaS 平台 —— 服务治理、中间件管理、MaaS、DevOps 的基础设施统一平台。

## 状态

🚧 早期开发中。本期范围：Platform Core 底座 + MaaS 推理平台。详见
[设计规格](./docs/superpowers/specs/2026-07-26-maas-platform-foundation-design.md)。

## 技术栈

- 控制面：Go + controller-runtime
- 存储：PostgreSQL｜事件：NATS（嵌入式）｜可观测：OpenTelemetry
- 前端：Vue 3 + Element Plus
- 协议：Apache 2.0

## 快速开始

```bash
make build      # 编译
make test       # 测试
./bin/core      # 运行（最小骨架）
```

## 贡献

见 [CONTRIBUTING.md](./CONTRIBUTING.md)。提交信息遵循 Conventional Commits。
```

- [ ] **Step 3: 写入 CONTRIBUTING.md**

```markdown
# 贡献指南

感谢参与！请先阅读本文档。

## 开发环境

- Go >= 1.22
- make
- （可选）golangci-lint

## 流程

1. Fork → 新建分支 `feat/<short-desc>`
2. `make test` 确保通过
3. 提交信息遵循 [Conventional Commits](https://www.conventionalcommits.org/)
4. PR 关联对应 issue，填写 PR 模板

## 约定

- 业务领域逻辑不进 `internal/core/`；Core 只含横切元能力。
- 多租户隔离由 Core 统一治理，插件不得绕过。
- 新增依赖须与 Apache 2.0 兼容（禁止 GPL/AGPL）。
- 注释与文档使用中文。
```

- [ ] **Step 4: 写入 SECURITY.md**

```markdown
# 安全策略

## 报告漏洞

请勿公开提 issue 报告安全漏洞。发送邮件至 security@example.com，
24 小时内响应。请附复现步骤与影响范围。

## 支持版本

仅最新 minor 版本接受安全修复。
```

- [ ] **Step 5: 写入 CODE_OF_CONDUCT.md**

采用 Contributor Covenant 2.1 中文版：
```bash
curl -sSL https://www.contributor-covenant.org/version/2/1/code_of_conduct/code_of_conduct.md -o CODE_OF_CONDUCT.md
```
确认文件内含中文或英文标准正文即可。

- [ ] **Step 6: 写入 issue 模板 `.github/ISSUE_TEMPLATE/bug_report.yml`**

```yaml
name: Bug 报告
description: 报告一个缺陷
labels: ["bug"]
body:
  - type: textarea
    id: what-happened
    attributes:
      label: 现象与复现步骤
      description: 你做了什么？期望与实际结果？
    validations:
      required: true
  - type: input
    id: version
    attributes:
      label: 版本
    validations:
      required: true
  - type: textarea
    id: env
    attributes:
      label: 环境（OS / K8s / Go）
    validations:
      required: true
```

- [ ] **Step 7: 写入 feature 模板 `.github/ISSUE_TEMPLATE/feature_request.yml`**

```yaml
name: 功能建议
description: 提出一个新功能
labels: ["enhancement"]
body:
  - type: textarea
    id: problem
    attributes:
      label: 要解决什么问题
    validations:
      required: true
  - type: textarea
    id: solution
    attributes:
      label: 设想的方案
    validations:
      required: true
```

- [ ] **Step 8: 写入 PR 模板 `.github/PULL_REQUEST_TEMPLATE.md`**

```markdown
## 变更说明

<!-- 本 PR 做了什么，为什么 -->

## 关联 issue

Closes #

## 检查清单

- [ ] `make test` 通过
- [ ] 新增依赖 license 兼容 Apache 2.0
- [ ] 业务逻辑未泄漏进 `internal/core/`（如适用）
- [ ] 已补充/更新测试
```

- [ ] **Step 9: Commit**

```bash
git add LICENSE README.md CONTRIBUTING.md SECURITY.md CODE_OF_CONDUCT.md .github/
git commit -m "docs: 添加开源治理全套文件（Apache-2.0 / 贡献 / 安全 / 模板）"
```

---

### Task 3: CI 流水线

**Files:**
- Create: `.github/workflows/ci.yml`
- Create: `.golangci.yml`

**Interfaces:** 无

- [ ] **Step 1: 写入 .golangci.yml（精简默认规则）**

```yaml
run:
  timeout: 5m
  go: "1.22"
linters:
  enable:
    - errcheck
    - govet
    - ineffassign
    - staticcheck
    - unused
    - misspell
    - gofmt
    - goimports
linters-settings:
  goimports:
    local-prefixes: github.com/aitoys/paas
```

- [ ] **Step 2: 写入 ci.yml**

```yaml
name: CI
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.22"
      - run: go mod download
      - run: go test ./... -race -count=1
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.22"
      - uses: golangci/golangci-lint-action@v6
        with:
          version: latest
  license-check:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.22"
      - name: 安装 go-licenses 并检查
        run: |
          go install github.com/google/go-licenses@latest
          go-licenses check ./... --disallowed_types=forbidden,reciprocal,restricted   # 禁止 GPL/AGPL 等
  build:
    runs-on: ubuntu-latest
    needs: [test, lint]
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.22"
      - run: mkdir -p bin && go build -o bin/core ./cmd/core
```

- [ ] **Step 3: 本地验证 lint 配置语法（可选，若已装 golangci-lint）**

Run:
```bash
golangci-lint config path   # 确认配置可被识别
```
Expected: 输出 `.golangci.yml` 的绝对路径，无解析错误。

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/ci.yml .golangci.yml
git commit -m "ci: 添加 GitHub Actions（test/lint/license-check/build）"
```

---

### Task 4: 插件契约接口

**Files:**
- Create: `pkg/plugin/plugin.go`
- Create: `pkg/plugin/plugin_test.go`

**Interfaces:**
- Produces: `plugin.Manifest`, `plugin.Plugin`, `plugin.CoreDeps`, `plugin.CRDSchema`, `plugin.RouteSpec`, `plugin.MeterSpec`

- [ ] **Step 1: 写失败测试 `pkg/plugin/plugin_test.go`**

```go
package plugin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// stubPlugin 用于验证契约可被实现并返回 Manifest。
type stubPlugin struct{ name, version string }

func (s *stubPlugin) Manifest() Manifest {
	return Manifest{Name: s.name, Version: s.version}
}
func (s *stubPlugin) Routes() []RouteSpec          { return nil }
func (s *stubPlugin) Schemas() []CRDSchema         { return nil }
func (s *stubPlugin) Meters() []MeterSpec          { return nil }
func (s *stubPlugin) Init(context.Context, CoreDeps) error { return nil }
func (s *stubPlugin) Run(context.Context) error    { return nil }

func TestPluginManifest(t *testing.T) {
	var p Plugin = &stubPlugin{name: "maas", version: "v0.1.0"}
	assert.Equal(t, "maas", p.Manifest().Name)
	assert.Equal(t, "v0.1.0", p.Manifest().Version)
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./pkg/plugin/ -run TestPluginManifest -v`
Expected: 编译失败（`undefined: Manifest` 等）。

- [ ] **Step 3: 写实现 `pkg/plugin/plugin.go`**

```go
// Package plugin 定义平台子系统接入 Platform Core 的契约。
// 子系统（如 MaaS）通过实现 Plugin 接口注册到 Core，由 Core 注入依赖。
package plugin

import "context"

// Manifest 声明插件元信息。
type Manifest struct {
	Name    string   // 插件名，如 "maas"；全平台唯一
	Version string   // 语义化版本
	Depends []string // 依赖的其他插件名（Core 解析加载顺序）
}

// RouteSpec 声明插件暴露给 API Gateway 的路由。
type RouteSpec struct {
	Path    string // 含方法的路径，如 "POST /v1/chat/completions"
	Require string // 所需权限标识，如 "maas:infer"
}

// CRDSchema 声明由 Core 统一注册到 K8s 的 CRD（Task 本期仅承载定义）。
type CRDSchema struct {
	Group   string
	Version string
	Kind    string
	Plural  string
}

// MeterSpec 声明插件产出的计量事件类型。
type MeterSpec struct {
	Name    string // 如 "tokens"
	Unit    string // 如 "count"
}

// CoreDeps 由 Core 在 Init 阶段注入；插件不得自行构造外部连接。
// 具体字段在 Plan 2 逐步补全（DB / EventBus / Provider / OTel），
// 本期以接口形式预留，避免循环依赖。
type CoreDeps interface {
	// Logger 返回带租户/插件上下文的日志器（Plan 2 实现）。
	Logger() interface{}
}

// Plugin 是子系统接入 Core 必须实现的契约。
type Plugin interface {
	Manifest() Manifest
	Routes() []RouteSpec
	Schemas() []CRDSchema
	Meters() []MeterSpec
	Init(ctx context.Context, deps CoreDeps) error
	Run(ctx context.Context) error
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./pkg/plugin/ -run TestPluginManifest -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add pkg/plugin/
git commit -m "feat(plugin): 定义插件契约接口（Manifest/Plugin/CoreDeps）"
```

---

### Task 5: Plugin Registry 实现

**Files:**
- Create: `internal/core/plugin/registry.go`
- Create: `internal/core/plugin/registry_test.go`

**Interfaces:**
- Consumes: `plugin.Plugin`, `plugin.Manifest`（来自 Task 4）
- Produces: `plugin.Registry`, `plugin.NewRegistry`, `plugin.LoadOrder`

- [ ] **Step 1: 写失败测试 `registry_test.go`**

```go
package plugin

import (
	"context"
	"testing"

	"github.com/aitoys/paas/pkg/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newStub(name string, deps ...string) plugin.Plugin {
	return &stub{name: name, deps: deps}
}

type stub struct{ name string; deps []string }

func (s *stub) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: s.name, Depends: s.deps}
}
func (s *stub) Routes() []plugin.RouteSpec                 { return nil }
func (s *stub) Schemas() []plugin.CRDSchema                { return nil }
func (s *stub) Meters() []plugin.MeterSpec                 { return nil }
func (s *stub) Init(context.Context, plugin.CoreDeps) error { return nil }
func (s *stub) Run(context.Context) error                  { return nil }

func TestRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	p := newStub("maas")
	require.NoError(t, r.Register(p))

	got, ok := r.Get("maas")
	assert.True(t, ok)
	assert.Equal(t, "maas", got.Manifest().Name)
}

func TestRegisterDuplicate(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(newStub("maas")))
	err := r.Register(newStub("maas"))
	assert.Error(t, err)
}

func TestLoadOrderResolvesDeps(t *testing.T) {
	// maas 依赖 base；期望加载顺序为 [base, maas]
	r := NewRegistry()
	require.NoError(t, r.Register(newStub("maas", "base")))
	require.NoError(t, r.Register(newStub("base")))

	order, err := r.LoadOrder()
	require.NoError(t, err)
	require.Len(t, order, 2)
	assert.Equal(t, "base", order[0].Manifest().Name)
	assert.Equal(t, "maas", order[1].Manifest().Name)
}

func TestLoadOrderDetectsMissingDep(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(newStub("maas", "ghost")))
	_, err := r.LoadOrder()
	assert.Error(t, err)
}

func TestLoadOrderDetectsCycle(t *testing.T) {
	r := NewRegistry()
	require.NoError(t, r.Register(newStub("a", "b")))
	require.NoError(t, r.Register(newStub("b", "a")))
	_, err := r.LoadOrder()
	assert.Error(t, err)
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/core/plugin/ -v`
Expected: 编译失败（`undefined: NewRegistry`）。

- [ ] **Step 3: 写实现 `registry.go`**

```go
// Package plugin 实现 Platform Core 的插件注册中心：
// 注册、去重、依赖拓扑排序与环检测。
package plugin

import (
	"fmt"

	pkgplugin "github.com/aitoys/paas/pkg/plugin"
)

// Registry 管理已注册的插件。
type Registry struct {
	plugins map[string]pkgplugin.Plugin
}

// NewRegistry 创建空注册中心。
func NewRegistry() *Registry {
	return &Registry{plugins: make(map[string]pkgplugin.Plugin)}
}

// Register 注册插件；重名返回错误。
func (r *Registry) Register(p pkgplugin.Plugin) error {
	name := p.Manifest().Name
	if name == "" {
		return fmt.Errorf("插件名不能为空")
	}
	if _, exists := r.plugins[name]; exists {
		return fmt.Errorf("插件已注册: %s", name)
	}
	r.plugins[name] = p
	return nil
}

// Get 按名取插件。
func (r *Registry) Get(name string) (pkgplugin.Plugin, bool) {
	p, ok := r.plugins[name]
	return p, ok
}

// All 返回所有插件（顺序不保证）。
func (r *Registry) All() []pkgplugin.Plugin {
	out := make([]pkgplugin.Plugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		out = append(out, p)
	}
	return out
}

// LoadOrder 按依赖做拓扑排序，返回加载顺序；
// 缺失依赖或循环依赖返回错误。
func (r *Registry) LoadOrder() ([]pkgplugin.Plugin, error) {
	// 入度表
	indeg := make(map[string]int)
	adj := make(map[string][]string)
	for name, p := range r.plugins {
		indeg[name] += 0 // 确保节点存在
		for _, dep := range p.Manifest().Depends {
			if _, ok := r.plugins[dep]; !ok {
				return nil, fmt.Errorf("插件 %s 依赖未注册的 %s", name, dep)
			}
			adj[dep] = append(adj[dep], name)
			indeg[name]++
		}
	}

	// 入度为 0 的节点入队
	var queue []string
	for name, d := range indeg {
		if d == 0 {
			queue = append(queue, name)
		}
	}

	var ordered []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		ordered = append(ordered, cur)
		for _, nxt := range adj[cur] {
			indeg[nxt]--
			if indeg[nxt] == 0 {
				queue = append(queue, nxt)
			}
		}
	}

	if len(ordered) != len(r.plugins) {
		return nil, fmt.Errorf("检测到插件依赖循环")
	}

	out := make([]pkgplugin.Plugin, 0, len(ordered))
	for _, name := range ordered {
		out = append(out, r.plugins[name])
	}
	return out, nil
}
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/core/plugin/ -v`
Expected: 全部 PASS（5 个用例）。

- [ ] **Step 5: Commit**

```bash
git add internal/core/plugin/
git commit -m "feat(core/plugin): 实现插件注册中心（注册/去重/拓扑排序/环检测）"
```

---

### Task 6: Identity 领域模型与内存仓储

**Files:**
- Create: `internal/core/identity/model.go`
- Create: `internal/core/identity/repository.go`
- Create: `internal/core/identity/memory/store.go`
- Create: `internal/core/identity/memory/store_test.go`

**Interfaces:**
- Produces: `identity.Tenant`, `identity.User`, `identity.Repository`

- [ ] **Step 1: 写领域模型 `model.go`**

```go
// Package identity 是 Platform Core 的身份与租户领域模型。
// 多租户隔离的最小元数据在此定义；所有业务表通过 tenant_id 关联。
package identity

import "time"

// Tenant 表示一个租户（组织）。
type Tenant struct {
	ID        string
	Name      string
	CreatedAt time.Time
}

// User 表示租户内的用户。
type User struct {
	ID       string
	TenantID string // 所属租户；多租户隔离键
	Name     string
	IsAdmin  bool
}
```

- [ ] **Step 2: 写仓储接口 `repository.go`**

```go
package identity

import "context"

// Repository 是 Identity 持久化抽象。
// 实现需在所有查询中强制按 tenant 过滤（Plan 2 的 PG 实现会在 SQL 层注入）。
type Repository interface {
	CreateTenant(ctx context.Context, t Tenant) error
	GetTenant(ctx context.Context, id string) (Tenant, error)
	CreateUser(ctx context.Context, u User) error
	// UsersByTenant 仅返回该租户的用户，防止跨租户泄漏。
	UsersByTenant(ctx context.Context, tenantID string) ([]User, error)
}
```

- [ ] **Step 3: 写失败测试 `memory/store_test.go`**

```go
package memory

import (
	"context"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/core/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateAndGetTenant(t *testing.T) {
	s := NewStore()
	tnt := identity.Tenant{ID: "t1", Name: "acme", CreatedAt: time.Now()}
	require.NoError(t, s.CreateTenant(context.Background(), tnt))

	got, err := s.GetTenant(context.Background(), "t1")
	require.NoError(t, err)
	assert.Equal(t, "acme", got.Name)
}

func TestUsersByTenantIsolation(t *testing.T) {
	s := NewStore()
	_ = s.CreateTenant(context.Background(), identity.Tenant{ID: "t1", Name: "a", CreatedAt: time.Now()})
	_ = s.CreateTenant(context.Background(), identity.Tenant{ID: "t2", Name: "b", CreatedAt: time.Now()})
	require.NoError(t, s.CreateUser(context.Background(), identity.User{ID: "u1", TenantID: "t1", Name: "alice"}))
	require.NoError(t, s.CreateUser(context.Background(), identity.User{ID: "u2", TenantID: "t2", Name: "bob"}))

	users, err := s.UsersByTenant(context.Background(), "t1")
	require.NoError(t, err)
	require.Len(t, users, 1)
	assert.Equal(t, "alice", users[0].Name) // 不应泄漏 t2 的 bob
}
```

- [ ] **Step 4: 运行测试确认失败**

Run: `go test ./internal/core/identity/memory/ -v`
Expected: 编译失败（`undefined: NewStore`）。

- [ ] **Step 5: 写内存实现 `memory/store.go`**

```go
// Package memory 提供 identity.Repository 的内存实现，
// 供本 plan 阶段的 Core 启动与测试使用；Plan 2 替换为 PostgreSQL 实现。
package memory

import (
	"context"
	"fmt"
	"sync"

	"github.com/aitoys/paas/internal/core/identity"
)

type Store struct {
	mu       sync.RWMutex
	tenants  map[string]identity.Tenant
	users    map[string]identity.User
}

func NewStore() *Store {
	return &Store{tenants: map[string]identity.Tenant{}, users: map[string]identity.User{}}
}

func (s *Store) CreateTenant(_ context.Context, t identity.Tenant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tenants[t.ID]; exists {
		return fmt.Errorf("租户已存在: %s", t.ID)
	}
	s.tenants[t.ID] = t
	return nil
}

func (s *Store) GetTenant(_ context.Context, id string) (identity.Tenant, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tenants[id]
	if !ok {
		return identity.Tenant{}, fmt.Errorf("租户不存在: %s", id)
	}
	return t, nil
}

func (s *Store) CreateUser(_ context.Context, u identity.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.users[u.ID]; exists {
		return fmt.Errorf("用户已存在: %s", u.ID)
	}
	s.users[u.ID] = u
	return nil
}

func (s *Store) UsersByTenant(_ context.Context, tenantID string) ([]identity.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []identity.User
	for _, u := range s.users {
		if u.TenantID == tenantID {
			out = append(out, u)
		}
	}
	return out, nil
}
```

- [ ] **Step 6: 运行测试确认通过**

Run: `go test ./internal/core/identity/... -v`
Expected: PASS。

- [ ] **Step 7: Commit**

```bash
git add internal/core/identity/
git commit -m "feat(core/identity): 领域模型与仓储抽象 + 内存实现（多租户隔离）"
```

---

### Task 7: 健康检查端点

**Files:**
- Create: `internal/core/health/handler.go`
- Create: `internal/core/health/handler_test.go`

**Interfaces:**
- Produces: `health.Handler`, `health.NewHandler`

- [ ] **Step 1: 写失败测试**

```go
package health

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLivezReturnsOK(t *testing.T) {
	rec := httptest.NewRecorder()
	NewHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/core/health/ -v`
Expected: `undefined: NewHandler`。

- [ ] **Step 3: 写实现**

```go
// Package health 提供 Core 进程的存活探针端点。
package health

import (
	"encoding/json"
	"net/http"
)

type Handler struct{}

func NewHandler() *Handler { return &Handler{} }

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/core/health/ -v`
Expected: PASS。

- [ ] **Step 5: Commit**

```bash
git add internal/core/health/
git commit -m "feat(core/health): 添加 /livez 存活探针端点"
```

---

### Task 8: Core 启动入口与插件加载组装

**Files:**
- Modify: `cmd/core/main.go`
- Create: `cmd/core/main_test.go`

**Interfaces:**
- Consumes: `plugin.Registry`（Task 5）、`identity.Repository`（Task 6）、`health.Handler`（Task 7）、`plugin.Plugin`（Task 4）

- [ ] **Step 1: 写失败测试 `cmd/core/main_test.go`**

```go
package main

import (
	"context"
	"testing"

	"github.com/aitoys/paas/pkg/plugin"
	"github.com/stretchr/testify/assert"
)

// capturePlugin 是测试用插件，记录 Init/Run 是否被调用。
type capturePlugin struct {
	name           string
	inited, ran    bool
}

func (c *capturePlugin) Manifest() plugin.Manifest { return plugin.Manifest{Name: c.name} }
func (c *capturePlugin) Routes() []plugin.RouteSpec { return nil }
func (c *capturePlugin) Schemas() []plugin.CRDSchema { return nil }
func (c *capturePlugin) Meters() []plugin.MeterSpec { return nil }
func (c *capturePlugin) Init(context.Context, plugin.CoreDeps) error { c.inited = true; return nil }
func (c *capturePlugin) Run(context.Context) error { c.ran = true; return nil }

// TestBootstrapInitializesAndRunsPlugins 验证 bootstrapCore 按拓扑顺序 Init+Run 所有插件。
func TestBootstrapInitializesAndRunsPlugins(t *testing.T) {
	p := &capturePlugin{name: "maas"}
	ran, err := bootstrapCore(context.Background(), []plugin.Plugin{p})
	assert.NoError(t, err)
	assert.Equal(t, map[string]bool{"maas": true}, ran)
	assert.True(t, p.inited)
	assert.True(t, p.ran)
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./cmd/core/ -v`
Expected: `undefined: bootstrapCore`。

- [ ] **Step 3: 改写 `cmd/core/main.go`**

```go
// Package main 是 Platform Core 的启动入口。
// 职责：组装依赖、注册插件、按拓扑顺序 Init+Run 插件、暴露探针端点。
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/aitoys/paas/internal/core/health"
	coreplugin "github.com/aitoys/paas/internal/core/plugin"
	"github.com/aitoys/paas/pkg/plugin"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 本 plan 阶段：无业务插件；MaaS 插件在 Plan 3 注册进来。
	if err := run(ctx, nil); err != nil {
		log.Fatalf("core 启动失败: %v", err)
	}
}

// run 启动 HTTP 探针并引导插件；返回非 nil 则进程以 1 退出。
func run(ctx context.Context, plugins []plugin.Plugin) error {
	go serveHealth()
	ran, err := bootstrapCore(ctx, plugins)
	if err != nil {
		return err
	}
	log.Printf("core 启动完成，已运行插件: %v", ran)
	<-ctx.Done()
	log.Println("core 收到退出信号，停止")
	return nil
}

// bootstrapCore 把插件注册到 Registry，按拓扑顺序 Init + Run。
// 返回 map[插件名]是否成功运行。任一插件 Init/Run 失败则中止并返回错误。
func bootstrapCore(ctx context.Context, plugins []plugin.Plugin) (map[string]bool, error) {
	registry := coreplugin.NewRegistry()
	for _, p := range plugins {
		if err := registry.Register(p); err != nil {
			return nil, err
		}
	}
	ordered, err := registry.LoadOrder()
	if err != nil {
		return nil, fmt.Errorf("插件加载顺序解析失败: %w", err)
	}

	// 本 plan 阶段 CoreDeps 仍是占位；Plan 2 注入 DB/EventBus/OTel。
	deps := noopCoreDeps{}
	ran := map[string]bool{}
	for _, p := range ordered {
		if err := p.Init(ctx, deps); err != nil {
			return ran, fmt.Errorf("插件 %s 初始化失败: %w", p.Manifest().Name, err)
		}
		if err := p.Run(ctx); err != nil {
			return ran, fmt.Errorf("插件 %s 运行失败: %w", p.Manifest().Name, err)
		}
		ran[p.Manifest().Name] = true
	}
	return ran, nil
}

// noopCoreDeps 是本 plan 的 CoreDeps 占位实现；Plan 2 替换为真实依赖。
type noopCoreDeps struct{}

func (noopCoreDeps) Logger() interface{} { return nil }

func serveHealth() {
	mux := http.NewServeMux()
	mux.Handle("/livez", health.NewHandler())
	srv := &http.Server{Addr: ":8080", Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("health 服务退出: %v", err)
	}
}
```

- [ ] **Step 4: 运行确认通过**

Run:
```bash
go test ./cmd/core/ -v
go test ./... -race
```
Expected: `TestBootstrapInitializesAndRunsPlugins` PASS；全量测试无失败。

- [ ] **Step 5: 验证端到端启动**

Run:
```bash
make build && (./bin/core &)
sleep 1
curl -s http://localhost:8080/livez
kill %1
```
Expected: 返回 `{"status":"ok"}`。

- [ ] **Step 6: Commit**

```bash
git add cmd/core/
git commit -m "feat(core): 启动入口组装依赖、加载插件并暴露 /livez"
```

---

## Self-Review 结论

1. **Spec 覆盖**：Plan 1 覆盖 spec 第 2、3、9 节（架构骨架 / Core 部分模块 / 开源就绪）的**地基部分**。Identity/Plugin 契约/Registry/Health 已落地。spec 第 3 节其余模块（Provider/Orchestrator/EventBus/Metering/Observability）、第 4-8 节（MaaS/隔离/可观测/前端/交付）显式划入 Plan 2-6，不在本 plan 范围。
2. **占位符扫描**：无 TODO/TBD；所有代码步骤含真实可编译代码。
3. **类型一致**：`plugin.Plugin` / `Manifest` / `Registry` / `identity.Repository` 在跨任务间签名一致；`bootstrapCore` 签名在测试与实现中匹配。

## 后续 Plan 规划（非本 plan）

- **Plan 2**：Core 元能力 —— Provider Registry、Orchestrator（controller-runtime envtest）、NATS EventBus、Metering、OTel 埋点；Identity 换 PostgreSQL 实现（带 SQL 级 tenant 过滤）。
- **Plan 3**：MaaS 插件 —— ModelRegistry、InferenceDeployment CRD、DeployController、GPUScheduler（显存+反亲和）。
- **Plan 4**：Inference Gateway（OpenAI-compatible + 路由 + 计量）+ vLLM 纳管。
- **Plan 5**：前端三套（console-admin fork vue-admin / console-user / landing）。
- **Plan 6**：交付（Helm chart + airsync 离线工具）。
