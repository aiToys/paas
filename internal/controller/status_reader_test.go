package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

// paasLabels 是 PaaS 工作负载资源的标准标签（reconciler labelsFor 同款）。
func paasLabels(tid string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/managed-by": "paas",
		"paas.aitoys/tenant":           tid,
	}
}

func TestK8sStatusReader_FillStatusService(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "wl-1", Namespace: "paas", Labels: paasLabels("t-acme")},
			Status:     appsv1.DeploymentStatus{Replicas: 3, ReadyReplicas: 2},
		},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "wl-2", Namespace: "paas", Labels: paasLabels("t-acme")},
			Status:     appsv1.DeploymentStatus{Replicas: 2, ReadyReplicas: 2},
		},
	)
	r := NewK8sStatusReader(cs, "paas")
	wls := []workload.Workload{
		{ID: "wl-1", TenantID: "t-acme", Type: workload.TypeService, Replicas: 3, Status: "deploying"},
		{ID: "wl-2", TenantID: "t-acme", Type: workload.TypeService, Replicas: 2, Status: "deploying"},
		// 无匹配 Deployment：保持 store 原值
		{ID: "wl-x", TenantID: "t-acme", Type: workload.TypeService, Replicas: 1, Ready: 1, Status: "running"},
	}
	if err := r.FillStatus(tenant.WithTenant(context.Background(), "t-acme"), wls); err != nil {
		t.Fatalf("FillStatus: %v", err)
	}
	if wls[0].Ready != 2 || wls[0].Status != workload.StatusDeploying {
		t.Errorf("wl-1: ready=%d status=%s, want 2/deploying", wls[0].Ready, wls[0].Status)
	}
	if wls[1].Ready != 2 || wls[1].Status != workload.StatusRunning {
		t.Errorf("wl-2: ready=%d status=%s, want 2/running", wls[1].Ready, wls[1].Status)
	}
	if wls[2].Ready != 1 || wls[2].Status != "running" {
		t.Errorf("wl-x 无匹配应保持原值: ready=%d status=%s", wls[2].Ready, wls[2].Status)
	}
}

func TestK8sStatusReader_FillStatusFailed(t *testing.T) {
	// Progressing=False（ProgressDeadlineExceeded）+ Ready=0 + Replicas>0 -> failed
	cs := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "wl-fail", Namespace: "paas", Labels: paasLabels("t-acme")},
			Status: appsv1.DeploymentStatus{Replicas: 1, ReadyReplicas: 0, Conditions: []appsv1.DeploymentCondition{{
				Type: appsv1.DeploymentProgressing, Status: "False", Reason: "ProgressDeadlineExceeded",
			}}},
		},
	)
	r := NewK8sStatusReader(cs, "paas")
	wls := []workload.Workload{{ID: "wl-fail", TenantID: "t-acme", Type: workload.TypeService, Replicas: 1, Status: "deploying"}}
	if err := r.FillStatus(tenant.WithTenant(context.Background(), "t-acme"), wls); err != nil {
		t.Fatalf("FillStatus: %v", err)
	}
	if wls[0].Status != workload.StatusFailed {
		t.Errorf("Progressing=False 应 failed, got %s", wls[0].Status)
	}
}

func TestK8sStatusReader_FillStatusJobAndCron(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Name: "wl-job", Namespace: "paas", Labels: paasLabels("t-acme")},
			Status:     batchv1.JobStatus{Succeeded: 1},
		},
		&batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Name: "wl-cron", Namespace: "paas", Labels: paasLabels("t-acme")},
			Status:     batchv1.CronJobStatus{Active: []corev1.ObjectReference{{}, {}}},
		},
	)
	r := NewK8sStatusReader(cs, "paas")
	wls := []workload.Workload{
		{ID: "wl-job", TenantID: "t-acme", Type: workload.TypeJob, Status: "running"},
		{ID: "wl-cron", TenantID: "t-acme", Type: workload.TypeCronJob, Status: "pending"},
	}
	if err := r.FillStatus(tenant.WithTenant(context.Background(), "t-acme"), wls); err != nil {
		t.Fatalf("FillStatus: %v", err)
	}
	if wls[0].Ready != 1 || wls[0].Status != workload.StatusSucceeded {
		t.Errorf("job: ready=%d status=%s, want 1/succeeded", wls[0].Ready, wls[0].Status)
	}
	if wls[1].Ready != 2 || wls[1].Status != workload.StatusRunning {
		t.Errorf("cron: ready=%d status=%s, want 2/running", wls[1].Ready, wls[1].Status)
	}
}

func TestK8sStatusReader_NilAndNoTenant(t *testing.T) {
	// clientset nil -> no-op（降级，保持原值）
	r := NewK8sStatusReader(nil, "paas")
	wls := []workload.Workload{{ID: "wl-1", Type: workload.TypeService, Ready: 5, Status: "running"}}
	if err := r.FillStatus(tenant.WithTenant(context.Background(), "t-acme"), wls); err != nil {
		t.Fatalf("nil clientset should no-op: %v", err)
	}
	if wls[0].Ready != 5 {
		t.Errorf("nil clientset 不应改原值, got ready=%d", wls[0].Ready)
	}
	// 无租户上下文 -> no-op（fail-closed）
	cs := fake.NewSimpleClientset()
	r2 := NewK8sStatusReader(cs, "paas")
	wls2 := []workload.Workload{{ID: "wl-1", Type: workload.TypeService, Ready: 5, Status: "running"}}
	if err := r2.FillStatus(context.Background(), wls2); err != nil {
		t.Fatalf("no tenant should no-op: %v", err)
	}
	if wls2[0].Ready != 5 {
		t.Errorf("无租户上下文不应改原值, got ready=%d", wls2[0].Ready)
	}
}

func TestK8sStatusReader_TenantIsolation(t *testing.T) {
	// 跨租户：t-acme 查不到 t-globex 的 Deployment（label selector 限定本租户）
	cs := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "wl-g", Namespace: "paas", Labels: paasLabels("t-globex")},
			Status:     appsv1.DeploymentStatus{Replicas: 2, ReadyReplicas: 2},
		},
	)
	r := NewK8sStatusReader(cs, "paas")
	wls := []workload.Workload{{ID: "wl-g", TenantID: "t-acme", Type: workload.TypeService, Replicas: 2, Ready: 0, Status: "deploying"}}
	_ = r.FillStatus(tenant.WithTenant(context.Background(), "t-acme"), wls)
	if wls[0].Ready != 0 {
		t.Errorf("跨租户不应回填（隔离）, got ready=%d", wls[0].Ready)
	}
}
