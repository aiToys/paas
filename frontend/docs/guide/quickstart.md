# 快速开始

先选择本地体验或集群部署，再进入控制台。后续部署、监控和模型调用各有依赖，不会仅因控制台能打开就自动启用。

## 1. 启动平台

克隆仓库并进入根目录：

```bash
git clone https://github.com/aitoys/paas.git
cd paas
```

### Docker 本地体验

需要 Docker 与 Compose；首次构建还需能下载依赖与基础镜像。

```bash
docker compose up --build
```

另一终端检查存活：

```bash
curl http://localhost:8080/livez
```

打开 `http://localhost:8080/console/`（经典版）或 `http://localhost:8080/next/`（新版试验版）。Compose 提供 PostgreSQL 持久化；默认不接 K8s 数据面、真实构建与观测后端。

### 源码开发

需要 Go ≥ 1.26、Node.js ≥ 22.13、pnpm、GNU make。

```bash
make build
PAAS_K8S_ENABLED=false ./bin/core
```

另一终端启动新版前端：

```bash
cd frontend
pnpm install
pnpm --dir console-user-v2 dev
```

访问 `http://localhost:5176/next/`；经典版可运行 `pnpm dev:user`，访问 `http://localhost:5174/console/`。上述后端未配置数据库，使用内存模式。若移除 `PAAS_K8S_ENABLED=false`，Core 会尝试使用本机 kubeconfig，写操作可能落到真实集群。

### 集群部署

见 [K8s 安装](../deploy/k8s)。需准备镜像拉取、存储、CRD/RBAC、访问入口及生产安全配置。

## 2. 登录与创建产品

只有启用演示 seed 的环境才预置以下账号，密码均为 `123456`：

| 账号 | 租户 | 角色 |
|---|---|---|
| `acme-admin` | Acme | 租户管理员 |
| `acme-dev` | Acme | 开发者 |
| `globex-admin` | Globex | 租户管理员 |

生产关闭演示 seed 后，使用平台管理员开通的租户账号；API Key 用于程序调用，不替代网页登录密码。详见[登录与 API Key](./auth)。

新版进入「产品」→ 新建产品；经典版进入「应用」→ 新建应用。两者对应同一个 Application。新产品中添加可部署的服务，再配置目标环境、镜像和资源规格。

## 3. 部署并验证运行

本步需要 K8s 数据面。没有集群时，可先熟悉产品与服务配置，不将保存记录当作 Pod 已部署。

1. 在产品的服务运行视图选择服务，发起交付并检查实际目标环境；生产发布必须声明资源规格。
2. 若从代码构建，先按[交付指南](../features/devops)配置真实 builder 与镜像库，再预演与触发流水线。
3. 在工作负载详情确认实例就绪、镜像和实际环境。流水线成功、Pod 就绪、外部入口可达是不同检查项。

已有直接创建的工作负载，也可从新版「工作负载」进入运行详情；经典版在应用的工作负载/部署视图查看。

## 4. 查看日志、指标和链路

新版「工作负载」→ 运行详情可查看实例、容器日志与事件，见[运行排障](../features/runtime)。

「可观测」页查询 Prometheus、Loki、Jaeger。需要管理员先配置相应后端，业务也需接入指标与 trace 采集；查询为空应先检查采集状态，不能直接判断服务无流量。

## 5. 调用一个模型

管理员先配置模型、通道端点与凭证。在模型目录确认可用模型 ID，然后进入 Playground，或使用有 `model:infer` 权限的 Key：

```bash
curl -N -H "Authorization: Bearer <你的 API Key>" -H "Content-Type: application/json" \
  -d '{"model":"<可用模型 ID>","messages":[{"role":"user","content":"你好"}],"stream":true}' \
  http://<平台地址>/v1/chat/completions
```

要按应用归因用量，需创建绑定该应用的 API Key；租户级 Key 不会自动得到应用归因。未配置通道或凭证时按接口错误排查，见[MaaS](../features/maas)。
