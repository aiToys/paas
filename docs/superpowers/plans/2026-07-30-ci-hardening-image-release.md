# CI 质量门加固 + 镜像发布 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为现有 `.github/workflows/ci.yml` 补 PG 集成测试 job、覆盖率上报 job、tag 触发的多平台镜像发布 job，使 CI 达开源生产标准。

**Architecture:** 纯 GitHub Actions YAML 增量，不改 Go 代码。三个新 job 独立追加：`test-pg`（postgres service container 跑 11 包集成测试 -p 1 串行）、`coverage`（coverprofile + artifact）、`release-image`（tag `v*` 触发，buildx 多平台推 ghcr.io，依赖全部检查 job）。`build` job 的 `needs` 加 `test-pg` 让构建等 PG 测试过。验收用 `actionlint` 校验 workflow 语法 + 本地 `docker build` 验证 Dockerfile 可构建。

**Tech Stack:** GitHub Actions（postgres service / setup-buildx / docker/login / metadata / build-push action）、actionlint（workflow 语法校验）、Docker buildx（linux/amd64 + linux/arm64 多平台）。

## Global Constraints

- Go 版本固定 `1.23`（所有 setup-go 与 Dockerfile 一致）。
- PG 集成测试包列表与 `Makefile test-pg` 目标 **完全一致**（11 包，按现有顺序）。
- PG 集成测试必须 `-p 1` 串行（各包 resetSchema 共享同一 database，并行互清）。
- `PAAS_TEST_PG_URL` 驱动集成测试，DSN = `postgres://paas:paas-dev@localhost:5432/paas?sslmode=disable`（job 内访问 service 走 `localhost:5432`）。
- `release-image` 仅在 `startsWith(github.ref, 'refs/tags/v')` 触发，且 `needs` 全部检查 job（任一失败不发布）。
- 镜像 registry = `ghcr.io`，镜像全名 `ghcr.io/aitoys/paas-core`（即 `ghcr.io/${{ github.repository }}-core`，repository=aitoys/paas）。
- 多平台 `linux/amd64,linux/arm64`。
- 鉴权用自动提供的 `GITHUB_TOKEN`（`packages: write`），无需额外 secret。
- license-check 必须仍拦截 GPL/AGPL；新增 GH Actions 均为主流官方 action（Apache/MIT）。
- 不引入 codecov 等第三方上报服务（开源纯净度优先），覆盖率走 artifact 上传。
- 未经用户明确要求不执行 `git commit` / 建分支（CLAUDE.md 硬约束）。

## 文件结构

- `.github/workflows/ci.yml`（修改）：追加 test-pg / coverage / release-image 三个 job；build job 的 needs 加 test-pg；顶部 `on` 加 `tags: [v*]` 触发。
- `docs/CONTRIBUTING.md`（修改，可选）：补「CI 如何工作 / 如何本地复现 test-pg / 如何发版打 tag」一节。
- `CHANGELOG.md`（修改）：加 CI 加固条目。
- `CLAUDE.md`（修改）：常用命令小节补「推 tag v* 触发镜像发布到 ghcr.io」。
- 不创建新文件，不改 Go 代码，不改 Dockerfile（已就绪）。

---

### Task 1: test-pg job（PG 集成测试进 CI）

**Files:**
- Modify: `.github/workflows/ci.yml`（在 `build` job 之后追加 `test-pg` job）

**Interfaces:**
- Consumes: `Makefile` 的 test-pg 目标所列 11 个集成测试包路径（作为 `go test` 的包参数，按原顺序）。
- Produces: 一个名为 `test-pg` 的 job，PR/push 到 main 时运行，起 postgres service 跑全 11 包集成测试。

- [ ] **Step 1: 写 test-pg job 的 YAML（postgres service + 11 包集成测试）**

在 `.github/workflows/ci.yml` 的 `build` job 之后追加：

```yaml
  test-pg:
    runs-on: ubuntu-latest
    services:
      postgres:
        image: postgres:16-alpine
        env:
          POSTGRES_DB: paas
          POSTGRES_USER: paas
          POSTGRES_PASSWORD: paas-dev
        ports: ["5432:5432"]
        options: >-
          --health-cmd "pg_isready -U paas"
          --health-interval 5s
          --health-timeout 5s
          --health-retries 12
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.23"
      - run: go mod download
      - name: PG 集成测试（11 包，-p 1 串行避免 resetSchema 共享 db 互清）
        env:
          PAAS_TEST_PG_URL: postgres://paas:paas-dev@localhost:5432/paas?sslmode=disable
        run: |
          go test -tags=integration -p 1 -count=1 \
            ./internal/core/identity/pg/ ./internal/core/application/pg/ \
            ./internal/environment/pg/ ./internal/appconfig/pg/ ./internal/dataservice/pg/ \
            ./internal/workload/pg/ ./internal/devops/pg/ ./internal/governance/pg/ \
            ./internal/configcenter/pg/ ./internal/billing/pg/ ./internal/security/pg/
```

- [ ] **Step 2: 用 actionlint 校验 workflow 语法（test-pg job 结构合法）**

Run（本地装了 actionlint）: `actionlint .github/workflows/ci.yml`
Expected: 无关于 test-pg job 的错误（若 actionlint 未装，见 Step 3 的 docker 本地等价验证）。

- [ ] **Step 3: 本地等价验证（CI 不可本地全跑，验证 test-pg job 的核心逻辑等价于 Makefile test-pg）**

确认 job 内 `go test` 的包列表与 `Makefile` 的 `test-pg` 目标逐包一致（11 包，顺序相同），`-p 1` 与 `-tags=integration` 与 Makefile 一致。本地跑 `make test-pg` 确认 11 包全 PASS（与 CI 将跑的命令等价）。

Run: `make test-pg`
Expected: 11 包集成测试全 PASS（本地 PG 已就绪前提下）。

- [ ] **Step 4: Commit（用户未要求 commit 时跳过此步，仅保留工作树改动）**

```bash
# 仅当用户明确要求提交时执行：
git add .github/workflows/ci.yml
git commit -m "ci: 新增 test-pg job（postgres service 跑 11 包集成测试）"
```

---

### Task 2: coverage job（覆盖率上报）

**Files:**
- Modify: `.github/workflows/ci.yml`（在 `test-pg` job 之后追加 `coverage` job）

**Interfaces:**
- Consumes: 无（独立 job，跑内存测试带覆盖率）。
- Produces: 一个名为 `coverage` 的 job，产出 `coverage.out` artifact 并在日志打印总覆盖率。

- [ ] **Step 1: 写 coverage job 的 YAML**

在 `test-pg` job 之后追加：

```yaml
  coverage:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.23"
      - name: 跑测试并生成覆盖率
        run: go test ./... -race -count=1 -coverprofile=coverage.out
      - name: 上传 coverage artifact
        uses: actions/upload-artifact@v4
        with:
          name: coverage
          path: coverage.out
      - name: 打印总覆盖率到日志
        run: go tool cover -func=coverage.out | tail -1
```

- [ ] **Step 2: 用 actionlint 校验 workflow 语法**

Run: `actionlint .github/workflows/ci.yml`
Expected: 无关于 coverage job 的错误。

- [ ] **Step 3: 本地等价验证（coverage job 的命令等价于本地 cover 目标的核心）**

本地跑 coverprofile 生成 + func 摘要，确认命令能产出覆盖率（与 CI 命令等价）。

Run: `go test ./... -race -count=1 -coverprofile=/tmp/cov.out && go tool cover -func=/tmp/cov.out | tail -1`
Expected: 输出 `total:	(statements)	XX.X%` 一行，非空。

- [ ] **Step 4: Commit（用户未要求 commit 时跳过）**

```bash
# 仅当用户明确要求提交时执行：
git add .github/workflows/ci.yml
git commit -m "ci: 新增 coverage job（coverprofile + artifact 上传）"
```

---

### Task 3: release-image job + 触发与依赖接线

**Files:**
- Modify: `.github/workflows/ci.yml`（顶部 `on:` 加 tags 触发；追加 `release-image` job；`build` job 的 needs 加 test-pg）

**Interfaces:**
- Consumes: 现有 `Dockerfile`（多阶段 distroless nonroot，已就绪）；全部检查 job 作为 needs 前置。
- Produces: 一个名为 `release-image` 的 job，仅在 tag `v*` 时触发，多平台 buildx 构建并推 `ghcr.io/aitoys/paas-core`；`build` job 现等 test-pg 通过。

- [ ] **Step 1: 顶部 on 加 tags 触发**

把 `.github/workflows/ci.yml` 顶部的 `on:` 从：

```yaml
on:
  push:
    branches: [main]
  pull_request:
    branches: [main]
```

改为：

```yaml
on:
  push:
    branches: [main]
    tags:
      - "v*"
  pull_request:
    branches: [main]
```

- [ ] **Step 2: build job 的 needs 加 test-pg**

把 `build` job 的：

```yaml
  build:
    runs-on: ubuntu-latest
    needs: [test, lint]
```

改为：

```yaml
  build:
    runs-on: ubuntu-latest
    needs: [test, lint, test-pg]
```

- [ ] **Step 3: 写 release-image job 的 YAML**

在 `coverage` job 之后追加：

```yaml
  release-image:
    if: startsWith(github.ref, 'refs/tags/v')
    needs: [test, lint, license-check, build, frontend, test-pg, coverage]
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-buildx-action@v3
      - uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      - uses: docker/metadata-action@v5
        id: meta
        with:
          images: ghcr.io/${{ github.repository }}-core
          tags: |
            type=semver,pattern={{version}}
            type=semver,pattern={{major}}.{{minor}}
            type=semver,pattern={{major}}
      - uses: docker/build-push-action@v6
        with:
          context: .
          file: ./Dockerfile
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          platforms: linux/amd64,linux/arm64
```

- [ ] **Step 4: 用 actionlint 校验 workflow 语法（on/needs/permissions/if 全合法）**

Run: `actionlint .github/workflows/ci.yml`
Expected: 全文件无错误（含 on.tags、release-image 的 if/needs/permissions、build 的 needs 变更）。

- [ ] **Step 5: 本地验证 Dockerfile 可构建（release-image 的构建逻辑等价于本地 docker build）**

确认本地用现有 Dockerfile 能构建出 core 镜像（release-image job 跑的是同一 Dockerfile）。

Run: `docker build -t paas-core:ci-check .`
Expected: 构建成功，`docker run --rm paas-core:ci-check`（无 DB 时 core 启动到内存后端，/livez 通）。

- [ ] **Step 6: Commit（用户未要求 commit 时跳过）**

```bash
# 仅当用户明确要求提交时执行：
git add .github/workflows/ci.yml
git commit -m "ci: 新增 release-image job（tag v* 触发多平台推 ghcr.io）+ build 依赖 test-pg"
```

---

### Task 4: 文档同步 + 全量验收

**Files:**
- Modify: `CHANGELOG.md`（加 CI 加固条目）
- Modify: `CLAUDE.md`（常用命令补发版说明）
- Modify: `docs/CONTRIBUTING.md`（补「CI 流水线」小节，若该文件存在）

**Interfaces:**
- Consumes: Task 1-3 完成的 ci.yml 改动。
- Produces: 文档与 CI 行为同步；全量 actionlint + 本地 build/test 绿。

- [ ] **Step 1: CHANGELOG 加条目**

在 `CHANGELOG.md` 顶部（最新版本区或 Unreleased 区）追加：

```markdown
- CI 质量门加固：新增 `test-pg` job（postgres service 跑全 11 模块 PG 集成测试，-p 1 串行）、`coverage` job（coverprofile + artifact）、`release-image` job（推 tag `v*` 触发，buildx 多平台 linux/amd64+arm64 推 `ghcr.io/aitoys/paas-core`）。`build` job 现依赖 `test-pg` 通过。
```

- [ ] **Step 2: CLAUDE.md 常用命令小节补发版说明**

在 `CLAUDE.md`「常用命令」后端部分末尾追加一行说明（紧跟现有 PG 集成测试注释之后）：

```markdown
# 发版：推 tag 触发 CI 镜像发布（git tag v0.1.0 && git push origin v0.1.0 → ghcr.io/aitoys/paas-core:0.1.0/0.1/0）
```

- [ ] **Step 3: CONTRIBUTING.md 补「CI 流水线」小节（若文件存在）**

先读 `docs/CONTRIBUTING.md` 或根 `CONTRIBUTING.md` 确认存在；存在则补一节说明 CI 五+三 job 作用、如何本地复现 test-pg（`make test-pg`）、如何发版打 tag。若不存在，跳过本步（YAGNI，不强造文件）。

- [ ] **Step 4: 全量验收**

逐条核对 spec 验收标准：

1. PR 提交 → test-pg job 跑（结构正确，11 包参数与 Makefile 一致）。
2. coverage job 产 coverage.out artifact + 日志打印总覆盖率。
3. tag `v*` → release-image 触发，多平台推 `ghcr.io/aitoys/paas-core`（version/major.minor/major 三 tag）。
4. 任一检查 job 失败 → release-image 不执行（needs 全覆盖）。
5. license-check 仍拦截 GPL/AGPL（未改 license-check job）。
6. 新增 action 均官方（actions/checkout, setup-go, upload-artifact, docker/*）。

Run（本地可验证项）:
```bash
actionlint .github/workflows/ci.yml        # 语法全绿
make build                                  # core 编译通过
make test                                   # 内存测试全绿
docker build -t paas-core:ci-check .        # Dockerfile 可构建
```
Expected: 四项全过。

- [ ] **Step 5: Commit（用户未要求 commit 时跳过）**

```bash
# 仅当用户明确要求提交时执行：
git add CHANGELOG.md CLAUDE.md docs/CONTRIBUTING.md
git commit -m "docs: 同步 CI 加固说明（CHANGELOG/CLAUDE/CONTRIBUTING）"
```

---

## Self-Review

**1. Spec coverage:**
- spec 验收标准 1（PR 触发 test-pg，11 包全 PASS）→ Task 1。✅
- spec 验收标准 2（coverage job 产 artifact + 日志总覆盖率）→ Task 2。✅
- spec 验收标准 3（tag v0.1.0 → release-image 触发推三 tag）→ Task 3。✅
- spec 验收标准 4（docker pull 可拉取运行）→ Task 3 Step 5 本地 docker build 验证 + Dockerfile 已 nonroot/distroless（CI 跑同 Dockerfile）。✅
- spec 验收标准 5（任一检查失败 release-image 不执行）→ Task 3 `needs` 全覆盖 + `if: startsWith(github.ref, 'refs/tags/v')`。✅
- spec 验收标准 6（license-check 仍拦截 + 新增 action 官方 Apache/MIT）→ Task 4 Step 4 核对。✅
- spec「触发与依赖」on.push.tags + release-image needs 全检查 job → Task 3 Step 1-3。✅
- spec「与现有 job 的关系」build needs 加 test-pg → Task 3 Step 2。✅

**2. Placeholder scan:** 无 TBD/TODO；每个 YAML 块为完整可用内容；commit 步显式标注「用户未要求 commit 时跳过」（遵守 CLAUDE.md 硬约束）。

**3. Type consistency:** job 名一致（`test-pg` / `coverage` / `release-image`，与 needs 列表逐字匹配）；PG 包列表与 Makefile 逐包对齐；DSN 与 Makefile 一致；镜像名 `ghcr.io/${{ github.repository }}-core`。

**已知限制：** GitHub Actions 的真实运行结果只能 push 后在 GH UI 观察，本地无法完整复现 service container / buildx 多平台。本地验收用 actionlint（语法）+ make test-pg（逻辑等价）+ docker build（Dockerfile 可构建）三层覆盖，最大化在 push 前发现问题。
