package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/aitoys/paas/api/core/v1alpha1"
)

func TestDataServiceReconcileCreatesStatefulSet(t *testing.T) {
	scheme := newScheme(t)
	d := &v1alpha1.DataService{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-1", Namespace: "default"},
		Spec:       v1alpha1.DataServiceSpec{TenantID: "t-acme", Kind: "db", Name: "ds-1", Engine: "postgres"},
	}
	cl := clientfake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(d).
		WithStatusSubresource(&v1alpha1.DataService{}).
		Build()
	r := &DataServiceReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "ds-1", Namespace: "default"}}); err != nil {
		t.Fatalf("reconcile 失败: %v", err)
	}
	var sts appsv1.StatefulSet
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "ds-1", Namespace: "default"}, &sts); err != nil {
		t.Fatalf("应创建 StatefulSet: %v", err)
	}
	if got := sts.Spec.Template.Spec.Containers[0].Image; got != "postgres:15-alpine" {
		t.Fatalf("postgres 引擎镜像应为 postgres:15-alpine，实得 %s", got)
	}
	// status 应回写 phase + image（ready=0 因 fake client 无 ReadyReplicas）。
	var got v1alpha1.DataService
	_ = cl.Get(context.Background(), types.NamespacedName{Name: "ds-1", Namespace: "default"}, &got)
	if got.Status.Phase != "creating" {
		t.Fatalf("ready=0 时 phase 应为 creating，实得 %s", got.Status.Phase)
	}
	if got.Status.Image != "postgres:15-alpine" {
		t.Fatalf("status.image 应回写落地镜像")
	}
}

func TestDataServiceReconcileUnknownEngineFailsPhase(t *testing.T) {
	scheme := newScheme(t)
	d := &v1alpha1.DataService{
		ObjectMeta: metav1.ObjectMeta{Name: "ds-x", Namespace: "default"},
		Spec:       v1alpha1.DataServiceSpec{Kind: "unknown-kind", Name: "ds-x", Engine: "nope"},
	}
	cl := clientfake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(d).
		WithStatusSubresource(&v1alpha1.DataService{}).
		Build()
	r := &DataServiceReconciler{Client: cl, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "ds-x", Namespace: "default"}}); err != nil {
		t.Fatalf("未知 kind 不应返错（应记 failed phase）: %v", err)
	}
	var got v1alpha1.DataService
	_ = cl.Get(context.Background(), types.NamespacedName{Name: "ds-x", Namespace: "default"}, &got)
	if got.Status.Phase != "failed" {
		t.Fatalf("未知 kind/engine 组合 phase 应为 failed，实得 %s", got.Status.Phase)
	}
}

func TestEngineImageCoverage(t *testing.T) {
	cases := map[string]string{
		"db|postgres": "postgres:15-alpine",
		"db|mysql":    "mysql:8",
		"cache|redis": "redis:7-alpine",
		"mq|kafka":    "bitnami/kafka:3.7",
		"storage|":    "minio/minio:latest",
		"vector|":     "milvusdb/milvus:latest",
		"search|":     "docker.elastic.co/elasticsearch/elasticsearch:8.13.0",
	}
	for k, want := range cases {
		kind := k
		engine := ""
		for i := 0; i < len(k); i++ {
			if k[i] == '|' {
				kind = k[:i]
				engine = k[i+1:]
				break
			}
		}
		if got := engineImage(kind, engine); got != want {
			t.Errorf("engineImage(%q,%q)=%q, want %q", kind, engine, got, want)
		}
	}
}
