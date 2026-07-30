# airsync 离线交付工具设计

**日期**：2026-07-30
**状态**：待评审
**关联**：`2026-07-26-maas-platform-foundation-design.md`（SaaS + 私有化双模交付，控制面必须可打包为离线交付件）、CI 镜像发布 spec（ghcr.io 镜像源）

## 背景与动机

CLAUDE.md 明确：**SaaS + 私有化双模交付**，控制面必须可打包为**离线交付件**。私有化客户环境通常**无外网**，无法 `docker pull` 公网镜像或 `helm install` 拉 chart。需要把全部依赖（镜像 + Helm chart + 迁移 SQL + 前端 dist）打进一个自包含交付包，离线部署。

`airsync` 是规划中的离线工具（CLAUDE.md「后续子模块: deploy/ airsync」）。本切片落地 airsync CLI + Helm chart，使平台具备私有化离线交付能力——开源 PaaS 的关键差异化（多数开源项目只支持在线 install）。

## 范围

**做**：
- `airsync` CLI（`cmd/airsync/`，Go）：`bundle`（公网打包）/ `install`（私有部署）/ `verify`（完整性校验）。
- Helm chart（`deploy/charts/paas/`）：core deployment + postgres + service + ingress + configmap，values 参数化。
- 离线镜像包（OCI tar）：core + postgres:16 + 前端 nginx，digest 校验。

**不做（YAGNI）**：
- 在线自动升级 / 版本漂移检测（归后续运维切片）。
- 多架构镜像同步（先 amd64，arm64 留后续）。
- 配置向导 GUI（CLI 够，Web 控制台归后续）。
- 镜像漏洞扫描集成（trivy 等归 CI 安全加固）。
- 数据迁移/备份恢复（仅含 schema 迁移，业务数据归运维）。

## 设计

### airsync CLI（`cmd/airsync/`）

```go
// cmd/airsync/main.go
// airsync bundle --version v0.1.0 [--registry ghcr.io/aitoys]
//   拉全部镜像（core / postgres:16 / frontend）→ docker save 成 OCI tar
//   + 拉 Helm chart（.tgz）+ embed migrations/*.sql + frontend dist
//   → 打包 paas-bundle-v0.1.0.tar.gz（含 manifest.json 清单 + sha256）
//
// airsync install --bundle paas-bundle-v0.1.0.tar.gz \
//   --target-registry registry.private.com \
//   --namespace paas --set paas.db.url=...
//   docker load 镜像 → retag → push 到私有 registry
//   helm template chart（image 指向私有 registry）→ kubectl apply
//   跑 migrations（core 启动自动 up）
//
// airsync verify --bundle paas-bundle-v0.1.0.tar.gz
//   校验 manifest.json 的 sha256 与实际文件一致（防传输损坏）
```

子命令三件套，纯 Go + 调 `docker`/`helm`/`kubectl` CLI（sh exec，不引重型 client 库，KISS）。

### Helm chart（`deploy/charts/paas/`）

```
deploy/charts/paas/
  Chart.yaml          # name: paas, version, appVersion
  values.yaml         # image.registry/tag, db.enabled/url, ingress.host, env
  templates/
    core-deployment.yaml    # core 容器（含 PAAS_DB_URL env + migrations 启动跑）
    core-service.yaml
    postgres.yaml           # postgres:16 StatefulSet（db.enabled=true 时，否则外置）
    ingress.yaml
    configmap.yaml          # 迁移 SQL / 前端 dist 挂载（或镜像内置）
    _helpers.tpl
```

values 参数化（开源用户改 `values.yaml` 定制）：`image.registry`（离线指向私有）、`db.url`（外置 PG 或内置）、`ingress.host`、`env.PAAS_API_KEY` 等。

### 离线镜像包

`bundle` 产 `paas-bundle-v0.1.0.tar.gz`：
```
manifest.json          # 镜像列表 + digest + chart 版本 + paas 版本
images/
  core-amd64.tar       # docker save 的 OCI 镜像
  postgres-16-alpine-amd64.tar
  frontend-amd64.tar
chart/
  paas-0.1.0.tgz       # helm package 产物
migrations/            # embed 进 core 二进制（已 //go:embed），bundle 内冗余备份
```

`install` 时 `docker load` 全部 → retag 到 `--target-registry` → push → chart `image.registry` 设为私有 registry → `helm install`。

### 公网/私有两路径

- **公网（在线）**：`helm install paas ghcr.io/aitoys/charts/paas`（chart 作 OCI artifact 推 ghcr.io，CI 发布）。
- **私有（离线）**：`airsync bundle` 打包 → 物理介质/U 盘传到客户 → `airsync install` 部署。两条路径共用同一 chart，仅 image.registry 不同。

## 验收标准

1. `airsync bundle --version v0.1.0` 产 `paas-bundle-v0.1.0.tar.gz`，含全部镜像 + chart + manifest.json（sha256 全对）。
2. `airsync verify` 校验 bundle 完整性（篡改任一文件 → 失败）。
3. `airsync install --bundle ... --target-registry localhost:5000` 在**离线** kind 集群部署成功（断公网验证）：
   - 镜像推到私有 registry
   - helm install 起 core deployment + postgres
   - core 启动跑迁移 + seed
   - `curl` /livez 通
4. `helm install paas ./deploy/charts/paas`（公网路径）同样可用。
5. values 参数化验证：改 `db.url` 指向外置 PG → core 连外置 PG。
6. license：airsync 自研 Apache 2.0；Helm Apache 2.0；镜像（postgres/distroleless）兼容。

## 风险与对策

- **镜像体积**：多镜像 tar 可能数百 MB~GB。对策：`docker save --compress` + tar.gz；分层复用（base layer 共享）；按需打包（不打包 GPU device-plugin 镜像除非启用）。
- **私有 registry 认证**：`install` 需 login。对策：`--registry-auth` 参数（user:pass 或 dockerconfigjson），生成 imagePullSecret。
- **chart 版本与镜像 tag 对齐**：bundle 里 chart appVersion 必须匹配镜像 tag。对策：`airsync bundle` 从 chart 读取 appVersion 决定拉哪个 tag，manifest 记录对齐关系，`verify` 校验。
- **K8s 版本兼容**：chart 用稳定 API（apps/v1、batch/v1、networking.k8s.io/v1），覆盖 K8s 1.24+。
- **docker/helm/kubectl 依赖**：airsync 调外部 CLI。对策：`airsync doctor` 检查依赖存在性 + 版本，缺失给安装提示。
