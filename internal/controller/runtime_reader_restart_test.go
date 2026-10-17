package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/aitoys/paas/pkg/tenant"
)

// rsFor 构造属于指定 Deployment 的 ReplicaSet（owner 链 + revision + restart token 注解）。
func rsFor(dep, revision, token string, replicas, ready int32) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      dep + "-" + revision,
			Namespace: tenant.Namespace("t-acme"),
			Labels:    paasLabels("t-acme"),
			Annotations: map[string]string{
				"deployment.kubernetes.io/revision": revision,
			},
			OwnerReferences: []metav1.OwnerReference{
				{Kind: "Deployment", Name: dep, APIVersion: "apps/v1", UID: "dep-uid"},
			},
		},
		Spec: appsv1.ReplicaSetSpec{
			Template: podTemplateWithToken(token),
		},
		Status: appsv1.ReplicaSetStatus{Replicas: replicas, ReadyReplicas: ready},
	}
}

func podTemplateWithToken(token string) (t corev1.PodTemplateSpec) {
	t.Annotations = map[string]string{"paas.aitoys/restart-token": token}
	return
}

// TestRestartProgress_LagWindowNotCompleted 锁定集群实测缺陷（2026-10-17）：
// 新 RS 已创建但 Deployment 聚合 status 仍反映旧版满编（UpdatedReplicas 滞后窗口），
// completed 判定必须锚定 RS 级状态——否则 0.5s 内假性 succeeded，幂等窗口形同虚设。
func TestRestartProgress_LagWindowNotCompleted(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "wl-1", Namespace: tenant.Namespace("t-acme"), Labels: paasLabels("t-acme")},
			Spec:       appsv1.DeploymentSpec{Replicas: int32Ptr(1)},
			// 滞后窗口：聚合 status 还是旧版满编 1/1/1
			Status: appsv1.DeploymentStatus{Replicas: 1, UpdatedReplicas: 1, ReadyReplicas: 1, AvailableReplicas: 1},
		},
		rsFor("wl-1", "1", "rt-old", 1, 1), // 旧 RS 仍在跑
		rsFor("wl-1", "2", "rt-new", 0, 0), // 新 RS 刚创建未起副本
	)
	r := NewK8sRuntimeReader(cs, nil)
	phase, _, err := r.RestartProgress(tenant.WithTenant(context.Background(), "t-acme"), "wl-1", "rt-new")
	if err != nil {
		t.Fatalf("RestartProgress: %v", err)
	}
	if phase == "completed" {
		t.Fatal("滞后窗口（新 RS 0 副本 + 旧 RS 满编）不得判 completed——旧副本未退场")
	}
}

func TestRestartProgress_RollingStates(t *testing.T) {
	cases := []struct {
		name string
		dep  *appsv1.Deployment
		rss  []*appsv1.ReplicaSet
		want string
	}{
		{
			name: "真完成：新 RS 满编就绪 + 旧 RS 归零",
			dep:  depWithStatus(1),
			rss:  []*appsv1.ReplicaSet{rsFor("wl-1", "1", "rt-old", 0, 0), rsFor("wl-1", "2", "rt-new", 1, 1)},
			want: "completed",
		},
		{
			name: "滚动中：新 RS 起了未就绪，旧 RS 残留",
			dep:  depWithStatus(1),
			rss:  []*appsv1.ReplicaSet{rsFor("wl-1", "1", "rt-old", 1, 1), rsFor("wl-1", "2", "rt-new", 1, 0)},
			want: "running",
		},
		{
			name: "滚动中：旧 RS 残留（新 RS 已就绪）",
			dep:  depWithStatus(1),
			rss:  []*appsv1.ReplicaSet{rsFor("wl-1", "1", "rt-old", 1, 1), rsFor("wl-1", "2", "rt-new", 1, 1)},
			want: "running",
		},
		{
			name: "superseded：最新 RS token 不匹配",
			dep:  depWithStatus(1),
			rss:  []*appsv1.ReplicaSet{rsFor("wl-1", "1", "rt-old", 0, 0), rsFor("wl-1", "2", "rt-newer", 1, 1)},
			want: "superseded",
		},
		{
			name: "投影未生效：只有旧 RS",
			dep:  depWithStatus(1),
			rss:  []*appsv1.ReplicaSet{rsFor("wl-1", "1", "rt-old", 1, 1)},
			want: "superseded", // 旧 token 即最新 → 本次重启已被覆盖视角
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := []runtime.Object{tc.dep}
			for _, rs := range tc.rss {
				objs = append(objs, rs)
			}
			cs := fake.NewSimpleClientset(objs...)
			r := NewK8sRuntimeReader(cs, nil)
			phase, _, err := r.RestartProgress(tenant.WithTenant(context.Background(), "t-acme"), "wl-1", "rt-new")
			if err != nil {
				t.Fatalf("RestartProgress: %v", err)
			}
			if phase != tc.want {
				t.Errorf("phase = %s, want %s", phase, tc.want)
			}
		})
	}
}

func depWithStatus(replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "wl-1", Namespace: tenant.Namespace("t-acme"), Labels: paasLabels("t-acme")},
		Spec:       appsv1.DeploymentSpec{Replicas: int32Ptr(replicas)},
	}
}

func int32Ptr(i int32) *int32 { return &i }
