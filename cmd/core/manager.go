package main

import (
	"context"
	"log"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/aitoys/paas/api/core/v1alpha1"
	"github.com/aitoys/paas/internal/controller"
	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/internal/workload"
)

// k8sAppliers 聚合 K8s 数据面 applier（workload + dataservice），供各 repo 装饰。
type k8sAppliers struct {
	workload    workload.Applier
	dataservice dataservice.Applier
}

// startManager 按 PAAS_KUBECONFIG 启 controller-runtime manager（K8s 数据面）。
// 非空则启 Workload + DataService Reconciler 并返回 applier 供 repo 装饰；
// 为空或启动失败则返回 nil（走纯 PG/memory，现状不变，降级不阻塞）。
func startManager() (k8sAppliers, context.CancelFunc) {
	kubeconfig := os.Getenv("PAAS_KUBECONFIG")
	if kubeconfig == "" {
		log.Printf("K8s 数据面: 未配 PAAS_KUBECONFIG，workload/dataservice 走 PG/memory（dev 路径）")
		return k8sAppliers{}, nil
	}
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	// 设 controller-runtime logger（否则 reconcile 错误被吞，无法排查）。
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	cfg := ctrl.GetConfigOrDie() // 读 KUBECONFIG（PAAS_KUBECONFIG 优先于默认）
	// controller-runtime 默认 metrics server 占 :8080，与 core HTTP 服务冲突；
	// 改到 PAAS_METRICS_ADDR（默认 :8081），空则禁用。
	metricsAddr := os.Getenv("PAAS_METRICS_ADDR")
	if metricsAddr == "" {
		metricsAddr = ":8081"
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: metricsAddr},
	})
	if err != nil {
		log.Printf("K8s 数据面: 启动 manager 失败（降级为无 K8s）: %v", err)
		return k8sAppliers{}, nil
	}
	if err := (&controller.WorkloadReconciler{Client: mgr.GetClient(), Scheme: scheme}).SetupWithManager(mgr); err != nil {
		log.Printf("K8s 数据面: 注册 WorkloadReconciler 失败（降级为无 K8s）: %v", err)
		return k8sAppliers{}, nil
	}
	if err := (&controller.DataServiceReconciler{Client: mgr.GetClient(), Scheme: scheme}).SetupWithManager(mgr); err != nil {
		log.Printf("K8s 数据面: 注册 DataServiceReconciler 失败（降级为无 K8s）: %v", err)
		return k8sAppliers{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		log.Printf("K8s 数据面: manager 启动（Workload+DataService Reconciler 运行，namespace=%s)", os.Getenv("PAAS_K8S_NAMESPACE"))
		if err := mgr.Start(ctx); err != nil {
			log.Printf("K8s 数据面: manager 退出: %v", err)
		}
	}()
	ns := os.Getenv("PAAS_K8S_NAMESPACE")
	return k8sAppliers{
		workload:    controller.NewK8sApplier(mgr.GetClient(), ns),
		dataservice: controller.NewDataServiceK8sApplier(mgr.GetClient(), ns),
	}, cancel
}
