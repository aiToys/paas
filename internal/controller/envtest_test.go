//go:build integration

// envtest 集成测试：拉起本地 etcd + apiserver（controller-runtime envtest），
// 验证 WorkloadReconciler 在真实 K8s API server 下创建 Deployment。
//
// 运行前提：安装 KUBEBUILDER_ASSETS（envtest binary）：
//
//	go install sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
//	$(go env GOPATH)/bin/setup-envtest use latest --use-env
//
// 然后：
//
//	make test-envtest   # 或 KUBEBUILDER_ASSETS=... go test -tags=integration ./internal/controller/
//
// 无 assets 时 envtest.Start 报错，测试 fail（非 skip）——CI 应预装 binary。
package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	corev1alpha1 "github.com/aitoys/paas/api/core/v1alpha1"
)

func TestEnvtestReconcilerCreatesDeployment(t *testing.T) {
	// envtest 启动（需 KUBEBUILDER_ASSETS 指向 etcd+apiserver binary）
	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../config/crds"},
		CRDInstallOptions:     envtest.CRDInstallOptions{Paths: []string{"../../config/crds/core.aitoys.github.com_workloads.yaml"}},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	if err != nil {
		t.Skipf("envtest binary 未安装（KUBEBUILDER_ASSETS），跳过: %v", err)
		return
	}
	defer testEnv.Stop()

	require.NoError(t, scheme.AddToScheme(scheme.Scheme))
	require.NoError(t, corev1alpha1.AddToScheme(scheme.Scheme))

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme.Scheme})
	require.NoError(t, err)
	require.NoError(t, (&WorkloadReconciler{Client: mgr.GetClient(), Scheme: scheme.Scheme}).SetupWithManager(mgr))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = mgr.Start(ctx) }()

	// 创建 Workload CR，期望 reconciler 创建 Deployment
	wl := &corev1alpha1.Workload{}
	wl.Name = "envtest-svc"
	wl.Namespace = "default"
	wl.Spec.Type = "service"
	wl.Spec.Replicas = 1
	wl.Spec.Image = "nginx:latest"
	require.NoError(t, mgr.GetClient().Create(ctx, wl))

	// 轮询 Deployment 被创建
	var dep corev1.Deployment
	err = wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 10*time.Second, true,
		func(ctx context.Context) (bool, error) {
			err := mgr.GetClient().Get(ctx, types.NamespacedName{Name: "envtest-svc", Namespace: "default"}, &dep)
			return err == nil, nil
		})
	require.NoError(t, err, "reconciler 应创建 Deployment")
	assert.Equal(t, "nginx:latest", dep.Spec.Template.Spec.Containers[0].Image)
}
