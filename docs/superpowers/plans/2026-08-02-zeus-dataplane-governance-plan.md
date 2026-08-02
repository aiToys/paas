# zeus 数据面纳管 + 治理真实化 实施 Plan（P0+P1）

> 依据 spec：`docs/superpowers/specs/2026-08-02-zeus-dataplane-governance-design.md`
> 约束（CLAUDE.md）：未经用户明确要求**不 git commit/分支**；TDD；KISS/YAGNI；注释中文；多租户隔离由 Core 强制；Apache 2.0 依赖。

## 全局约束

- 主语言 Go，controller-runtime + k8s.io v0.36.0（已在依赖树）。
- 内存/PG 双路径必须同时工作；`PAAS_K8S_NAMESPACE` 空（非集群）时数据面相关降级返空，不 panic。
- 多租户隔离：`/dp/` 端点按 token 绑定 tenant 过滤，只返该租户 Endpoints。
- 不 commit（CLAUDE.md 明令）。每任务以「测试通过」为完成标志，不用 commit 步骤。
- `make manifests` 用 controller-gen 重生成 CRD（修改 `api/core/v1alpha1/*.go` 后必须跑）。

---

## Task 1: CRD WorkloadSpec 加 Port 字段

**文件**：`api/core/v1alpha1/workload_types.go`

**改 `WorkloadSpec`**（`:18-31` 末尾追加）：
```go
// Port 是 Service 对外暴露端口（service 类型且 >0 才建 K8s Service）。
// +kubebuilder:validation:Minimum=1
Port int32 `json:"port,omitempty"`
// ContainerPort 是 Pod 监听端口；空则 = Port。
// +kubebuilder:validation:Minimum=1
ContainerPort int32 `json:"containerPort,omitempty"`
```

**验证**：`make manifests` 重生成 CRD YAML（config/crds/ + deepcopy 无变化，int32 已有）；`go build ./api/...` 通过。

---

## Task 2: workload 领域 + handler + memory store 透传 Port

**文件**：`internal/workload/model.go`、`internal/workload/handler.go`、`internal/workload/memory/store.go`

**model.go** `Workload`（`:32-48`）追加：
```go
Port         int   `json:"port,omitempty"`
ContainerPort int  `json:"containerPort,omitempty"`
```
`Validate` 不强制 Port（0=不建 Service）。

**handler.go** Create/Update 解析 body 时透传 Port/ContainerPort（现有 struct tag 补字段）。

**memory/store.go** seed 给 service 类型工作负载补 Port（如 `Port:8080, ContainerPort:8080`）。

**验证**：`go test ./internal/workload/ -v` 通过（含 seed 断言 Port）。

---

## Task 3: controller 建 K8s Service + readiness probe（核心）

**文件**：`internal/controller/workload_controller.go`、`internal/controller/workload_controller_test.go`

**测试先行**（fake client）：
- `TestReconcileServiceCreatesService`：建 type=service Port=8080 的 Workload → reconcile → 断言 `corev1.Service` 存在、Name=Workload 名、Selector=`paas.aitoys/workload`、Ports[0].Port=8080、OwnerRef 指向 CR。
- `TestReconcileServiceNoPortSkipsService`：Port=0 → 无 Service。
- `TestReconcileJobNoService`：type=job → 无 Service。

**实现**：
1. `podSpec`：`ContainerPort>0` 时 container 加 `Ports` + readiness `TCPSocket` probe：
```go
if w.Spec.ContainerPort > 0 {
    cport := w.Spec.ContainerPort
    container.Ports = []corev1.ContainerPort{{ContainerPort: cport}}
    container.ReadinessProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
        TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(cport)},
    }, PeriodSeconds: 10, FailureThreshold: 3}
}
```
2. 新增 `applyService(ctx, w)`：
```go
func (r *WorkloadReconciler) applyService(ctx context.Context, w *v1alpha1.Workload) error {
    if w.Spec.Type != "service" || w.Spec.Port <= 0 { return nil }
    svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: w.Name, Namespace: w.Namespace}}
    _, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
        labels := labelsFor(w)
        svc.SetLabels(labels)
        if svc.CreationTimestamp.IsZero() {
            svc.Spec.Selector = labels  // selector 创建后不可变
        }
        cport := w.Spec.ContainerPort
        if cport <= 0 { cport = w.Spec.Port }
        svc.Spec.Ports = []corev1.ServicePort{{
            Port: w.Spec.Port, TargetPort: intstr.FromInt32(cport), Protocol: corev1.ProtocolTCP,
        }}
        return controllerutil.SetControllerReference(w, svc, r.Scheme)
    })
    return err
}
```
3. `applyDeployment` 成功后调 `r.applyService(ctx, w)`（错误 best-effort 记日志不阻断 status 回写）。
4. `SetupWithManager` 加 `.Owns(&corev1.Service{})`。
5. import 补 `k8s.io/apimachinery/pkg/util/intstr`。

**验证**：`go test ./internal/controller/ -run TestReconcile -v` 全绿；`make manifests`（无 CRD 字段变化需重生成，但 Owns 不影响 CRD）。

---

## Task 4: PG migration + pg store Port 字段

**文件**：`internal/storage/pg/migrations/0015_workload_ports.up.sql` + `.down.sql`、`internal/workload/pg/store.go`

**migration up**：`ALTER TABLE workloads ADD COLUMN port INTEGER NOT NULL DEFAULT 0; ADD COLUMN container_port INTEGER NOT NULL DEFAULT 0;`
**down**：`ALTER TABLE workloads DROP COLUMN container_port; DROP COLUMN port;`

**pg/store.go**：insert/select/scan 加 port/container_port 字段；seed 复用 memory 的 `SeedWorkloads()`（DRY，同一真源已补 Port）。

**验证**：`make test-pg` 通过（若环境无 PG，跳过，CI 跑）。

---

## Task 5: dataplane 模块 - K8s Endpoints reader（核心）

**文件**：新建 `internal/dataplane/endpoints.go` + `endpoints_test.go`

**接口设计**（依赖倒置，便于测试）：
```go
package dataplane

// EndpointsReader 读 K8s Endpoints 的 ready 实例（数据面服务发现真源）。
type EndpointsReader interface {
    // Instances 返回某 K8s Service（Endpoints 同名）的 ready 实例列表。
    Instances(ctx context.Context, namespace, serviceName string) ([]Instance, error)
    // Services 列某命名空间所有带 paas.aitoys 标签的 Service（供 Discovery 列服务）。
    Services(ctx context.Context, namespace string) ([]ServiceInfo, error)
}

// Instance 对齐 zeus types.Instance（数据面 SDK 消费）。
type Instance struct {
    ID       string            `json:"id"`
    Name     string            `json:"name"`
    Cluster  string            `json:"cluster"`  // 本期统一 "default"
    Protocol string            `json:"protocol"` // "http"
    IP       string            `json:"ip"`
    Port     int32             `json:"port"`
    Metadata map[string]string `json:"metadata,omitempty"`
}
```

**K8s 实现** `k8sEndpointsReader{clientset kubernetes.Interface}`：
- `Instances`：`clientset.CoreV1().Endpoints(ns).Get(ctx, serviceName, ...)` → 遍历 `Subsets`，`Addresses`（ready）每个 + 每个 `Ports` 生成 Instance（IP=`Address.IP`，Port=`Port.Port`）；忽略 `NotReadyAddresses`。无 Endpoints 时返空切片不报错。
- `Services`：`clientset.CoreV1().Services(ns).List` label selector `paas.aitoys/managed-by=paas`（或 `app.kubernetes.io/managed-by=paas`）。

**测试**：fake clientset（`k8s.io/client-go/kubernetes/fake`）灌 Endpoints（含 ready/not-ready），断言只返 ready、Port/IP 正确、空 Endpoints 返空。

**验证**：`go test ./internal/dataplane/ -run Endpoints -v` 通过。

---

## Task 6: dataplane handler + dp token 鉴权

**文件**：`internal/dataplane/handler.go`、`auth.go`、`handler_test.go`

**dp token 鉴权**（复用 API Key 机制）：
- `auth.go` `DPTokenAuth(idb identity.Repository)` 中间件：从 `Authorization: Bearer <dp-token>` 取 token → `identity.LookupAPIKey` → 注入 tenant ctx（`tenant.WithTenant`）。失败 401。dp token 是 role=`dataplane` 的专用 API Key（启动 seed 或工作负载创建时生成，本 Task 先支持任意有效 API Key，token 生成机制 Task 8 接 controller 注入）。

**handler**：
```go
type Handler struct {
    reader    EndpointsReader
    ns        string  // PAAS_K8S_NAMESPACE
    services  governance.ServiceStore  // 读服务元信息
}

// GET /dp/services → 列租户内服务（governance 表）
// GET /dp/instances?service=<name> → reader.Instances(ns, name) + signature
// POST /dp/register → 声明服务元信息（幂等 CreateService）
// DELETE /dp/register?id= → DeleteService
// PUT /dp/heartbeat → 刷 UpdatedAt（保留兼容）
```
- `signature` = sha256(sorted `id:ip:port` 串)，供 zeus Watcher 对比变化。
- `ns` 为空（非集群）→ `/dp/instances` 返空切片 + 200（降级）。
- ServeMux 风格：`handler.ServeHTTP` 按 method+path 分发。

**测试**：httptest + fake reader + 内存 identity（seed dp token），测：
- `/dp/instances` 返 fake reader 数据 + signature；
- 无 token 401；
- 错误 tenant 不泄漏（返空）；
- `ns=""` 降级返空。

**验证**：`go test ./internal/dataplane/ -v` 通过。

---

## Task 7: 挂 /dp/ 路由 + clientset 注入

**文件**：`cmd/core/main.go`、`cmd/core/manager.go`、`cmd/core/persistence.go`

**manager.go**：`k8sAppliers` 已有 `clientset`（builder.K8sJob 用）——暴露给 dataplane。无 clientset 时 dataplane reader=nil。

**persistence.go / main.go**：
- 构造 `dataplane.Handler{Reader: k8sEndpointsReader{clientset}, ns, services: govServiceStore}`。
- main.go `mux.Handle("/dp/", dpAuth(dpHandler))`（独立于 `auth` 控制 API，用 dp token 鉴权）。
- `/dp/` 不进 OpenAPI registry（数据面 SDK 消费，非人类契约）或登记为独立 spec（YAGNI，先不登记）。

**验证**：`go build ./cmd/core/` 通过；`./bin/core` 启动后 `curl /dp/services`（带 token）200。

---

## Task 8: controller 注入 dp token + PAAS_DP_ENDPOINT env

**文件**：`internal/controller/workload_controller.go`、`cmd/core/seed.go`（或 persistence）

**controller**：`podSpec` 给 container 加 env（仅 service 类型）：
```go
if w.Spec.Type == "service" {
    container.Env = append(container.Env,
        corev1.EnvVar{Name: "PAAS_DP_ENDPOINT", Value: "http://paas-core.paas.svc/dp"},
        corev1.EnvVar{Name: "PAAS_DP_TOKEN", Value: dpToken},
        corev1.EnvVar{Name: "PAAS_TENANT_ID", Value: w.Spec.TenantID},
    )
}
```
- `dpToken`：WorkloadReconciler 加字段 `DPToken string`（core 启动期 seed 一个平台级 dataplane token，cmd/core 注入）。**KISS**：先用单一平台级 dp token（绑 tenant 由 PAAS_TENANT_ID env 传 Pod，token 仅验有效性），生产化后续 per-workload token。

**seed**：`seedIdentity` 生成一个 `dataplane` role 的 API Key（或复用现有 admin key 作为 dp token，标注 TODO）。先 KISS：用现有 `sk-acme-admin` 作 dp token 演示，token 生成机制标 TODO。

**验证**：fake client 测 Pod env 含 PAAS_DP_TOKEN；集群 e2e 验证 Pod 收到 env。

---

## Task 9: rbac 补 endpoints 权限

**文件**：`deploy/charts/paas/templates/rbac.yaml`

ClusterRole 补：
```yaml
- apiGroups: [""]
  resources: ["endpoints"]
  verbs: ["get", "list", "watch"]
- apiGroups: [""]
  resources: ["services"]
  verbs: ["get", "list", "watch", "create", "update", "patch", "delete"]
```
（services 已有，确认补全 verbs；endpoints 新增。）

**验证**：`helm template paas deploy/charts/paas -f deploy/charts/paas/values-paas-k8s.yaml | rg endpoints` 出现。

---

## Task 10: sdk/paas-registry 独立 module（核心）

**文件**：新建 `sdk/paas-registry/go.mod`、`resolver.go`、`registry.go`、`registry_test.go`

**go.mod**：
```
module github.com/aitoys/paas/sdk/paas-registry
go 1.26
require github.com/go-zeus/zeus v0.1.0
replace github.com/go-zeus/zeus => /Users/wangtao/data/github.com/go-zeus/zeus
```
> **开源发布 TODO**：zeus 须先发布 public module 或 vendor 进本仓。

**resolver.go**：
```go
package paasregistry

import "github.com/go-zeus/zeus/app"

func init() {
    app.RegisterRegistryResolver("paas", func(rawURL string, opts ...app.Option) (registry.Registry, error) {
        u, err := url.Parse(rawURL)
        // paas://<host>/dp?token=<>&service=<>
        return newPaasRegistry(u), nil
    })
}
```
（注：resolver 签名对齐 `plugins/registry/etcd/resolver.go`，实施时确认 `app.RegisterRegistryResolver` 实际签名。）

**registry.go**：`paasRegistry{base, token, service, http.Client}` 实现：
- `Register(ctx, *types.Instance)`：POST `{base}/register` JSON。
- `Deregister(ctx, *types.Instance)`：DELETE `{base}/register?id=`。
- `GetService(ctx, name)`：GET `{base}/instances?service={name|service}` → JSON → `*types.ServiceEntry`。
- `Watch(ctx, name)`：goroutine 2s 轮询 GetService，sha256 签名对比，变化时 close 旧 ch 发新信号。

**registry_test.go**：httptest mock `/dp/`，测 Register/GetService 解析/Watch 签名变化触发。

**验证**：`cd sdk/paas-registry && go test ./...` 通过。

---

## Task 11: governance 控制台 Instance 切 K8s 源

**文件**：`internal/governance/handler.go`、`cmd/core/main.go`

**handler.go** `ListInstances`：若注入了 `EndpointsReader` + `ns` 非空，从 K8s Endpoints 读（real）；否则降级 Instance 表（内存/PG 现状）。Instance 字段从 K8s address 填充。

**main.go**：governance handler 注入 dataplane reader（与 /dp/ 共享）。

**验证**：`go test ./internal/governance/` 通过；集群 e2e：`GET /api/services/{id}/instances` 返真实 Pod IP。

---

## 验证清单（P0+P1 完成）

- [ ] `go build ./...` + `go test ./...` 全绿（内存路径）。
- [ ] `make manifests` CRD 含 port/containerPort。
- [ ] `make test-pg` 通过（PG 路径，环境具备时）。
- [ ] fake client：service 建 K8s Service + readiness；job 不建。
- [ ] dataplane：/dp/instances 返 fake Endpoints ready；dp token 鉴权；ns 空降级。
- [ ] sdk/paas-registry：Register/GetService/Watch 测通。
- [ ] helm template：rbac endpoints/services verbs 齐全。
- [ ] 集群 e2e（部署后）：Workload→Service DNS 可解析；/dp/instances 返真实 Pod；zeus 应用经 paas:// 发现（P3 examples 验证，本批可选）。

## 后续（不在本批）

P2（可观测应用级埋点 + 配置 watch + 熔断真实 stats）、P3（examples 电商 demo + bootstrap）。
