# 数据服务真实化设计（部署→连接→监控）

> 状态：设计稿（2026-08-01）。把数据服务从「占位 StatefulSet」升级为「真实可连引擎 + 应用自动注入连接信息 + Pod 级监控告警」。

## 背景与问题

当前 `internal/controller/dataservice_controller.go` 只落了一个**裸 StatefulSet**，离「真实可用」差 5 个关键环节：

| 缺口 | 后果 |
|------|------|
| 无 ClusterIP Service（引用了不存在的 headless 名） | 应用/Pod 连不上（无 DNS） |
| spec 字段未透传容器 env | **mysql 无 `MYSQL_ROOT_PASSWORD` 直接启动失败**；redis 无密码；minio 无 access key |
| 无凭证 Secret 生成 | 没有密码可用 |
| 无连接信息回写 | 前端/应用拿不到 host:port+凭证 |
| 应用绑定只记一条 Binding 记录 | 绑定后应用拿不到连接串 |

可观测侧 `targetType` 只有 `app|workload|env`，数据服务无监控维度。

## 目标

聚焦 4 类引擎做扎实（**db→mysql / cache→redis / mq→nats / storage→minio**），端到端真实可用；vector/search 保持现状占位（YAGNI）。具体：

1. **部署真实**：spec 字段透传容器 env + 自动生成强随机凭证 Secret + 暴露 headless/ClusterIP Service + 回写 status。
2. **连接可用**：数据服务详情返回 host:port+凭证（明文，read 权限者可见）；应用绑定数据服务后**自动向 appconfig 注入连接条目**，工作负载重启即作为 env 注入，真正可连。
3. **监控可视化**：observability 加 `targetType=dataservice`，配 Prometheus 时取真实 Pod CPU/内存，未配走 memory 惰性兜底；前端数据服务详情页加监控卡 + 告警规则。

## 已决策（AskUserQuestion 确认）

- **引擎范围**：聚焦 4 类（mysql/redis/nats/minio）做扎实，vector/search 占位。
- **凭证暴露**：强随机密码存 K8s Secret + domain 明文；list 掩码 password，详情明文（dataservice:read 权限者）。
- **绑定可用性**：自动注入 appconfig（绑定→写连接条目→工作负载重启注入 env→真实可连；解绑自动清）。
- **监控来源**：Pod 级真实（Prometheus+cAdvisor）+ memory 惰性兜底，与现有 observability 架构一致。
- **host 形式**：FQDN `<name>.<ns>.svc.cluster.local`（任意 namespace 场景可连，最稳）。
- **注入 key**：固定名（`DATABASE_URL`/`REDIS_URL`/`NATS_URL`/`MINIO_ENDPOINT`+`MINIO_ACCESS_KEY`+`MINIO_SECRET_KEY`），应用代码引用方便；同应用同 Kind 多绑定覆盖取最后（起步约定，文档注明）。
- **监控 real 依赖**：不强制。配 `PAAS_PROM_URL` 真实查，未配走 memory 兜底（现状架构不变）。

## 架构

```
POST /api/dataservices {kind:db, engine:mysql}
  → domain.Create 生成凭证（控制面纯函数）+ BuildConnection 算 host:port+uri
  → ApplyRepo 投影 DataService CRD（spec.credentials + spec.connection）
  → DataServiceReconciler:
      ├─ CreateOrUpdate Secret（MYSQL_ROOT_PASSWORD 等，from spec.credentials，幂等不覆盖已有）
      ├─ CreateOrUpdate headless Service（STS 必需）+ ClusterIP Service（应用访问）
      ├─ CreateOrUpdate StatefulSet（env valueFrom secretKeyRef + 启动参数注入密码）
      └─ 回写 status.phase/ready/image（host/connection 控制面已算，无需回流）

POST /api/applications/{id}/bindings {type:dataservice, name:dsId}
  → Application 记 Binding
  → BindingInjector 钩子（依赖倒置，cmd/core 桥接）：
      查 DataService → 算连接条目 → appconfig Upsert（TypeSecret）
        db→DATABASE_URL / cache→REDIS_URL / mq→NATS_URL / storage→MINIO_ENDPOINT+ACCESS_KEY+SECRET_KEY
  → 工作负载改后重启 → env 注入 → 真实可连；解绑自动清条目

GET /api/observability/metrics?targetType=dataservice&targetId=dsId
  → 配 PAAS_PROM_URL：按 paas_aitoys_dataservice 标签查 cadvisor Pod CPU/内存（真实）
  → 未配：memory 惰性补点（兜底，前端可视化闭环）
```

核心不变量：**Connection 全程控制面生成**（dev 纯内存/PG/K8s 三模式行为一致），reconciler 只落地不回流；**依赖倒置**（application 不 import dataservice/appconfig；dataservice 不依赖 K8s）。

---

## A. 数据面增强

### A.1 连接信息生成（纯函数，控制面）

**新增 `internal/dataservice/connection.go`**：

- `GenerateCredentials(kind, engine string) map[string]string`：用 `crypto/rand` 生成强随机（24 字符，base62）。按 Kind：
  - db：`user=root`、`password=<rand>`、`database=<spec.db_name 或 "appdb">`
  - cache：`password=<rand>`
  - mq：`token=<rand>`
  - storage：`accessKey="minio"`、`secretKey=<rand>`
- `EnginePort(kind string) int32`：mysql 3306 / redis 6379 / nats 4222 / minio 9000（S3 API；console 9001 不暴露给应用，仅 Pod 内）。
- `BuildConnection(d DataService, namespace string) map[string]string`：
  - `host = <d.Name>.<namespace>.svc.cluster.local`
  - `port = EnginePort(d.Kind)`
  - 合并 credentials（user/password/database 或 accessKey/secretKey 或 token）
  - `uri`：按 Kind 拼标准连接串
    - db：`mysql://<user>:<password>@<host>:<port>/<database>`
    - cache：`redis://:<password>@<host>:<port>`
    - mq：`nats://<token>@<host>:<port>`（NATS auth token 形式；用户名匿名）
    - storage：不拼单 uri，拆 `endpoint=http://<host>:<port>` + accessKey + secretKey
- `MaskConnection(conn map[string]string) map[string]string`：password/secretKey/token → `••••••`（list 用）。
- 纯函数，无 K8s 依赖，可单测。

### A.2 领域模型

**`internal/dataservice/model.go`**：

- `DataService` 加 `Connection map[string]string json:"connection,omitempty"`。
- `KindMetas` MQ 的 engine 选项加 `nats` 并设为默认（保留 kafka/rabbitmq/rocketmq 选项；默认改 nats）。
- 新增 `NamespaceResolver` 接口（`Namespace() string`），由 cmd/core 注入 `PAAS_K8S_NAMESPACE`（默认 `"paas"`）；Create 时调 `BuildConnection(d, ns)` 填充 `d.Connection`（含 GenerateCredentials）。
- `Repository.Create` 实现负责：若 `d.Connection` 为空（未注入 resolver 的测试场景），用默认 ns `"paas"` 兜底填充（保证三模式一致 + 测试可控）。

### A.3 CRD

**`api/core/v1alpha1/dataservice_types.go`**：`DataServiceSpec` 加：
```go
Credentials map[string]string `json:"credentials,omitempty"` // 控制面生成（user/password/...），reconciler 读建 Secret
Connection  map[string]string `json:"connection,omitempty"`  // 控制面算（host/port/uri），可观测/调试
```
`make manifests` 重新生成 CRD YAML + deepcopy（`+kubebuilder:object:generate=true` 已在，map 字段 deepcopy 自动）。

### A.4 Reconciler 增强

**`internal/controller/dataservice_controller.go`**：

- `engineImage`：mq case 加 `nats → "nats:2-alpine"`；vector/search 保持现状。
- 新增 `enginePort(kind)`（同 A.1 EnginePort，避免跨包循环，controller 内重复一份常量或导出 A.1 的）——采用导出 `dataservice.EnginePort` 复用（DRY）。
- 新增 `containerSpec(d) (corev1.Container, []corev1.EnvFromSource)`：按 Kind 构造容器：
  - db/mysql：env `MYSQL_ROOT_PASSWORD`（secretKeyRef `<name>-secret/key=password`）、`MYSQL_DATABASE`（secretKeyRef key=database 或 value=spec.db_name）；image `mysql:8`。
  - cache/redis：command `["redis-server","--requirepass","$(REDIS_PASSWORD)"]`；env `REDIS_PASSWORD`（secretKeyRef key=password）；image `redis:7-alpine`。
  - mq/nats：args `["-auth","$(NATS_TOKEN)"]`；env `NATS_TOKEN`（secretKeyRef key=token）；image `nats:2-alpine`；port 4222。
  - storage/minio：command `["server","/data","--console-address",":9001"]`；env `MINIO_ROOT_USER`（secretKeyRef key=accessKey）、`MINIO_ROOT_PASSWORD`（secretKeyRef key=secretKey）；image `minio/minio:latest`；port 9000。
- mutate 内（CreateOrUpdate 闭包）：
  1. Secret `<d.Name>-secret`（stringData = d.Spec.Credentials；OwnerRef=d；**已存在则不覆盖**——controllerutil.CreateOrUpdate 天然幂等，但 mutate 内仅在 `secret.CreationTimestamp.IsZero()` 时写 stringData，避免覆盖已改密码）。
  2. headless Service `<d.Name>-headless`（ClusterIP None，port=enginePort，OwnerRef=d）。
  3. ClusterIP Service `<d.Name>`（port=enginePort，selector=labels，OwnerRef=d）。
  4. StatefulSet（replicas=1，ServiceName=`<d.Name>-headless`，container=containerSpec，OwnerRef=d）。
- 失败态：apply 出错 best-effort 回写 `phase=failed` + 返回 error 触发重试（与 workload_controller 同款）。
- `SetupWithManager`：加 `Owns(&corev1.Secret{})` + `Owns(&corev1.Service{})`（删 CR 级联清 Secret/Service/STS）。

---

## B. 连接信息返回 + 应用自动注入

### B.1 数据服务 API

**`internal/dataservice/handler.go`**：

- GET `/api/dataservices/{id}`（详情）：返回 domain，含 `Connection` 明文（dataservice:read 权限者可见，已是 handler 既有 allow 门控）。
- GET `/api/dataservices`（list）：每条 `Connection` 经 `MaskConnection` 掩码（password/secretKey/token → `••••••`），不泄漏明文。
- 不动权限模型（已有 dataservice:read/write + prod:write）。

### B.2 应用绑定注入（依赖倒置）

**`internal/core/application/handler.go` + `repository.go`**：

- 新增接口（application 包内定义，cmd/core 实现）：
  ```go
  type BindingInjector interface {
      OnBind(ctx context.Context, appID, envID, bindingType, bindingName string) error
      OnUnbind(ctx context.Context, appID, bindingType, bindingName string) error
  }
  ```
- Handler 加 `binder BindingInjector` 字段 + `WithBindingInjector` opt。
- POST `/api/applications/{id}/bindings` 成功记 Binding 后：若 `bindingType=="dataservice"` 且 binder 注入，调 `OnBind`。**注入失败仅 log + 不阻断绑定**（绑定是主操作，注入是增强；返回 201，body 不含注入错误）。envID 取数据服务自身的 EnvID（绑定资源随资源环境走，由 OnBind 实现内部查 DataService 决定，handler 不传 envID，避免 application 依赖 dataservice）——修正：OnBind 签名不传 envID，实现自行从 DataService 取。
- DELETE `/api/applications/{id}/bindings/{type}/{name}`：若 type=dataservice，调 `OnUnbind` 清理注入条目（best-effort）。

**`cmd/core` 桥接实现**（`persistence.go` 或新 `binding_injector.go`）：

- `dsBindingInjector{ dsRepo dataservice.Repository, cfgRepo appconfig.Repository }`：
  - `OnBind`：`dsRepo.Get(ctx, name)` → 按 `ds.Kind` 算注入条目（key/value/envID）：
    - db：`DATABASE_URL = ds.Connection["uri"]`
    - cache：`REDIS_URL = ds.Connection["uri"]`
    - mq：`NATS_URL = ds.Connection["uri"]`
    - storage：`MINIO_ENDPOINT = ds.Connection["endpoint"]`、`MINIO_ACCESS_KEY = accessKey`、`MINIO_SECRET_KEY = secretKey`
    - 全部 `TypeSecret`（含密码）；envID = `ds.EnvID`；appID = 传入 appID。
    - 每条 `cfgRepo.Upsert(ctx, ConfigItem{AppID, EnvID, Key, Value, TypeSecret})`。
  - `OnUnbind`：按 Kind 算 key 集，逐条 `cfgRepo.Delete(ctx, appID, envID, key)`（envID 需查 ds；若 ds 已删则跳过 best-effort）。
- 注入到 application handler：`application.NewHandler(..., application.WithBindingInjector(inj))`。

**起步约定**（文档注明）：同应用×环境同 Kind 多数据服务绑定，固定 key 覆盖取最后绑定；后续可演进 `DS_<dsId>_<KEY>` 前缀避免冲突（YAGNI，本期不做）。

---

## C. 监控告警可视化

### C.1 后端 observability

- **`internal/observability/model.go`**：`TargetType` 注释/校验加 `dataservice`（`app|workload|env|dataservice`）；Validate 放行（现有是空校验，无需改逻辑，仅注释）。
- **`internal/observability/memory/store.go`**：seed 加 2 条 `targetType=dataservice` 惰性 series（ds-svc-mysql / ds-cache-redis，同 app/workload/env 模式：CPU/内存/RPS/延迟四指标随机游走）。
- **`internal/observability/real/metrics.go`**：targetType=dataservice 时，Prometheus 查询用 Pod 标签过滤：
  - CPU：`sum(rate(container_cpu_usage_seconds_total{pod=~"<dsName>-0", namespace="<ns>"}[5m]))`
  - 内存：`container_memory_working_set_bytes{pod=~"<dsName>-0", namespace="<ns>"}`
  - targetId = dsName；映射到 `paas_cpu_usage`/`paas_mem_usage` 语义返回（与 app/workload 同结构）。
  - 未配 PAAS_PROM_URL：compose 退回 memory 兜底（现状不变，无需改）。
- handler 不变（已透传 targetType/targetId）。

### C.2 前端

- **新建 `frontend/console-user/src/views/resources/DataServiceDetail.vue`**（路由 `/resources/:kind/:id`）：
  - 基本信息（Kind/Engine/Status/创建时间）。
  - 连接信息卡：host/port/user/database（或 accessKey）+ uri；password/secretKey 默认掩码 + 「显示/复制」按钮（复制走 `navigator.clipboard`，掩码时复制明文需二次确认）。
  - 监控卡：4 指标（CPU/内存/RPS/延迟）当前值 + CSS sparkline 趋势，10s 轮询（`GET /api/observability/metrics?targetType=dataservice&targetId=<id>`）。
  - 告警规则 section：targetType 下拉含 `dataservice`，复用现有 observability 告警 CRUD。
- 数据服务列表（`DataServices.vue`）点行 → 路由跳详情。

---

## 文件清单

| 文件 | 动作 | 职责 |
|------|------|------|
| `internal/dataservice/connection.go` | 新建 | GenerateCredentials / EnginePort / BuildConnection / MaskConnection（纯函数） |
| `internal/dataservice/connection_test.go` | 新建 | 纯函数单测 |
| `internal/dataservice/model.go` | 改 | +Connection 字段；KindMetas mq 加 nats 默认；NamespaceResolver 接口 |
| `internal/dataservice/handler.go` | 改 | list 掩码 / 详情明文 |
| `internal/dataservice/memory/store.go` + `pg/store.go` | 改 | Create 填 Connection；map 深拷（顺带修引用泄漏） |
| `api/core/v1alpha1/dataservice_types.go` | 改 | Spec 加 Credentials/Connection |
| `config/crds/` + `deploy/charts/paas/templates/crds.yaml` | 改 | `make manifests` 重新生成 |
| `internal/controller/dataservice_controller.go` | 改 | Secret+Svc+STS+env 注入+nats |
| `internal/controller/dataservice_controller_test.go` | 改 | fake client 测 Secret/Svc/STS/env |
| `internal/core/application/handler.go` | 改 | BindingInjector 接口 + OnBind/OnUnbind 调用 |
| `cmd/core/binding_injector.go`（或 persistence.go） | 新建/改 | dsBindingInjector 桥接 dataservice+appconfig |
| `cmd/core/persistence.go` / `serve.go` | 改 | 注入 NamespaceResolver + BindingInjector |
| `internal/observability/model.go` + `memory/store.go` + `real/metrics.go` | 改 | +dataservice targetType |
| `frontend/console-user/src/views/resources/DataServiceDetail.vue` | 新建 | 详情+连接+监控+告警 |
| `frontend/console-user/src/views/resources/DataServices.vue` | 改 | 点行跳详情 |
| `frontend/console-user/src/router` | 改 | 加 `/resources/:kind/:id` 路由 |
| `CLAUDE.md` + `CHANGELOG.md` | 改 | 文档 |

## 关键约束与风险

- **凭证安全**：crypto/rand 强随机；存 K8s Secret + domain 明文（同级敏感度，与 core env 一致）；list 掩码、详情明文；reconciler 日志只记 dsName/phase，**密码不进日志**。
- **幂等**：Secret 已存在不覆盖（mutate 仅创建时写 stringData）；CreateOrUpdate 天然幂等；删 CR 级联清 Secret/Service/STS（OwnerRef）。
- **依赖倒置**：application 不 import dataservice/appconfig（BindingInjector 接口）；dataservice 不依赖 K8s（纯函数生成连接）；observability 不依赖 dataservice（targetType 字符串约定）。
- **三模式一致**：Connection 全程控制面生成，dev/PG/K8s 行为一致；reconciler 只落地不回流（避免跨进程反向同步复杂度）。
- **KISS/YAGNI**：单副本 StatefulSet；固定注入 key；vector/search 占位；监控 Pod 级（引擎 exporter 留后续）；同 Kind 多绑定覆盖（前缀方案留后续）。
- **RBAC**：core SA 需对 secrets create/get/update（现 rbac.yaml 是否含 secrets 待确认，缺则补）；services/statefulsets 已在 apps 核心资源范围。
- **不碰 git**；Apache 2.0（无新依赖，k8s.io/client-go 已在树）。
- **集群网络**：FQDN `<name>.<ns>.svc.cluster.local` 要求应用 Pod 与数据服务同集群（跨 ns 也可）；数据服务与应用工作负载均落 `PAAS_K8S_NAMESPACE`。

## 测试策略

- **纯函数单测**（connection.go）：凭证随机性（长度/字符集/不同次不同）、BuildConnection 各 Kind uri 格式、MaskConnection 掩码正确。
- **fake client 测 reconciler**：创建→Secret/Svc/STS 均落地+env 正确+OwnerRef；幂等（二次 reconcile 不覆盖 Secret）；未知 engine→failed。
- **handler 测**：list 掩码、详情明文；application binding 注入调 OnBind（mock injector）。
- **桥接测**：dsBindingInjector.OnBind 各 Kind 写正确 appconfig 条目（mock repos）。
- **集群 e2e**（部署后）：建 mysql ds→Pod Running→`kubectl exec` 验证 `mysql -u root -p<password>` 可登录；建应用绑定→appconfig 出现 DATABASE_URL；建 redis→`redis-cli -a <password> ping` 返 PONG。
