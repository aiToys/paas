# 真实 K8s 数据面纳管设计

**日期**：2026-07-30
**状态**：待评审
**关联**：`2026-07-26-maas-platform-foundation-design.md`（混合部署 + 数据面/控制面解耦）、workload 切片（进程内 mock 待替换）、DevOps 切片（Release 编排）

## 背景与动机

workload 当前是**进程内 mock**（Create/Update 只改内存，无真实部署）。这是距「真实 PaaS」最大的功能缺口——控制面下发期望状态后，必须有真实数据面把它落到 K8s。

CLAUDE.md 关键约束：
- **混合部署**：控制面跑 K8s，数据面纳管 K8s 原生资源 + 外部实例。
- **数据面与控制面解耦**：控制面只下发 CRD 期望状态，**控制面挂了数据面继续跑**。
- **GPU 调度**：K8s device-plugin + 自研编排（本期显存核算 + 反亲和）。
- **技术栈**：controller-runtime + kubebuilder。

本切片把 workload 从 mock 替换为真实 K8s 编排：Workload CRD 作期望状态，controller-runtime controller reconcile 落 K8s Deployment/Job/CronJob。

## 范围

**做**：
- Workload CRD（期望状态：type/replicas/image/env/lane/schedule）。
- controller-runtime controller：reconcile Workload CRD → K8s Deployment（service）/ Job（job）/ CronJob（cronjob），status 回写。
- GPU：device-plugin 资源纳管（`nvidia.com/gpu`）+ 显存核算（请求显存 vs 可用）+ 反亲和（GPU 负载分散节点）。
- 控制面/数据面解耦验证（停控制面，已下发 Workload 继续跑）。

**不做（YAGNI / 归后续切片）**：
- 外部实例纳管（非 K8s 的 vLLM 裸金属 / 云 API）——归「外部数据面」切片。
- 自建 vLLM 部署——本期**纳管已有 vLLM**（通过 Deployment + device-plugin），不自建推理引擎（轻资产路线）。
- 泳道流量染色路由——归服务治理泳道切片（LaneID 字段已预留，CRD 带上但不实现染色）。
- 蓝绿/金丝雀策略实现——Release 接口已开放，本期只 rolling。
- HPA/VPA 自动伸缩——手动 replicas 优先。

## 架构

```
cmd/coremanager (或 core 内嵌 manager)          控制面
  ├─ controller-runtime manager
  │   ├─ WorkloadReconciler (watch Workload CRD)
  │   └─ GPU 调度器（显存核算 + 反亲和）
  └─ 写 K8s API：Deployment/Job/CronJob（数据面实际运行体）

Release 编排（DevOps）→ workload.Repository.Update → Workload CRD spec
  → controller reconcile → K8s rolling update → status.ready 回写
```

**解耦**：Workload CRD 是期望状态真源。controller 是数据面组件（watch + reconcile）。控制面（core/manager）挂了，K8s 按已下发 CRD 继续运行（Deployment 不会被删，controller 重启后继续 reconcile）。Release 编排只更新 CRD spec.image，不直接操作 K8s——经 controller 中介。

## 设计

### Workload CRD

`config/crds/`（kubebuilder 生成 + apimachinery types）：

```go
// api/v1/workload_types.go
type WorkloadSpec struct {
    Type      WorkloadType // service|job|cronjob
    Replicas  *int32
    Image     string       // 镜像（Release 编排更新此字段）
    ImageRef  string       // 不可变 digest（生产锁定）
    EnvID     string
    LaneID    string       // default=基线，预留
    Schedule  string       // cron（cronjob）
    Containers []ContainerSpec // 含 GPU 请求
    Affinity   *GPUAffinity
}
type WorkloadStatus struct {
    Ready       int32
    Conditions  []metav1.Condition
}
// +kubebuilder:object:root=true
type Workload struct { Spec WorkloadSpec; Status WorkloadStatus; ... }
```

CRD 通过 `make manifests`（controller-gen）生成 YAML，`config/crds/` 提交。

### WorkloadReconciler

```go
func (r *WorkloadReconciler) Reconcile(ctx, req) (ctrl.Result, error) {
    // 1. Get Workload CRD
    // 2. 按 Type 映射目标资源：
    //    service → Deployment（apps/v1）
    //    job → Job（batch/v1）
    //    cronjob → CronJob（batch/v1）
    // 3. GPU 显存核算：sum(containers.gpu) vs node 可用，反亲和分散
    // 4. CreateOrUpdate 目标资源（server-side apply，幂等）
    // 5. 读目标资源 status → 回写 Workload.status.ready
    // 6. 最终一致：spec.replicas == status.ready 时 Done
}
```

幂等（CreateOrUpdate）、最终一致（retry till ready）、status 回写。envtest（controller-runtime 测试框架）覆盖。

### GPU 调度（显存核算 + 反亲和）

- **device-plugin 纳管**：节点装 nvidia device-plugin，K8s 暴露 `nvidia.com/gpu` extended resource。Workload spec 声明 GPU 请求。
- **显存核算**：调度前查节点 GPU 显存可分配（`nvidia.com/gpu` 计数 + 自定义 `gpu-memory` extended resource 或 device-class），请求超可用 → 拒绝（pending）。
- **反亲和**：`podAntiAffinity` 把 GPU 工作负载分散到不同节点（避免单节点 GPU 过载），`topologyKey: kubernetes.io/hostname`。
- 本期**显存核算 + 反亲和**（CLAUDE.md 明确），不做细粒度 MIG/切分（留后续）。

### workload.Repository 桥接

现有 `workload.Repository`（PG 已迁）作为控制面侧抽象。新增 `WorkloadReconciler` 不直接读写 workloads 表，而是 watch K8s Workload CRD。控制面（core）的 workload handler 把 Create/Update 转为写 K8s Workload CRD（client-go）：

```
workload handler Create → K8s client.Create(Workload CRD)
  → controller reconcile → Deployment → status 回写
workload handler List → 读 K8s Workload CRD（转 Repository 模型）或读 PG 镜像（双写？）
```

> **双写决策**：workload.Repository（PG）存期望 + 派生状态供 API 查询；K8s CRD 存运行态。本期 PG 作 API 查询源，CRD 作 reconcile 源，controller reconcile 时同步 PG status（或 handler 直接读 K8s，PG 仅 seed/dev 用）。具体在 plan 细化，spec 只定「PG + K8s 双视图，CRD 为 reconcile 真源」。

## 验收标准

1. `make manifests install`（CRD + RBAC）应用到 K8s（kind 集群）。
2. 创建 Workload CRD（type=service, replicas=2, image=nginx）→ controller reconcile → Deployment 起来 → Workload.status.ready=2。
3. 扩缩容 `spec.replicas` → Deployment 跟随 scale。
4. DevOps Release 更新 image → Workload.spec.image 变 → controller rolling update。
5. GPU 显存核算：声明 GPU 超可用 → Workload pending（不调度）。
6. **解耦验证**：停 core/manager pod，已下发 Workload 对应的 Deployment 不受影响（K8s 接管）；manager 恢复后继续 reconcile。
7. envtest 单测覆盖 reconcile 幂等 + status 回写 + GPU 核算。
8. license：controller-runtime/apache 一致 Apache 2.0。

## 风险与对策

- **envtest 环境复杂**：需 etcd + kube-apiserver 二进制（testenv setup）。对策：CI 加 envtest setup 步骤；本地 `make test-k8s` 封装。
- **reconcile 幂等/最终一致**：易出 race。对策：server-side apply（`Force: false`）+ status 子资源更新（separate write）。
- **GPU 资源调度依赖 device-plugin**：无 GPU 节点无法验显存核算。对策：envtest 注入 fake GPU node（自定义 extended resource）；真实 GPU 集成测试归后续。
- **PG/K8s 双视图漂移**：PG 状态 vs K8s 实际可能不一致。对策：controller 定期 resync（全量 reconcile）+ PG status 以 K8s 为准。
- **范围蔓延**：K8s 切片易做大。对策：严格 YAGNI（不做外部实例/泳道/蓝绿金丝雀），CRD + reconcile + GPU 核算 + 解耦验证四项即收。
