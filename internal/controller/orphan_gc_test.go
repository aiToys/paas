package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/aitoys/paas/api/core/v1alpha1"
)

// stubPlatform PlatformWorkloadLookup 桩：exists 控制 ID 集合，err 控制查询失败注入。
type stubPlatform struct {
	exists map[string]bool
	err    error
	calls  int
}

func (s *stubPlatform) Exists(_ context.Context, id string) (bool, error) {
	s.calls++
	if s.err != nil {
		return false, s.err
	}
	return s.exists[id], nil
}

func newOrphanCR(name string, age time.Duration) *v1alpha1.Workload {
	return &v1alpha1.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "paas-t-acme",
			CreationTimestamp: metav1.Time{Time: time.Now().Add(-age)},
		},
		Spec: v1alpha1.WorkloadSpec{TenantID: "t-acme", Type: "service", Name: name, Image: "nginx"},
	}
}

// 平台不存在的超龄 CR 应被删除；平台存在/未过宽限期的保留。
func TestCollectOrphansRemovesStaleCR(t *testing.T) {
	scheme := newScheme(t)
	stale := newOrphanCR("wl-stale", 24*time.Hour)  // 平台不存在 + 超宽限期 → 回收
	fresh := newOrphanCR("wl-fresh", 1*time.Minute) // 平台不存在但未过宽限期 → 保留
	alive := newOrphanCR("wl-alive", 24*time.Hour)  // 平台存在 → 保留
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(stale, fresh, alive).Build()
	plat := &stubPlatform{exists: map[string]bool{"wl-alive": true}}
	r := &WorkloadReconciler{Client: cl, Scheme: scheme, Platform: plat}

	r.collectOrphans(context.Background())

	for _, id := range []string{"wl-stale"} {
		if err := cl.Get(context.Background(), types.NamespacedName{Name: id, Namespace: "paas-t-acme"}, &v1alpha1.Workload{}); err == nil {
			t.Fatalf("%s 应被回收删除", id)
		}
	}
	for _, id := range []string{"wl-fresh", "wl-alive"} {
		if err := cl.Get(context.Background(), types.NamespacedName{Name: id, Namespace: "paas-t-acme"}, &v1alpha1.Workload{}); err != nil {
			t.Fatalf("%s 应保留: %v", id, err)
		}
	}
}

// Platform 查询失败（PG 抖动）时本轮跳过，不误删。
func TestCollectOrphansSkipsOnLookupError(t *testing.T) {
	scheme := newScheme(t)
	stale := newOrphanCR("wl-stale", 24*time.Hour)
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(stale).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme,
		Platform: &stubPlatform{err: errors.New("pg down")}}

	r.collectOrphans(context.Background())

	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-stale", Namespace: "paas-t-acme"}, &v1alpha1.Workload{}); err != nil {
		t.Fatalf("查询失败时不应删除: %v", err)
	}
}

// Platform nil（本地 dev 无 stores）不回收。
func TestCollectOrphansNilPlatform(t *testing.T) {
	scheme := newScheme(t)
	stale := newOrphanCR("wl-stale", 24*time.Hour)
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(stale).Build()
	r := &WorkloadReconciler{Client: cl, Scheme: scheme}

	r.collectOrphans(context.Background()) // 不应 panic / 删除

	if err := cl.Get(context.Background(), types.NamespacedName{Name: "wl-stale", Namespace: "paas-t-acme"}, &v1alpha1.Workload{}); err != nil {
		t.Fatalf("nil Platform 不应删除: %v", err)
	}
}
