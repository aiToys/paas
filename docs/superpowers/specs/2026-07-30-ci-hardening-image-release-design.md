# CI 质量门加固 + 镜像发布设计

**日期**：2026-07-30
**状态**：待评审
**关联**：`2026-07-29-persistence-remaining-modules-design.md`（11 模块 PG 集成测试需 CI 回归保护）、`CONTRIBUTING.md`、`Dockerfile`（已存在多阶段 distroless）

## 背景与动机

现有 CI（`.github/workflows/ci.yml`）已有 test（内存 race）/ lint（golangci-lint）/ license-check（go-licenses 拦截 GPL/AGPL）/ build（go build）/ frontend（pnpm build）五个 job，是开源质量门的基础。但三个关键缺口：

1. **PG 集成测试未在 CI 跑**——刚迁完的 11 模块 PG 实现只有本地 `make test-pg` 保护，PR 不触发，回归风险高（最严重）。
2. **无覆盖率上报**——贡献者看不到变更对覆盖率的影响。
3. **无镜像构建/发布**——开源用户无可直接 `docker pull` 的镜像，部署门槛高。

本切片补齐这三块，使 CI 达开源生产标准。

## 范围

**做**：
- 新增 `test-pg` job（GitHub Actions postgres service container）。
- 新增 `coverage` job（go test -coverprofile + artifact 上传）。
- 新增 `release-image` job（tag `v*` 触发，多阶段 Dockerfile 构建 + push ghcr.io）。

**不做（YAGNI）**：多 OS matrix（Linux 够，控制面跑 K8s）、多 Go 版本 matrix（1.23 单版本够，避免维护负担）、自动 changelog 生成、SBOM 上传（留后续安全加固）、e2e K8s 集群测试（依赖真实集群，归 K8s 数据面切片）。

## 设计

### test-pg job（核心增量）

用 GitHub Actions 的 `services` 起 postgres 容器，注入 `PAAS_TEST_PG_URL` 跑全 11 包集成测试（`-p 1` 串行，与 Makefile test-pg 一致）：

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
        with: { go-version: "1.23" }
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

> **service 网络注意**：job 内 step 访问 service 用 `localhost:5432`（GH Actions 把 service port 映射到 job 容器的 localhost）。`health-retries: 12`（~60s）覆盖 PG 首次启动。

### coverage job

```yaml
  coverage:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.23" }
      - run: go test ./... -race -count=1 -coverprofile=coverage.out
      - uses: actions/upload-artifact@v4
        with: { name: coverage, path: coverage.out }
      - run: go tool cover -func=coverage.out | tail -1  # 打印总覆盖率到日志
```

> 不接 codecov/codecov-action（避免引入第三方服务依赖，开源纯净度优先）；artifact 下载后本地 `go tool cover -html` 查看。后续若需 badge 可再加。

### release-image job（tag 触发）

复用已有 `Dockerfile`（多阶段 distroless，非 root），tag `v*` 时构建并推送 ghcr.io：

```yaml
  release-image:
    if: startsWith(github.ref, 'refs/tags/v')
    needs: [test, test-pg, lint, license-check, build, frontend, coverage]
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

> `github.repository` = `aitoys/paas`，镜像全名 `ghcr.io/aitoys/paas-core`。tag `v1.2.3` 产 `1.2.3` / `1.2` / `1` 三 tag。`GITHUB_TOKEN` 自动提供，无需额外 secret。多平台（amd64+arm64）覆盖 Apple Silicon / ARM 服务器。

### 触发与依赖

- `on: push: branches: [main]` + `pull_request: branches: [main]`：跑全部 job（除 release-image）。
- `on: push: tags: [v*]`：触发 release-image（`if: startsWith(github.ref, 'refs/tags/v')`）。
- `release-image` needs 全部检查 job（任一失败不发布）。

### 与现有 job 的关系

保留现有 test/lint/license-check/build/frontend 五 job 不动，新增 test-pg/coverage/release-image。`build` job 的 `needs` 可加 test-pg（让 build 等 PG 测试过）。

## 验收标准

1. PR 提交 → CI 跑 `test-pg` job，postgres service 起，11 包集成测试全 PASS（与本地 `make test-pg` 一致）。
2. CI 跑 `coverage` job，产出 coverage.out artifact，日志打印总覆盖率。
3. 推 tag `v0.1.0` → `release-image` job 触发，构建多平台镜像推到 `ghcr.io/aitoys/paas-core:0.1.0` / `:0.1` / `:0.1.0`。
4. `docker pull ghcr.io/aitoys/paas-core:0.1.0` 可拉取并运行（`PAAS_DB_URL=... docker run`）。
5. 任一检查 job 失败 → release-image 不执行（不发布坏镜像）。
6. license-check 仍拦截 GPL/AGPL；新增 GH Actions 均为主流官方 action（Apache/MIT license）。

## 风险与对策

- **service 启动时序**：`health-retries: 12` 给 PG ~60s 启动窗口；若仍 flaky，加 step 显式 `pg_isready` 轮询（与 Makefile 同款）。
- **ghcr.io 首次发布权限**：仓库 Packages 设置需 public（开源）+ linked package；首次 push 后在 GH UI 确认可见性。`GITHUB_TOKEN` 对 ghcr 有 packages:write，无需 PAT。
- **多平台构建慢**：buildx QEMU 跨编译 arm64 ~5-10min，可接受；若太慢降级为 amd64-only（留后续）。
- **Docker Hub 限速**：postgres:16-alpine 拉取可能限速；GH Actions 有 Docker Hub 拉取配额，flaky 时可缓存（`docker/setup-buildx` + cache-from）。
- **test-pg 并发互清**：已用 `-p 1`（与本地一致），service 单 db 下串行必须。
