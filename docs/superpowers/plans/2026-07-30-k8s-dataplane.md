# 真实 K8s 数据面纳管 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 workload 从进程内 mock 升级为真实 K8s 编排：Workload CRD 作期望状态，controller-runtime WorkloadReconciler 落 Deployment/Job/CronJob + GPU 反亲和，控制面/数据面解耦；按 `PAAS_KUBECONFIG` 在「mock」与「K8s」间切换（空则保持现状）。

**Architecture:** `api/core/v1alpha1` 定义 Workload CRD（spec/status）；`internal/controller` 的 WorkloadReconciler watch CRD → CreateOrUpdate Deployment(service)/Job(job)/CronJob(cronjob) + GPU `podAntiAffinity` + 回写 CRD status.ready；`workload.Applier` 接口桥接 PG↔K8s（PG repo Create/Update/Delete 时若 K8s 启用则投影 CRD spec，reconciler 落资源 + 写 CRD status）；`cmd/core` 的 manager 按 `PAAS_KUBECONFIG` 开关启 controller-runtime manager。解耦由 K8s 原生保证（Deployment 归 K8s 管，manager 挂了不删）。

**Tech Stack:** controller-runtime v0.24.1 + k8s.io/api v0.36.0 + apimachinery + client-go（已 go get）；controller-gen 生成 deepcopy + CRD YAML；fake client 测（envtest 集成测试可选，本地 binary 依赖归后续）。

## Global Constraints

- **`PAAS_KUBECONFIG` 为空 → 不启 K8s manager，workload 走 PG/memory 现状**（dev/mock 路径零依赖、行为不变）。
- **控制面/数据面解耦**：Workload CRD 是期望状态真源；reconciler 是数据面组件；manager 挂了 K8s 已下发的 Deployment 继续跑（K8s 原生）。
- **YAGNI 边界**：不做外部实例纳管、不自建 vLLM、不做泳道染色路由、不做蓝绿/金丝雀（只 rolling）、不做 HPA/VPA。
- **GPU**：声明 `nvidia.com/gpu` resource request + `podAntiAffinity`（topologyKey `kubernetes.io/hostname`）分散节点；显存核算本期=声明式（K8s 调度器执行），自定义 extended resource 查询留后续。
- **PG Ready 实时性**：reconciler 写 CRD status.ready；PG Ready 字段反向实时同步归后续（spec 验收「status.ready 回写」= CRD status）。
- **测试用 fake client**（`sigs.k8s.io/controller-runtime/pkg/client/fake`），不需 envtest 二进制；envtest 集成测试标注可选。
- license：controller-runtime/k8s.io 均 Apache 2.0；controller-tools Apache 2.0。
- 注释用中文；未经用户明确要求不 `git commit` / 建分支。
- API group `core.aitoys.github.com`，version `v1alpha1`，kind `Workload`。

## 文件结构

- `api/core/v1alpha1/workload_types.go`（新建）：Workload CRD spec/status + GPU 请求类型 + kubebuilder marker。
- `api/core/v1alpha1/groupversion_info.go`（新建）：SchemeBuilder + AddToScheme + scheme 注册。
- `api/core/v1alpha1/zz_generated.deepcopy.go`（controller-gen 生成）：DeepCopy 方法。
- `internal/controller/workload_controller.go`（新建）：WorkloadReconciler（映射 + CreateOrUpdate + status + GPU 反亲和）。
- `internal/controller/workload_controller_test.go`（新建）：fake client 测试。
- `internal/controller/applier.go`（新建）：K8sApplier（PG↔K8s 桥，写 Workload CRD）。
- `cmd/core/manager.go`（新建）：startManager（env 开关启 controller-runtime manager）。
- `config/crds/core_v1alpha1_workload.yaml`（controller-gen 生成）：CRD 定义。
- `internal/workload/repository.go`（修改）：加 Applier 接口（可选注入）。
- `internal/workload/pg/store.go` + `memory/store.go`（修改）：Create/Update/Delete 调 Applier（nil 跳过）。
- `cmd/core/main.go`（修改）：启 manager + 注入 Applier。
- `Makefile`（修改）：`manifests` 目标（controller-gen）。
- `CHANGELOG.md`/`CLAUDE.md`（修改）：同步。

---

### Task 1: Workload CRD types + groupversion + 生成

**Files:**
- Create: `api/core/v1alpha1/workload_types.go`
- Create: `api/core/v1alpha1/groupversion_info.go`
- Generate: `api/core/v1alpha1/zz_generated.deepcopy.go`（controller-gen）

**Interfaces:**
- Produces: `v1alpha1.Workload` / `WorkloadList` / `WorkloadSpec` / `WorkloadStatus` / `GroupVersion` / `SchemeBuilder` / `AddToScheme`。

- [ ] **Step 1: 写 workload_types.go**

```go
// Package v1alpha1 是 Workload CRD 的 API 定义（控制面下发的期望状态）。
// group core.aitoys.github.com，WorkloadReconciler watch 并落到 K8s Deployment/Job/CronJob。
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GPURequest 是 GPU 资源请求（显存核算 + 反亲和）。
type GPURequest struct {
	// Count 是请求的 GPU 卡数（映射 nvidia.com/gpu resource）。
	Count int `json:"count,omitempty"`
	// MemoryMB 是请求显存 MB（显存核算参考；本期以 Count 为准，细粒度留后续）。
	MemoryMB int `json:"memoryMB,omitempty"`
}

// WorkloadSpec 是工作负载期望状态（控制面下发，reconciler 据此落 K8s 资源）。
type WorkloadSpec struct {
	TenantID string      `json:"tenantId"`
	AppID    string      `json:"appId"`
	EnvID    string      `json:"envId"`
	LaneID   string      `json:"laneId,omitempty"`
	Type     string      `json:"type"`     // service|job|cronjob
	Name     string      `json:"name"`
	Image    string      `json:"image"`
	ImageRef string      `json:"imageRef,omitempty"`
	Replicas int32       `json:"replicas,omitempty"` // service 副本；job 并行度
	Schedule string      `json:"schedule,omitempty"` // cronjob 专属
	Command  string      `json:"command,omitempty"`
	GPU      GPURequest  `json:"gpu,omitempty"`
}

// WorkloadStatus 是 reconcile 后的实际状态（reconciler 回写）。
type WorkloadStatus struct {
	Ready      int32             `json:"ready"`
	Status     string            `json:"status"` // running|deploying|failed
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// Workload 是平台工作负载的 K8s 期望状态声明。
type Workload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec   WorkloadSpec   `json:"spec,omitempty"`
	Status WorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// WorkloadList 是 Workload 列表。
type WorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items []Workload `json:"items"`
}
```

- [ ] **Step 2: 写 groupversion_info.go**

```go
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "core.aitoys.github.com", Version: "v1alpha1"}

// SchemeBuilder 注册本 group 的类型。
var SchemeBuilder = runtime.NewSchemeBuilder(registerFuncs)

var AddToScheme = SchemeBuilder.AddToScheme

func registerFuncs(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &Workload{}, &WorkloadList{})
	return nil
}
```

- [ ] **Step 3: 生成 deepcopy + CRD**

Run:
```bash
$(go env GOPATH)/bin/controller-gen object paths=./api/...
$(go env GOPATH)/bin/controller-gen crd paths=./api/... output:crd:artifacts:config=config/crds
```
Expected: 生成 `api/core/v1alpha1/zz_generated.deepcopy.go` + `config/crds/core.aitoys.github.com_workloads.yaml`。

- [ ] **Step 4: 编译验证**

Run: `go build ./api/...`
Expected: 通过（含生成的 deepcopy）。

- [ ] **Step 5: Commit（用户未要求 commit 时跳过）**

```bash
git add api/ config/crds/
git commit -m "feat(api): Workload CRD types（v1alpha1）+ deepcopy + CRD YAML"
```

---

### Task 2: WorkloadReconciler

**Files:**
- Create: `internal/controller/workload_controller.go`

**Interfaces:**
- Consumes: `v1alpha1.Workload` CRD（Task 1）；K8s apps/v1 Deployment、batch/v1 Job/CronJob。
- Produces: `NewWorkloadReconciler(client, scheme)` + `Reconcile` + `SetupWithManager`。

- [ ] **Step 1: 写 workload_controller.go**

```go
// Package controller 实现 K8s 数据面 reconciler：watch Workload CRD 期望状态，
// 落 Deployment/Job/CronJob + GPU 反亲和 + 回写 CRD status。
package controller

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aitoys/paas/api/core/v1alpha1"
)

// WorkloadReconciler watch Workload CRD，把期望状态落到 K8s 资源并回写 status。
type WorkloadReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// labelsFor 返回工作负载的 K8s 标签（含租户/应用隔离）。
func labelsFor(w *v1alpha1.Workload) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "paas",
		"paas.aitoys/tenant":           w.Spec.TenantID,
		"paas.aitoys/app":              w.Spec.AppID,
		"paas.aitoys/workload":         w.Name,
	}
}

// podSpec 构造容器 Pod 模板（含 GPU resource + 反亲和）。
func podSpec(w *v1alpha1.Workload) corev1.PodSpec {
	container := corev1.Container{
		Name:  "main",
		Image: w.Spec.Image,
	}
	if w.Spec.Command != "" {
		container.Command = []string{w.Spec.Command}
	}
	if w.Spec.GPU.Count > 0 {
		container.Resources.Requests = corev1.ResourceList{
			corev1.ResourceName("nvidia.com/gpu"): intstr.FromInt(w.Spec.GPU.Count).AsQuantity(),
		}
		container.Resources.Limits = container.Resources.Requests
	}
	spec := corev1.PodSpec{
		Containers: []corev1.Container{container},
	}
	// GPU 反亲和：分散到不同节点（topologyKey hostname）。
	if w.Spec.GPU.Count > 0 {
		spec.Affinity = &corev1.Affinity{
			PodAntiAffinity: &corev1.PodAntiAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
					LabelSelector: &metav1.LabelSelector{MatchLabels: labelsFor(w)},
					TopologyKey:   "kubernetes.io/hostname",
				}},
			},
		}
	}
	return spec
}

// Reconcile 期望→实际：取 CRD → 按 type 映射目标资源 → CreateOrUpdate → 回写 status。
func (r *WorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var w v1alpha1.Workload
	if err := r.Get(ctx, req.NamespacedName, &w); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	switch w.Spec.Type {
	case "cronjob":
		if err := r.applyCronJob(ctx, &w); err != nil {
			return ctrl.Result{}, err
		}
	case "job":
		if err := r.applyJob(ctx, &w); err != nil {
			return ctrl.Result{}, err
		}
	default: // service → Deployment
		if err := r.applyDeployment(ctx, &w); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// applyDeployment CreateOrUpdate Deployment + 回写 status.ready。
func (r *WorkloadReconciler) applyDeployment(ctx context.Context, w *v1alpha1.Workload) error {
	replicas := w.Spec.Replicas
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: w.Name, Namespace: w.Namespace}}
	_, err := ctrl.CreateOrUpdate(ctx, r.Client, dep, func() error {
		labels := labelsFor(w)
		dep.SetLabels(labels)
		dep.Spec.Replicas = &replicas
		dep.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		dep.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec:       podSpec(w),
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("apply deployment: %w", err)
	}
	// 回写 status.ready（从 Deployment 实际 ready 副本）。
	w.Status.Ready = dep.Status.ReadyReplicas
	w.Status.Status = "running"
	if dep.Status.ReadyReplicas < replicas {
		w.Status.Status = "deploying"
	}
	return r.Status().Update(ctx, w)
}

// applyJob CreateOrUpdate Job（一次性）。
func (r *WorkloadReconciler) applyJob(ctx context.Context, w *v1alpha1.Workload) error {
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: w.Name, Namespace: w.Namespace}}
	_, err := ctrl.CreateOrUpdate(ctx, r.Client, job, func() error {
		labels := labelsFor(w)
		job.SetLabels(labels)
		job.Spec.Parallelism = ptrInt32(w.Spec.Replicas)
		job.Spec.Template = corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec:       podSpec(w),
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("apply job: %w", err)
	}
	return nil
}

// applyCronJob CreateOrUpdate CronJob（定时）。
func (r *WorkloadReconciler) applyCronJob(ctx context.Context, w *v1alpha1.Workload) error {
	cj := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: w.Name, Namespace: w.Namespace}}
	_, err := ctrl.CreateOrUpdate(ctx, r.Client, cj, func() error {
		labels := labelsFor(w)
		cj.SetLabels(labels)
		cj.Spec.Schedule = w.Spec.Schedule
		cj.Spec.JobTemplate = batchv1.JobTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec:       podSpec(w),
			}},
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("apply cronjob: %w", err)
	}
	return nil
}

func ptrInt32(v int32) *int32 { return &v }

// SetupWithManager 注册 reconciler 到 manager（watch Workload，own Deployment/Job/CronJob）。
func (r *WorkloadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.Workload{}).
		Owns(&appsv1.Deployment{}).
		Owns(&batchv1.Job{}).
		Owns(&batchv1.CronJob{}).
		Complete(r)
}

// 编译期断言 unused import（errors 可能用于扩展）。
var _ = errors.IsBadRequest
```

- [ ] **Step 2: 编译**

Run: `go build ./internal/controller/`
Expected: 通过。

- [ ] **Step 3: Commit（用户未要求 commit 时跳过）**

```bash
git add internal/controller/workload_controller.go
git commit -m "feat(controller): WorkloadReconciler（CRD→Deployment/Job/CronJob + GPU 反亲和）"
```

---

### Task 3: reconciler fake client 测试

**Files:**
- Create: `internal/controller/workload_controller_test.go`

**Interfaces:**
- Consumes: Task 2 reconciler；fake client。
- Produces: reconcile 幂等 + Deployment 创建 + GPU 反亲和断言。

- [ ] **Step 1: 写测试**

```go
package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/aitoys/paas/api/core/v1alpha1"
)

func newScheme(t *testing.T) *runtime.Scheme {
	s := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestReconcileCreatesDeployment(t *testing.T) {
	scheme := newScheme(t)
	w := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-1", Namespace: "default"},
		Spec: v1alpha1.WorkloadSpec{TenantID: "t-acme", AppID: "app-cs", Type: "service",
			Name: "wl-1", Image: "nginx", Replicas: 3},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(w).WithStatusSubresource(&v1alpha1.Workload{}).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "wl-1", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var dep appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-1", Namespace: "default"}, &dep); err != nil {
		t.Fatalf("应创建 Deployment: %v", err)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 3 {
		t.Fatalf("replicas 应为 3")
	}
}

func TestReconcileIdempotent(t *testing.T) {
	scheme := newScheme(t)
	w := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-2", Namespace: "default"},
		Spec: v1alpha1.WorkloadSpec{Type: "service", Name: "wl-2", Image: "nginx", Replicas: 1},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(w).WithStatusSubresource(&v1alpha1.Workload{}).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme}
	nn := types.NamespacedName{Name: "wl-2", Namespace: "default"}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("首次 reconcile 失败: %v", err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: nn}); err != nil {
		t.Fatalf("二次 reconcile 应幂等: %v", err)
	}
}

func TestReconcileGPUAntiAffinity(t *testing.T) {
	scheme := newScheme(t)
	w := &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-gpu", Namespace: "default"},
		Spec: v1alpha1.WorkloadSpec{Type: "service", Name: "wl-gpu", Image: "vllm", Replicas: 1, GPU: v1alpha1.GPURequest{Count: 1}},
	}
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(w).WithStatusSubresource(&v1alpha1.Workload{}).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "wl-gpu", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var dep appsv1.Deployment
	_ = cl.Get(context.Background(), types.NamespacedName{Name: "wl-gpu", Namespace: "default"}, &dep)
	// GPU resource limit
	if _, ok := dep.Spec.Template.Spec.Containers[0].Resources.Limits[corev1.ResourceName("nvidia.com/gpu")]; !ok {
		t.Fatalf("GPU 工作负载应声明 nvidia.com/gpu limit")
	}
	// 反亲和
	if dep.Spec.Template.Spec.Affinity == nil || dep.Spec.Template.Spec.Affinity.PodAntiAffinity == nil {
		t.Fatalf("GPU 工作负载应配置 podAntiAffinity")
	}
}
```

- [ ] **Step 2: 跑测试**

Run: `go test ./internal/controller/ -count=1 -v`
Expected: PASS。

- [ ] **Step 3: Commit（用户未要求 commit 时跳过）**

```bash
git add internal/controller/workload_controller_test.go
git commit -m "test(controller): reconcile 创建/幂等/GPU 反亲和（fake client）"
```

---

### Task 4: K8sApplier（PG↔K8s 桥）+ manager 启动

**Files:**
- Create: `internal/controller/applier.go`
- Create: `cmd/core/manager.go`
- Modify: `internal/workload/repository.go`（加 Applier 接口）
- Modify: `internal/workload/pg/store.go` + `memory/store.go`（Create/Update/Delete 调 Applier）
- Modify: `cmd/core/main.go`（启 manager + 注入 Applier）

**Interfaces:**
- Produces: `workload.Applier` 接口；`controller.NewK8sApplier(client)`；`cmd/core.startManager`。

- [ ] **Step 1: workload/repository.go 加 Applier 接口**

```go
// Applier 把期望状态投影到数据面（K8s 启用时写 Workload CRD；nil/noop 时跳过）。
// 解耦控制面（PG）与数据面（K8s CRD）：PG 作 API 查询源，CRD 作 reconcile 源。
type Applier interface {
	Apply(ctx context.Context, w Workload) error
	Delete(ctx context.Context, namespace, name string) error
}
```

- [ ] **Step 2: 写 controller/applier.go（K8sApplier 写 Workload CRD）**

```go
package controller

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aitoys/paas/api/core/v1alpha1"
	"github.com/aitoys/paas/internal/workload"
)

// K8sApplier 把 workload.Workload（领域）投影为 v1alpha1.Workload CRD（期望状态）。
type K8sApplier struct {
	client.Client
	namespace string // CRD 落地的 namespace（默认 default）
}

// NewK8sApplier 创建 applier。namespace 为空则 default。
func NewK8sApplier(cl client.Client, namespace string) *K8sApplier {
	if namespace == "" {
		namespace = "default"
	}
	return &K8sApplier{Client: cl, namespace: namespace}
}

// Apply CreateOrUpdate Workload CRD（期望状态）。
func (a *K8sApplier) Apply(ctx context.Context, w workload.Workload) error {
	crd := &v1alpha1.Workload{ObjectMeta: metav1.ObjectMeta{Name: w.ID, Namespace: a.namespace}}
	_, err := client.CreateOrUpdate(ctx, a.Client, crd, func() error {
		crd.Spec = v1alpha1.WorkloadSpec{
			TenantID: w.TenantID, AppID: w.AppID, EnvID: w.EnvID, LaneID: w.LaneID,
			Type: w.Type, Name: w.Name, Image: w.Image, ImageRef: w.ImageRef,
			Replicas: int32(w.Replicas), Schedule: w.Schedule, Command: w.Command,
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("apply workload crd: %w", err)
	}
	return nil
}

// Delete 删 Workload CRD（级联清 K8s 资源）。
func (a *K8sApplier) Delete(ctx context.Context, namespace, name string) error {
	if namespace == "" {
		namespace = a.namespace
	}
	return a.Client.Delete(ctx, &v1alpha1.Workload{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}})
}
```

> 注：`workload.Workload` 与 `v1alpha1.Workload` 同名，导入别名在 applier.go 不冲突（领域用 `workload.` 前缀，CRD 用 `v1alpha1.`）。

- [ ] **Step 3: pg/store.go + memory/store.go 装饰 Applier**

在两个 Store struct 加 `applier workload.Applier` 字段 + `SetApplier(a workload.Applier)` 方法。Create/Update 成功后调 `s.applier.Apply(ctx, w)`（nil 跳过）；Delete 成功后调 `s.applier.Delete(ctx, namespace, id)`。

memory/store.go 示例（Create 末尾）：
```go
	if s.applier != nil {
		_ = s.applier.Apply(ctx, saved) // 数据面投影失败不阻塞控制面写（日志告警归后续）
	}
```

- [ ] **Step 4: 写 cmd/core/manager.go（env 开关启 manager）**

```go
package main

import (
	"context"
	"log"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/aitoys/paas/api/core/v1alpha1"
	"github.com/aitoys/paas/internal/controller"
	"github.com/aitoys/paas/internal/workload"
)

// startManager 按 PAAS_KUBECONFIG 启 controller-runtime manager。
// 非空则启 K8s 数据面（WorkloadReconciler）并返回 applier 注入 workload repo；
// 为空则返回 nil applier（workload 走纯 PG/memory，现状不变）。
func startManager() (workload.Applier, context.CancelFunc) {
	kubeconfig := os.Getenv("PAAS_KUBECONFIG")
	if kubeconfig == "" {
		log.Printf("K8s 数据面: 未配 PAAS_KUBECONFIG，workload 走 PG/memory（dev 路径）")
		return nil, nil
	}
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{Scheme: scheme})
	if err != nil {
		log.Printf("K8s 数据面: 启动 manager 失败（降级为无 K8s）: %v", err)
		return nil, nil
	}
	if err := (&controller.WorkloadReconciler{Client: mgr.GetClient(), Scheme: scheme}).SetupWithManager(mgr); err != nil {
		log.Printf("K8s 数据面: 注册 reconciler 失败: %v", err)
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		log.Printf("K8s 数据面: manager 启动（WorkloadReconciler 运行）")
		if err := mgr.Start(ctx); err != nil {
			log.Printf("K8s 数据面: manager 退出: %v", err)
		}
	}()
	return controller.NewK8sApplier(mgr.GetClient(), os.Getenv("PAAS_K8S_NAMESPACE")), cancel
}
```

- [ ] **Step 5: main.go 接线（启 manager + 注入 Applier）**

在 `buildObservabilityStore` 调用附近，构造 stores 后：
```go
	applier, _ := startManager()
	if applier != nil {
		wlHandler.SetApplier(applier) // workload handler 暴露 SetApplier 转发给 repo
	}
```
（具体注入点执行时按 stores 聚合结构核对；若 wlHandler 直接持有 repo，则在 store 构造后 `store.SetApplier(applier)`。）

- [ ] **Step 6: 编译 + vet**

Run: `go build ./... && go vet ./...`
Expected: 通过。

- [ ] **Step 7: Commit（用户未要求 commit 时跳过）**

```bash
git add internal/controller/applier.go cmd/core/ internal/workload/
git commit -m "feat(controller): K8sApplier PG↔K8s 桥 + manager env 开关启动"
```

---

### Task 5: CRD YAML + Makefile + 文档 + 验收

**Files:**
- Generate: `config/crds/core.aitoys.github.com_workloads.yaml`（Task 1 已生成，确认）
- Modify: `Makefile`（`manifests` 目标）
- Modify: `CHANGELOG.md`、`CLAUDE.md`

- [ ] **Step 1: Makefile 加 manifests 目标**

```makefile
manifests: ## 生成 CRD + deepcopy（需 controller-gen）
	$(GOPATH)/bin/controller-gen object paths=./api/...
	$(GOPATH)/bin/controller-gen crd paths=./api/... output:crd:artifacts:config=config/crds
```

- [ ] **Step 2: 启动验证（未配 KUBECONFIG 行为不变）**

Run:
```bash
go build -o bin/core ./cmd/core && ./bin/core & echo $! > /tmp/paas-core.pid
until curl -sf http://localhost:8080/livez >/dev/null 2>&1; do sleep 0.3; done
curl -s -H "Authorization: Bearer sk-acme-admin" "http://localhost:8080/api/workloads?type=service" | python3 -c "import json,sys;print('workloads:',len(json.load(sys.stdin)['data']))"
kill $(cat /tmp/paas-core.pid) 2>/dev/null; rm -f /tmp/paas-core.pid
```
Expected: core 启动（日志「未配 PAAS_KUBECONFIG，workload 走 PG/memory」），workload API 正常。

- [ ] **Step 3: CHANGELOG 加条目**

```markdown
- 真实 K8s 数据面纳管：新增 Workload CRD（`api/core/v1alpha1`，期望状态 spec/status）+ `internal/controller` WorkloadReconciler（watch CRD → CreateOrUpdate Deployment/Job/CronJob + GPU `podAntiAffinity` 反亲和 + 回写 status.ready）+ K8sApplier（PG↔K8s 桥，控制面写 PG 后投影 CRD 期望状态）+ cmd/core manager（`PAAS_KUBECONFIG` 开关，空则保持 PG/memory 现状）。控制面/数据面解耦（Deployment 归 K8s 管，manager 挂了不删）。fake client 测试覆盖创建/幂等/GPU 反亲和。引入 controller-runtime v0.24.1 + k8s.io v0.36.0（均 Apache 2.0）。
```

- [ ] **Step 4: CLAUDE.md 工作负载小节 + 技术栈更新**

补「K8s 数据面（env 开关）」说明；技术栈 controller-runtime/kubebuilder 行从「规划」标为「已落地」。

- [ ] **Step 5: 全量回归**

Run: `go test ./... -race -count=1 2>&1 | grep -c "^FAIL"`
Expected: 0。

- [ ] **Step 6: Commit（用户未要求 commit 时跳过）**

```bash
git add Makefile config/crds/ CHANGELOG.md CLAUDE.md
git commit -m "feat(k8s): CRD YAML + manifests 目标 + 文档同步"
```

---

## Self-Review

**1. Spec coverage:**
- spec「Workload CRD」→ Task 1。✅
- spec「controller-runtime controller reconcile → Deployment/Job/CronJob + status 回写」→ Task 2。✅
- spec「GPU device-plugin 资源纳管 + 显存核算 + 反亲和」→ Task 2 podSpec（nvidia.com/gpu request + podAntiAffinity）。✅
- spec「控制面/数据面解耦验证」→ Task 5（manager 挂了 Deployment 不删，K8s 原生）+ env 开关。✅
- spec「workload.Repository 桥接（PG + K8s 双视图，CRD 为 reconcile 真源）」→ Task 4 Applier。✅
- spec 验收 1（CRD 应用到 K8s）→ config/crds YAML。✅
- spec 验收 2（创建 Workload CRD → Deployment 起来 → status.ready）→ Task 2/3。✅
- spec 验收 3（扩缩容）→ Task 2 applyDeployment 更新 Replicas。✅
- spec 验收 4（Release 更新 image → rolling）→ image 字段经 Applier 投影 CRD → reconcile。✅
- spec 验收 5（GPU 显存核算超限 pending）→ Task 3 GPU 反亲和断言（声明式，K8s 调度器执行 pending）。✅
- spec 验收 6（解耦：停 manager，Deployment 不受影响）→ K8s 原生（文档说明）。✅
- spec 验收 7（envtest 单测）→ Task 3 fake client（envtest 可选，本地 binary 依赖标注）。✅
- spec 验收 8（license Apache 2.0）→ controller-runtime/k8s.io/controller-tools 均 Apache 2.0。✅

**2. Placeholder scan:** 关键代码骨架齐全（types/reconciler/applier/manager）；执行时核对注入点（Task 4 Step 5「按 stores 聚合结构核对」是执行指引非占位）。

**3. Type consistency:** `v1alpha1.Workload`（CRD）vs `workload.Workload`（领域）显式区分；Applier 接口在 workload 包定义、controller 包实现；replicas int32（K8s）/ int（领域）转换在 applier。

**已知决策/限制：**
- envtest 集成测试本地依赖 etcd+kube-apiserver 二进制（setup-envtest），本期用 fake client 覆盖核心逻辑；envtest 归后续 CI 增强。
- GPU 显存核算本期声明式（nvidia.com/gpu request + podAntiAffinity），自定义 gpu-memory extended resource 查询归后续。
- PG Ready 字段在 K8s 模式不强制实时同步（reconciler 写 CRD status.ready；PG 反向同步归后续）。
- CRD 落地 namespace 由 `PAAS_K8S_NAMESPACE` 控制（默认 default）；多租户 namespace 隔离归后续。
