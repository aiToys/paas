# paas

[![Go](https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go)](https://go.dev/)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](./LICENSE)
[![CI](https://github.com/aitoys/paas/actions/workflows/ci.yml/badge.svg)](https://github.com/aitoys/paas/actions/workflows/ci.yml)

一站式 PaaS 平台 —— 服务治理、中间件管理、MaaS、DevOps 的基础设施统一平台。

## 状态

🚧 开源就绪起步期（完成度约 96%）。Platform Core 底座 + MaaS 推理平台 + 应用主线 + 全套平台能力横切已落地；**K8s 数据面（Workload/Dataservice CRD + Reconciler，真实集群端到端已验证）+ PostgreSQL 持久化（全 10 模块已迁）** 已上线，DevOps 构建流水线下沉到 K8s Job（DooD）。轻资产路线：不自建推理集群，聚合第三方供应商（OpenAI/DeepSeek/通义千问）。

### 已落地能力

- **Platform Core 底座**：插件契约 + Registry（拓扑排序 + 环检测）+ 依赖倒置注入；元数据 PG、事件 NATS（Core 自带，本期内存 mock）。
- **多租户身份（RBAC + 租户隔离端到端）**：API Key = `(租户, 用户, 角色)` 凭证；ctx 传播租户；Repository 强制 tenant 过滤，跨租户 not found 不泄漏；三预设角色（tenant-admin / developer / viewer）。
- **MaaS 端到端闭环 + 第三方供应商对接**：`Model → Channel → Provider` 三层抽象，聚合 OpenAI / DeepSeek / 通义千问（OpenAI 兼容协议，一个适配器覆盖三家），OpenAI 兼容流式推理（SSE）+ Token 计量 + **请求级 failover**（主通道限流/故障自动切备通道）。平台托管供应商凭证（平台级 Secret，仅管理员可写）。未配凭证时回退演示模型，开箱可用。
- **应用为主线**：应用是统一控制台主线抽象，资源以绑定形式归属；应用详情聚合工作负载 / DevOps / 配置 / 部署。
- **工作负载 + 环境**：Service/Job/CronJob 三类工作负载；环境（prod/test 物理隔离）独立一等公民，应用 × 环境多对多。
- **生产安全防护（横切）**：环境类型感知 RBAC（`prod:write`，developer 生产只读）+ 全局环境上下文 + 生产 gated 15min 超时 + 视觉强隔离 + 统一危险操作确认（`useDangerConfirm`）。后续切片自动继承。
- **DevOps CI/CD**：代码 → 构建 → 镜像（digest 不可变）→ 发布（rolling）→ 回滚，全链路 mock CI；跨应用 DevOps 中心总览。
- **应用配置**：工作负载级 env/Secret（静态、重启注入），Secret 掩码返回。
- **平台能力横切（治理四件套 + 可观测 + 安全 + 计费）**：
  - 服务治理（注册中心：服务/实例发现 + 心跳）
  - 配置中心（运行时动态配置：版本 / 发布 / 回滚 / 客户端发现）
  - 可观测（指标惰性时序 + 告警规则即时评估）
  - 安全（租户级密钥/证书 KMS + 审计日志自动记录）
  - 配额计费（租户级资源配额 + 用量 + 账单生成/支付）
- **数据服务资源（资源中心）**：DB / 缓存 / 消息队列 / 对象存储 / 向量库 / 搜索引擎，通用领域 + Kind 区分（DRY），动态表单。

完整设计见 [设计规格](./docs/superpowers/specs/)，实施计划见 [Plans](./docs/superpowers/plans/)，模块全景见 [蓝图](./docs/superpowers/specs/2026-07-27-platform-modules-blueprint.md)。

## 架构

三层 + 插件：

```
接入层   统一 API Gateway（OpenAI-compatible + 平台 REST + 鉴权/多租户路由/限流/计量）
控制面   Platform Core（最小不可分内核） + 插件槽（MaaS｜治理*｜中间件*｜DevOps*）
数据面   Inference Gateway / vLLM Pods / Provider Agent
```

- **Platform Core 最小不可分**：只含所有子系统都依赖的元能力（租户、鉴权、资源纳管、编排、可观测、插件机制）。
- **子系统是插件而非独立微服务**：以插件形式注册进 Core，共享 Core 的鉴权/存储/事件总线。
- **数据面与控制面解耦**：控制面只下发期望状态（CRD），数据面负责实际运行；控制面挂了，已部署模型继续服务。

## 技术栈

| 领域 | 选型 |
|------|------|
| 控制面 | Go 1.26 + controller-runtime + kubebuilder |
| 元数据存储 | PostgreSQL（全 10 模块已迁，可降级内存模式） |
| 事件总线 | NATS（嵌入式） |
| 推理引擎 | 第三方供应商聚合（OpenAI / DeepSeek / 通义千问，OpenAI 兼容协议；不自建 vLLM） |
| 可观测 | OpenTelemetry + Prometheus + Loki + Tempo |
| 交付 | Helm + OCI + `airsync` 离线工具 |
| 前端 | Vue 3 + Element Plus + Vite + TypeScript（console-user / console-admin / landing 三套） |
| 协议 | Apache 2.0 |

## 快速开始

环境要求：Go >= 1.26、Node >= 22.13、pnpm、GNU make（本地开发）；或仅需 Docker（容器体验）；或 K8s 集群 + helm（生产部署）。

> **本地 dev 提示**：本机存在 `~/.kube/config` 时 core 会自动连集群数据面（写操作投影 CRD）。纯内存调试请设 `PAAS_K8S_ENABLED=false`。

**Docker 一键启动**（最快体验）：

```bash
docker compose up --build         # 构建并启动 Platform Core（:8080）
curl -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/livez
```

**K8s 部署**（单镜像同域 serve 前端 + API，含数据面 CRD + Reconciler）：

```bash
# 公网路径（默认 values 用 ghcr.io/aitoys 公开镜像，开源用户首选）：
helm install paas deploy/charts/paas

# 本地 dev 集群便捷脚本（构建前端 embed 镜像 → push 集群内 registry → helm upgrade；
# 需自建 registry + 覆盖镜像地址，参考 deploy/charts/paas/values-paas-k8s.yaml）：
./scripts/deploy-k8s.sh
# 持久化：PAAS_DB_URL 指向集群内 postgres；数据面：in-cluster SA token + RBAC 自动授权
```

**本地开发**（后端 Platform Core，暴露 :8080）：

```bash
make build && ./bin/core          # 编译并运行，默认 API Key: sk-acme-admin（Acme 租户管理员）
```

**前端**（console-user 用户控制台，:5174）：

```bash
cd frontend && pnpm install && pnpm dev:user
```

打开 http://localhost:5174 即可看到：顶栏可切换 API Key（租户/角色视角）；模型市场（真实第三方供应商模型 GPT-4o / 通义千问 / DeepSeek + 演示模型）→ 点「试用」进 Playground 多轮流式推理；应用列表 → 应用详情（资源绑定/解绑）。换 Key 即换租户，应用数据按租户隔离。

> 真实供应商模型需先配置凭证：管理员在「平台能力 → 安全」填写平台级 API Key（OpenAI / DeepSeek / 通义千问）。未配置时 Playground 会回退到 `echo-demo` 演示模型并给出引导提示。

**端到端验证（core 启动后）**。API Key 绑定 (租户, 角色)：

```bash
# 模型市场富信息（含供应商 + 通道，平台级共享）
curl -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/api/models
# 流式推理（echo-demo 演示模型，开箱可用，无需配置供应商凭证）
curl -N -H "Authorization: Bearer sk-acme-dev" -H "Content-Type: application/json" \
  -d '{"model":"echo-demo","messages":[{"role":"user","content":"你好"}],"stream":true}' \
  http://localhost:8080/v1/chat/completions
# 多租户隔离：不同租户 Key 看到不同应用
curl -H "Authorization: Bearer sk-acme-admin"   http://localhost:8080/api/applications   # Acme
curl -H "Authorization: Bearer sk-globex-admin" http://localhost:8080/api/applications   # Globex
# 工作负载（应用运行形态）：跨应用按类型查询 + 扩缩容
curl -H "Authorization: Bearer sk-acme-admin" "http://localhost:8080/api/workloads?type=service"
curl -X PUT -H "Authorization: Bearer sk-acme-admin" -H "Content-Type: application/json" \
  -d '{"replicas":6,"status":"running"}' http://localhost:8080/api/workloads/wl-rec-svc
# 环境（物理隔离单元 prod|test）+ 按环境过滤工作负载
curl -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/api/environments
curl -H "Authorization: Bearer sk-acme-admin" "http://localhost:8080/api/workloads?envId=env-acme-test&type=service"
# DevOps：代码->构建->镜像->发布->回滚
curl -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/api/applications/app-cs/images
curl -X POST -H "Authorization: Bearer sk-acme-admin" -H "Content-Type: application/json" \
  -d '{"repoId":"repo-acme-cs"}' http://localhost:8080/api/applications/app-cs/buildruns   # 触发构建（mock CI）
# 发布到测试（dev 可）；发布到生产需 admin（dev 403，生产权限守卫）
curl -X POST -H "Authorization: Bearer sk-acme-dev" -H "Content-Type: application/json" \
  -d '{"envId":"env-acme-test","imageId":"img-acme-001"}' http://localhost:8080/api/applications/app-cs/releases
```

测试与检查：`make test`（含 race）、`make lint`（golangci-lint）。

## 贡献

见 [CONTRIBUTING.md](./CONTRIBUTING.md)。提交信息遵循 [Conventional Commits](https://www.conventionalcommits.org/)。

## 协议

[Apache License 2.0](./LICENSE) © 2026 The PaaS Authors
