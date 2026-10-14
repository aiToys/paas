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

// newOrphanDS 构造指定年龄的 DataService CR（孤儿回收测试用）。
func newOrphanDS(name string, age time.Duration) *v1alpha1.DataService {
	return &v1alpha1.DataService{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "paas-t-acme",
			CreationTimestamp: metav1.Time{Time: time.Now().Add(-age)},
		},
		Spec: v1alpha1.DataServiceSpec{TenantID: "t-acme", Kind: "db", Engine: "postgres"},
	}
}

// 数据服务孤儿回收：平台不存在 + 超宽限期删；平台存在 / 未过宽限期 / 查询失败 / nil 桥接保留。
// （2026-09-25 dataservice 补齐 OrphanGC 的回归锚点：此前平台侧删除后 CR+STS 永久残留，实测 9 个孤儿。）
func TestCollectOrphansDataService(t *testing.T) {
	scheme := newScheme(t)
	stale := newOrphanDS("ds-9000000000000000001-1", 24*time.Hour)  // 平台不存在 + 超宽限期 → 回收
	fresh := newOrphanDS("ds-9000000000000000002-1", 1*time.Minute) // 平台不存在但未过宽限期 → 保留
	alive := newOrphanDS("ds-9000000000000000003-1", 24*time.Hour)  // 平台存在 → 保留
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(stale, fresh, alive).Build()
	r := &DataServiceReconciler{Client: cl, Scheme: scheme,
		PlatformExists: func(_ context.Context, id string) (bool, error) {
			return id == "ds-9000000000000000003-1", nil
		}}

	r.collectOrphans(context.Background())

	if err := cl.Get(context.Background(), types.NamespacedName{Name: "ds-9000000000000000001-1", Namespace: "paas-t-acme"}, &v1alpha1.DataService{}); err == nil {
		t.Fatal("孤儿 ds CR 应被回收删除")
	}
	for _, id := range []string{"ds-9000000000000000002-1", "ds-9000000000000000003-1"} {
		if err := cl.Get(context.Background(), types.NamespacedName{Name: id, Namespace: "paas-t-acme"}, &v1alpha1.DataService{}); err != nil {
			t.Fatalf("%s 应保留: %v", id, err)
		}
	}
}

// PlatformExists 查询失败（PG 抖动）时数据服务本轮跳过，不误删。
func TestCollectOrphansDataServiceLookupError(t *testing.T) {
	scheme := newScheme(t)
	stale := newOrphanDS("ds-9000000000000000001-1", 24*time.Hour)
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(stale).Build()
	r := &DataServiceReconciler{Client: cl, Scheme: scheme,
		PlatformExists: func(context.Context, string) (bool, error) { return false, errors.New("pg down") }}

	r.collectOrphans(context.Background())

	if err := cl.Get(context.Background(), types.NamespacedName{Name: "ds-9000000000000000001-1", Namespace: "paas-t-acme"}, &v1alpha1.DataService{}); err != nil {
		t.Fatalf("查询失败时不应删除: %v", err)
	}
}

// PlatformExists nil（本地 dev 无 stores）不回收，不 panic。
func TestCollectOrphansDataServiceNilPlatform(t *testing.T) {
	scheme := newScheme(t)
	stale := newOrphanDS("ds-9000000000000000001-1", 24*time.Hour)
	cl := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(stale).Build()
	r := &DataServiceReconciler{Client: cl, Scheme: scheme}

	r.collectOrphans(context.Background())

	if err := cl.Get(context.Background(), types.NamespacedName{Name: "ds-9000000000000000001-1", Namespace: "paas-t-acme"}, &v1alpha1.DataService{}); err != nil {
		t.Fatalf("nil PlatformExists 不应删除: %v", err)
	}
}
