package main

import (
	"context"
	"log"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/aitoys/paas/api/core/v1alpha1"
	"github.com/aitoys/paas/internal/controller"
	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/internal/workload"
)

// k8sAppliers 聚合 K8s 数据面 applier（workload + dataservice），供各 repo 装饰。
// clientset + namespace 额外供 builder.K8sJob 创建构建 Job + 取 Pod 日志。
// wlReconciler 暴露 WorkloadReconciler 引用，供 stores 构造完成后延迟注入 AppConfigLookup
// （startManager 先于 buildAllStores，stores 就绪后才能装 appconfig 桥接）。
type k8sAppliers struct {
	workload     workload.Applier
	dataservice  dataservice.Applier
	dsRestarter  *controller.DSRestarter // 数据服务实例滚动重启（patch STS），nil=集群外降级
	clientset    kubernetes.Interface    // 供 builder.K8sJob（create Job + pods/log）；nil=K8s 不可用
	wlReconciler *controller.WorkloadReconciler
	client       client.Client // controller-runtime typed client（供 route applier 聚合 Ingress）
}

// startManager 启 controller-runtime manager（K8s 数据面），自动检测配置来源：
//   - PAAS_KUBECONFIG 显式指定（本地开发：~/.kube/config）
//   - 否则 ctrl.GetConfig 自动检测 in-cluster（SA token + KUBERNETES_SERVICE_HOST，
//     集群内 Deployment 部署）或默认 KUBECONFIG
//
// 启 Workload + DataService Reconciler 并返回 applier 供 repo 装饰；
// 无可用 config 或启动失败则返回 nil（走纯 PG/memory，降级不阻塞）。
func startManager() (k8sAppliers, context.CancelFunc) {
	// 显式禁用开关：本地 dev 设 PAAS_K8S_ENABLED=false 强制纯内存模式。
	// 原因：~/.kube/config 存在时 ctrl.GetConfig 会自动拾取，导致本地 core 启 manager
	// 连集群数据面——dev 写操作经 ApplyRepo 投影 CRD 到集群（意外操作生产）+ manager
	// informer/metrics 在非集群环境卡死。生产集群部署不设此开关（in-cluster 自动启用）。
	if strings.EqualFold(os.Getenv("PAAS_K8S_ENABLED"), "false") {
		log.Println("K8s 数据面: PAAS_K8S_ENABLED=false，纯内存模式（dev，不连集群）")
		return k8sAppliers{}, nil
	}
	// PAAS_KUBECONFIG 作为 KUBECONFIG 覆盖（ctrl.GetConfig 读 KUBECONFIG，不读 PAAS_KUBECONFIG）。
	if kc := os.Getenv("PAAS_KUBECONFIG"); kc != "" {
		_ = os.Setenv("KUBECONFIG", kc)
	}
	cfg, err := ctrl.GetConfig() // KUBECONFIG env 或 in-cluster 自动；都无则 error
	if err != nil {
		log.Printf("K8s 数据面: 无可用 config（%v），workload/dataservice 走 PG/memory", err)
		return k8sAppliers{}, nil
	}
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	// 设 controller-runtime logger（否则 reconcile 错误被吞，无法排查）。
	ctrl.SetLogger(zap.New(zap.UseDevMode(os.Getenv("PAAS_PROD") != "true")))
	// controller-runtime 默认 metrics server 占 :8080，与 core HTTP 服务冲突；
	// 改到 PAAS_METRICS_ADDR（默认 :8081），空则禁用。
	metricsAddr := os.Getenv("PAAS_METRICS_ADDR")
	if metricsAddr == "" {
		metricsAddr = ":8081"
	}
	mgrOpts := ctrl.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: metricsAddr},
	}
	// cache 集群级 watch（不限定 namespace）：数据面 CRD 按租户落在 paas-<tenant> 多 ns，
	// 限定单 ns 会 watch 不到其他租户的 CRD 导致 reconcile 漏掉。控制面 ns（paas）与数据面 ns
	// 一并 watch。集群级 CRD 数量可控（仅 Workload+DataService），watch 负载可接受。
	mgr, err := ctrl.NewManager(cfg, mgrOpts)
	if err != nil {
		log.Printf("K8s 数据面: 启动 manager 失败（降级为无 K8s）: %v", err)
		return k8sAppliers{}, nil
	}
	wlReconciler := &controller.WorkloadReconciler{
		Client: mgr.GetClient(), Scheme: scheme,
		// 数据面接入 token/端点注入 service 类型 Pod env（zeus 应用经 paas-registry 插件发现 PaaS）。
		// token 来自 PAAS_DP_TOKEN env（helm values dataplane.token），空则不注入。
		DPToken:    os.Getenv("PAAS_DP_TOKEN"),
		DPEndpoint: os.Getenv("PAAS_DP_ENDPOINT_DEFAULT"),
		// OTel trace 推送地址（PAAS_OTEL_ENDPOINT，集群内 jaeger:4318）。注入 service 类型
		// Pod env，应用 observ.Init 据此建 tracer 推 Jaeger。与 core 自身 tracing.Init 同源 env。
		OtelEndpoint: os.Getenv("PAAS_OTEL_ENDPOINT"),
		// 排障上下文：集群标识注入 Pod env（应用读入 OTel 资源属性 paas.cluster）。
		// 默认 "default"（单集群）；多区部署时 helm set 区分。
		ClusterID: clusterIDFromEnv(),
		// 应用域名->自动 Ingress 的 ingressClassName（env PAAS_INGRESS_CLASS，默认 hermes）。
		// workload spec.domain 非空时 reconciler 建 Ingress，host=domain -> Service:port。
		IngressClass: ingressClassFromEnv(),
	}
	if err := wlReconciler.SetupWithManager(mgr); err != nil {
		log.Printf("K8s 数据面: 注册 WorkloadReconciler 失败（降级为无 K8s）: %v", err)
		return k8sAppliers{}, nil
	}
	if err := (&controller.DataServiceReconciler{Client: mgr.GetClient(), Scheme: scheme}).SetupWithManager(mgr); err != nil {
		log.Printf("K8s 数据面: 注册 DataServiceReconciler 失败（降级为无 K8s）: %v", err)
		return k8sAppliers{}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		log.Printf("K8s 数据面: manager 启动（Workload+DataService Reconciler 集群级 watch，数据面 ns 按租户派生 paas-<tenant>）")
		if err := mgr.Start(ctx); err != nil {
			log.Printf("K8s 数据面: manager 退出: %v", err)
		}
	}()
	// clientset 供 builder.K8sJob 创建构建 Job + 取 Pod 日志（controller-runtime typed client
	// 不支持 pods/log 子资源）。构造失败不阻塞 workload/dataservice（applier 用 mgr.GetClient）。
	var clientset kubernetes.Interface
	if cs, err := kubernetes.NewForConfig(cfg); err != nil {
		log.Printf("K8s 数据面: 构造 clientset 失败（builder K8s 模式将降级）: %v", err)
	} else {
		clientset = cs
	}
	return k8sAppliers{
		workload:     controller.NewK8sApplier(mgr.GetClient()),
		dataservice:  controller.NewDataServiceK8sApplier(mgr.GetClient()),
		dsRestarter:  controller.NewDSRestarter(mgr.GetClient()),
		clientset:    clientset,
		wlReconciler: wlReconciler,
		client:       mgr.GetClient(),
	}, cancel
}

// ingressClassFromEnv 解析应用域名->自动 Ingress 的 ingressClassName。
// env PAAS_INGRESS_CLASS（helm values ingress.className）覆盖，默认 hermes（dev 集群 ingress controller）。
// 空串显式表示不设 ingressClassName（集群默认 IngressController 接管）。
func ingressClassFromEnv() string {
	if v := os.Getenv("PAAS_INGRESS_CLASS"); v != "" {
		return v
	}
	return "hermes"
}

// clusterIDFromEnv 集群标识（env PAAS_CLUSTER_ID，helm values core.clusterId）。
// 默认 "default"（单集群）；多区部署时区分。注入 Pod env 供应用 OTel 资源属性 paas.cluster 用。
func clusterIDFromEnv() string {
	if v := os.Getenv("PAAS_CLUSTER_ID"); v != "" {
		return v
	}
	return "default"
}
