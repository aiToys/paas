package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/aitoys/paas/api/core/v1alpha1"
	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/pkg/tenant"
)

func TestDataServiceK8sApplierApplyAndDelete(t *testing.T) {
	scheme := newScheme(t)
	cl := clientfake.NewClientBuilder().WithScheme(scheme).Build()
	a := NewDataServiceK8sApplier(cl)

	d := dataservice.DataService{
		ID: "ds-2", TenantID: "t-acme", Kind: dataservice.KindCache, Name: "ds-2", EnvID: "env-1",
		Spec: map[string]string{"engine": "valkey"},
	}
	if err := a.Apply(context.Background(), d); err != nil {
		t.Fatalf("apply 失败: %v", err)
	}
	var crd v1alpha1.DataService
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "ds-2", Namespace: tenant.Namespace("t-acme")}, &crd); err != nil {
		t.Fatalf("应创建 DataService CRD: %v", err)
	}
	if crd.Spec.Engine != "valkey" {
		t.Fatalf("engine 应透传 valkey，实得 %s", crd.Spec.Engine)
	}
	if crd.Spec.Kind != "cache" {
		t.Fatalf("kind 应为 cache")
	}

	// 幂等：再 apply 不报错（CreateOrUpdate）。
	if err := a.Apply(context.Background(), d); err != nil {
		t.Fatalf("幂等 apply 失败: %v", err)
	}

	if err := a.Delete(tenant.WithTenant(context.Background(), "t-acme"), "ds-2"); err != nil {
		t.Fatalf("delete 失败: %v", err)
	}
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "ds-2", Namespace: tenant.Namespace("t-acme")}, &crd); err == nil {
		t.Fatalf("删除后 CRD 应不存在")
	}
}

func TestEngineOfFallback(t *testing.T) {
	// 无 engine 字段时按 Kind 给默认（与 KindMeta.Default 对齐）。
	d := dataservice.DataService{Kind: dataservice.KindMQ, Spec: map[string]string{}}
	if got := dataservice.EngineOf(d); got != "nats" {
		t.Fatalf("mq 默认 engine 应为 nats，实得 %s", got)
	}
	// 显式 engine 优先。
	d2 := dataservice.DataService{Kind: dataservice.KindMQ, Spec: map[string]string{"engine": "rabbitmq"}}
	if got := dataservice.EngineOf(d2); got != "rabbitmq" {
		t.Fatalf("显式 engine 应优先，实得 %s", got)
	}
}

// 确保 v1alpha1.DataService 实现 runtime.Object（已由 deepcopy 保证），编译期校验。
var _ metav1.Object = &v1alpha1.DataService{}
