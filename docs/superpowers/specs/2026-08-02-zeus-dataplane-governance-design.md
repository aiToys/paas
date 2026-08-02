# zeus 数据面纳管 + PaaS 服务治理真实化设计（P0+P1）

> 状态：设计已对齐（AskUserQuestion 拍板范围 = P0+P1；paas registry 插件进 PaaS 仓）。
> 后续：writing-plans 拆 TDD 实施计划。

## 目标

把 PaaS 服务治理从「控制面 CRUD + 进程内 mock」升级为「K8s 原生服务发现真源 + 数据面 SDK 真实接入」，让 zeus（或任意支持 HTTP 发现的数据面 SDK）应用能真实注册、发现、被健康检查驱动。一刀拿掉平台最大那个 🔴（服务治理数据面 mock）。

**范围**：P0（工作负载建 K8s Service，阻塞前置）+ P1（/dp/ 数据面 API + paas-registry 插件 + 真实健康检查）。P2（可观测应用级埋点 + 配置 watch）与 P3（examples 电商 demo）后续切片，本 spec 不展开。

## 背景与现状（探索结论）

### PaaS 侧 gap
- `internal/controller/workload_controller.go:89 applyDeployment` 只建 `appsv1.Deployment`，**不建 `corev1.Service`**；`WorkloadSpec`（`api/core/v1alpha1/workload_types.go:18`）**无 Port 字段**。多微服务无法 DNS 互调。
- `internal/governance/`：Instance 注册即 healthy（`model.go:14` 注释），`Heartbeat` 仅刷时间戳（`repository.go:28`），**无真实健康检查、无过期剔除、无数据面 SDK 接入 API**。CircuitBreaker 用 FNV 哈希伪造窗口（`model.go:249`）。
- 唯一对外接口是控制台 REST `/api/services`（`handler.go`），用人类 API Key 鉴权，**非面向数据面 SDK**。
- **Core 自带 NATS 是愿景未落地**（go.mod 无 nats 依赖）。

### zeus 侧（接入点）
- registry 三接口（`registry/registry.go:9`）：`Registrar{Register/Deregister}` + `Watcher{Watch}` + `Discovery{GetService}`。
- 自带插件**仅 etcd / nacos**（`plugins/registry/`）；无 NATS / K8s / HTTP 插件。
- `examples/20-full-demo/internal/gwdisc/discovery.go` 是 HTTP 发现范本：2s 轮询 `/api/services` + 签名对比 + fan-out。
- `types.Instance`（`types/instance.go:9`）字段对齐 K8s Endpoints。
- OTel 选型，但 `WithOTLPEndpoint` 是占位符，需手写 otlptracehttp。
- module path：`github.com/go-zeus/zeus`（本地 private，外部依赖需 replace）。

## 架构（方案 B 变体）

**核心思想**：Instance 真源 = K8s Endpoints（readiness probe 驱动）。PaaS 经 `/dp/` HTTP API 把 Endpoints 暴露给数据面 SDK；`paas://` registry 插件适配为 zeus Discovery。不引入 etcd/nacos（PaaS 没有，引入违背「Core 不依赖外部治理」原则）。

```
zeus 应用 Pod（带 readiness probe）
  └─ import _ "github.com/aitoys/paas/sdk/paas-registry"
     └─ ZEUS_REGISTRY=paas://paas-core.paas.svc/dp?token=<dp-token>
        ├─ Register/Deregister → POST/DELETE /dp/register（声明服务元信息）
        └─ GetService/Watch   → GET /dp/instances?service=<name>（2s 轮询）
                                  └─ PaaS 读 K8s Endpoints ready address
                                      → 转 zeus Instance 结构返回

控制台 /api/services            ← governance 表（服务元信息，控制面声明）
控制台 /api/services/{id}/instances ← /dp/instances 同源（K8s Endpoints real）
```

**为什么不复用 etcd/nacos**：PaaS 无此类元设施（NATS 是假愿景）；引入等于先建外部治理中间件，违背架构约束。

**为什么 Instance 真源切到 K8s Endpoints**：readiness probe 已是 K8s 原生健康检查，免维护心跳/剔除 goroutine；Endpoints 自动随 Pod 扩缩/故障更新；与 PaaS 工作负载（Deployment）天然同源。

## P0：工作负载建 K8s Service

### 改动

**CRD 类型**（`api/core/v1alpha1/workload_types.go`）：`WorkloadSpec` 加
```go
Port         int32 `json:"port,omitempty"`         // Service 端口（对外暴露）；service 类型且 >0 才建 Service
ContainerPort int32 `json:"containerPort,omitempty"` // Pod 监听端口；空则 = Port
```
+ `+kubebuilder:validation:Minimum=1` 等注解（`make manifests` 重生成 CRD）。

**领域模型**（`internal/workload/model.go`）：`Workload` 加 `Port int` + `ContainerPort int`（json 同名）；`Validate` 不强制（Port=0 时不建 Service，Job/CronJob 无需）。

**Reconciler**（`internal/controller/workload_controller.go`）：
- 新增 `applyService(ctx, w)`：type=service 且 `Port>0` 时 `CreateOrUpdate` `corev1.Service`：
  - 名 = `w.Name`（与 Deployment 同名，K8s 允许）。
  - `Spec.Selector` = `labelsFor(w)`（匹配 Pod）。
  - `Spec.Ports` = 单端口 `ServicePort{Port: w.Spec.Port, TargetPort: containerPort, Protocol: TCP}`。
  - `SetControllerReference(w, svc)`（删 CR 级联清 Service）。
- `applyDeployment` 末尾调 `applyService`（仅 service 类型）。
- `SetupWithManager` 加 `Owns(&corev1.Service{})`。
- `podSpec`：`ContainerPort>0` 时给 container 加 `Ports: []ContainerPort{{ContainerPort: w.Spec.ContainerPort}}`。

**Handler/Store 透传**：`internal/workload/handler.go` Create/Update 接收 Port 字段；`memory/store.go` + `pg/store.go` seed 给演示工作负载补 Port（如 service 类型补 80/8080）；PG 迁移加 `port`/`container_port` 列（新 migration 0015）。

**RBAC**：确认 core SA 已有 `services` 权限（CLAUDE.md rbac.yaml 列了 services）；Endpoints 读权限 P1 补。

### 验证
- fake client 测：建 service 类型 Workload（Port=8080）→ 断言 `corev1.Service` 被创建、Selector/Port 正确、OwnerRef 指向 CR；Port=0 时不建；Job/CronJob 不建。
- 集群 e2e：API 建 Workload → `<name>.<ns>.svc.cluster.local:8080` 可 DNS 解析。

## P1：数据面闭环

### 1. `/dp/` 数据面 API（新建 `internal/dataplane/`）

独立于控制台 `/api/`（人类 API Key），用**数据面 token** 鉴权（复用 API Key 机制简化：生成专用 dp token，绑 tenant+app+workload）。

**模块结构**：
- `internal/dataplane/handler.go`：HTTP handler，挂 `/dp/` 前缀。
- `internal/dataplane/endpoints.go`：K8s Endpoints reader（clientset 读 Endpoints，过滤 ready address，转 zeus Instance 结构）。
- `internal/dataplane/auth.go`：dp token 校验中间件（复用 identity.LookupAPIKey，dp token 是专用前缀的 API Key）。

**端点**（HTTP/JSON，KISS 先不做 gRPC）：
| 方法路径 | 用途 | 数据源 |
|---|---|---|
| `GET /dp/services` | zeus Discovery 列服务 | governance 表（控制面声明的服务元信息） |
| `GET /dp/instances?service=<name>` | zeus 发现实例（**真源**） | K8s Endpoints ready address（clientset 实时读） |
| `POST /dp/register` | zeus 启动声明服务元信息 | 写 governance Service/Route 元信息（幂等） |
| `DELETE /dp/register?id=` | zeus 退出反注册 | 删 governance 元信息（不删 Endpoints，K8s 自管） |
| `PUT /dp/heartbeat` | 元数据刷新（可选） | 刷 governance Instance.UpdatedAt |

**响应契约**（对齐 zeus `types.Instance`）：
```jsonc
// GET /dp/instances?service=user-svc
{
  "service": "user-svc",
  "instances": [
    {"id":"user-<pod>","name":"user-svc","cluster":"default","protocol":"http","ip":"10.1.2.3","port":8080,"metadata":{}}
  ],
  "signature": "<hash>"  // 供 zeus Watcher 对比变化
}
```

**Endpoints reader 关键逻辑**：
- Service 名 → Workload 名（约定：governance Service.AppID/Name 映射到 K8s Service 名）。
- `clientset.CoreV1().Endpoints(ns).Get/List` → 遍历 `Subsets[].Addresses`（ready，排除 NotReadyAddresses）→ 每个地址 + 每个 port 生成一个 Instance。
- cluster 字段：本期统一 `default`（LaneID 路由 P3 演进）。

### 2. `sdk/paas-registry/`（PaaS 仓内**独立 module**）

独立 `go.mod`（**不进 PaaS 主 module**，避免主仓引入 zeus 依赖——Core 不依赖数据面 SDK，符合架构约束）。

**文件**：
- `sdk/paas-registry/go.mod`：`module github.com/aitoys/paas/sdk/paas-registry`，依赖 `github.com/go-zeus/zeus`，`replace github.com/go-zeus/zeus => /Users/wangtao/data/github.com/go-zeus/zeus`（本地开发）。
- `sdk/paas-registry/resolver.go`：`init()` 调 `app.RegisterRegistryResolver("paas", newPaasRegistry)`，解析 `paas://<host>/dp?token=<>&service=<>` URL。
- `sdk/paas-registry/registry.go`：实现 `registry.Registrar + Discovery + Watcher`：
  - `Register/Deregister`：POST/DELETE `/dp/register`。
  - `GetService`：GET `/dp/instances?service=`，解析 JSON → `*types.ServiceEntry`。
  - `Watch`：启 goroutine 2s 轮询 `GetService`，签名变化时发信号到 channel（仿 `gwdisc/discovery.go`）。
- `sdk/paas-registry/registry_test.go`：httptest mock `/dp/`，测 Register/GetService/Watch 签名对比。

**zeus 应用侧**（P3 examples 用）：`import _ "github.com/aitoys/paas/sdk/paas-registry"` + `ZEUS_REGISTRY=paas://paas-core.paas.svc/dp?token=<dp-token>&service=<svc>`。

### 3. 真实健康检查（readiness probe 驱动）

- `podSpec`：`ContainerPort>0` 时加 readiness probe（`HTTPGet /healthz` 或 `TCPSocket ContainerPort`——选 TCP，对应用零侵入，应用只要 listen 即 ready）。
- K8s 自动维护 Endpoints ready 集合 → `/dp/instances` 只返 ready → zeus Discovery 天然健康。
- governance `Heartbeat` 方法保留但不再是存活真源（降级为元数据刷新，注释标注 deprecated）。

### 4. 治理控制台增强

- `GET /api/services/{id}/instances`：改为从 K8s Endpoints 读（real），不再读 mock Instance 表。内存/PG 路径下 Instance 表降级为「手动补充实例」（兼容控制台手动注册演示，不破坏现状）。
- 新增「服务发现真源」标识（前端展示实例来自 K8s）。

### 5. dp token 机制

- 工作负载 controller 给 Pod 注入 env `PAAS_DP_TOKEN` + `PAAS_DP_ENDPOINT=http://paas-core.paas.svc/dp`。
- token 生成：core 启动期/工作负载创建期，调 identity 生成专用 API Key（绑 tenant+app，role=`dataplane`，仅 `governance:read` + dataplane 端点权限）。
- `/dp/` 中间件验 token → 注入 tenant ctx（复用 tenant.WithTenant）。
- **KISS**：token 经 env 注入 Pod（明文在 etcd Pod spec，与 core 自身 env 同级敏感度）。生产化后续演进 K8s ServiceAccount Token + SPIFFE。

## 文件清单

### PaaS 主仓
| 文件 | 动作 |
|---|---|
| `api/core/v1alpha1/workload_types.go` | 改：+Port/ContainerPort 字段 + kubebuilder 注解 |
| `internal/controller/workload_controller.go` | 改：applyService + Owns(Service) + podSpec ContainerPort + readiness probe |
| `internal/controller/workload_controller_test.go` | 改：补 Service 创建断言 |
| `internal/workload/model.go` | 改：+Port/ContainerPort |
| `internal/workload/handler.go` | 改：Create/Update 透传 Port |
| `internal/workload/memory/store.go` + `pg/store.go` | 改：seed Port + PG 列 |
| `internal/storage/pg/migrations/0015_*.up/down.sql` | 新建：workloads 加 port/container_port |
| `internal/dataplane/{handler,endpoints,auth}.go` | 新建：/dp/ API |
| `internal/dataplane/{handler,endpoints}_test.go` | 新建：测 |
| `cmd/core/main.go` | 改：挂 `/dp/` 路由（独立鉴权，不经 auth gateway） |
| `cmd/core/manager.go` | 改：k8sAppliers 暴露 clientset（dataplane 读 Endpoints 用） |
| `deploy/charts/paas/templates/rbac.yaml` | 改：补 endpoints get/list |

### 独立 module
| 文件 | 动作 |
|---|---|
| `sdk/paas-registry/go.mod` + `go.sum` | 新建：独立 module，replace 本地 zeus |
| `sdk/paas-registry/{resolver,registry}.go` | 新建：paas:// 插件 |
| `sdk/paas-registry/registry_test.go` | 新建：httptest 测 |

## 约束与风险

- **zeus 本地 private module**：`sdk/paas-registry` go.mod `replace github.com/go-zeus/zeus => 本地路径`。**开源发布前需 zeus 发布为 public module，或 vendor 进 PaaS 仓**（spec 标注，plan 落 TODO）。
- **不破坏现状**：`PAAS_K8S_NAMESPACE` 空（非集群部署）时，`/dp/` 降级返空 + 日志（与 observability real 模式同构），内存/PG 路径完全不受影响。
- **多租户隔离**：`/dp/` 端点强制按 token 绑定的 tenant 过滤——只返该租户 Endpoints。Endpoints 名约定带租户前缀或经 governance Service 表过滤（service→tenant 映射）。
- **KISS/YAGNI**：`/dp/` 先 HTTP/JSON 不 gRPC；服务发现 2s 轮询不做 long-poll（与 gwdisc 一致）；熔断器 stats 采集不接（保持确定性评估，归 P2）；dp token 复用 API Key 不上 SAT；cluster 统一 default（泳道路由归 P3）。
- **Instance 表去留**：降级保留（兼容控制台手动注册 + 内存路径演示），不破坏现有测试。生产真源是 K8s Endpoints。
- **依赖 license**：zeus（Apache 2.0，本地）；无新增主仓依赖（clientset 已在依赖树供 builder.K8sJob 用）。

## 不做（YAGNI，归后续）

- gRPC 数据面协议（HTTP/JSON 先行）。
- 服务发现 long-poll / watch 推送（2s 轮询够用）。
- 熔断器真实 stats 采集（P2）。
- 配置中心 watch 长连接（P2）。
- 应用级 metrics/traces 埋点（P2）。
- 泳道路由 / cluster 多维（P3）。
- dp token 演进 SAT/SPIFFE（生产化）。
- examples 电商 demo（P3，依赖 P0+P1 跑通）。
