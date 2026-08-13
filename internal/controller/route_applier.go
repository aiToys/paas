package controller

import (
	"context"
	"fmt"
	"log"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/aitoys/paas/internal/governance"
	"github.com/aitoys/paas/pkg/labels"
	"github.com/aitoys/paas/pkg/tenant"
)

// K8sRouteApplier 把 governance.Route 投影为标准 networkingv1.Ingress（hermes/nginx 等标准
// IngressController 通用，无专属 annotation 依赖）。实现 governance.RouteApplier。
//
// 与 K8sApplier（workload CRD）区别：Route 不是 K8s 资源（无 CRD/无 OwnerRef），按 (tenant ns, host)
// 聚合成单条多 path Ingress——多 Route 共享同 host 共用一条 Ingress，label paas.aitoys/route-host 标识。
// 每次 Route 变更重建该 host 的聚合 Ingress（ListRoutes 同 host 全部 enabled Route → 多 path）。
//
// 仅下发标准 Ingress 能力（host + path prefix → Service:Port）。Route.Methods（方法过滤）/ StripPath
// （前缀剥离）超出标准 Ingress 表达力，需 ingress controller 专属 annotation，本轮不下发（保互换性）。
type K8sRouteApplier struct {
	client.Client
	Routes       governance.RouteStore  // 重建需 ListRoutes 同 host 全部 Route
	Services     governance.ServiceStore // Route.ServiceID → Service.Name + Port
	IngressClass string                 // PAAS_INGRESS_CLASS（默认 nginx，dev hermes）；空=不设 ingressClassName
}

// NewK8sRouteApplier 创建 applier。RouteStore/ServiceStore 用裸 governance Repository（不持 ApplyRepo，
// 避免循环引用：applier 持裸 store，ApplyRepo 持 applier，装饰后 repo 给 handler）。
func NewK8sRouteApplier(cl client.Client, rs governance.RouteStore, ss governance.ServiceStore, class string) *K8sRouteApplier {
	return &K8sRouteApplier{Client: cl, Routes: rs, Services: ss, IngressClass: class}
}

// Apply 重建 (tenant ns, host) 聚合 Ingress。无 enabled Route → 删整条 Ingress（若有）。
// 服务解析失败（GetService not found）跳过该 path + log（best-effort，不阻断其他 path 重建）。
func (a *K8sRouteApplier) Apply(ctx context.Context, tid, host string) error {
	if tid == "" || host == "" {
		return nil
	}
	if err := EnsureNamespace(ctx, a.Client, tid); err != nil {
		return fmt.Errorf("ensure namespace: %w", err)
	}
	ns := tenant.Namespace(tid)

	routes, err := a.Routes.ListRoutes(ctxWithTid(ctx, tid), "")
	if err != nil {
		return fmt.Errorf("list routes: %w", err)
	}

	// 过滤同 host + enabled Route。
	var paths []networkingv1.HTTPIngressPath
	for _, r := range routes {
		if r.Host != host || !r.Enabled {
			continue
		}
		svc, err := a.Services.GetService(ctxWithTid(ctx, tid), r.ServiceID)
		if err != nil {
			log.Printf("[route-applier] 解析服务失败跳过该 path route=%s service=%s err=%v", r.ID, r.ServiceID, err)
			continue
		}
		pathType := networkingv1.PathTypePrefix
		paths = append(paths, networkingv1.HTTPIngressPath{
			Path:     orDefault(r.Path, "/"),
			PathType: &pathType,
			Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
				Name: svc.Name,
				Port: networkingv1.ServiceBackendPort{Number: clampInt32(svc.Port)},
			}},
		})
	}

	ingName := "route-" + tenant.SanitizeName(host)
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: ingName, Namespace: ns}}

	if len(paths) == 0 {
		// 无剩余 enabled Route：删整条 Ingress（not found 忽略，幂等）。
		return a.deleteIngress(ctx, tid, host)
	}

	_, err = controllerutil.CreateOrUpdate(ctx, a.Client, ing, func() error {
		ing.SetLabels(map[string]string{
			"app.kubernetes.io/managed-by": "paas",
			labels.KeyTenant:               tid,
			labels.KeyRouteHost:            host,
		})
		ing.Spec.Rules = []networkingv1.IngressRule{{
			Host: host,
			IngressRuleValue: networkingv1.IngressRuleValue{
				HTTP: &networkingv1.HTTPIngressRuleValue{Paths: paths},
			},
		}}
		if a.IngressClass != "" {
			ing.Spec.IngressClassName = &a.IngressClass
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("createorupdate ingress: %w", err)
	}
	log.Printf("[route-applier] Apply ns=%s host=%s paths=%d", ns, host, len(paths))
	return nil
}

// Delete 重建 (tenant ns, host) 聚合 Ingress（Route 删后调，剩 path 留 / 无剩删整条）。
// 语义等同 Apply 的「按当前剩余 Route 重建」——单条 Route 删后，该 host 可能仍剩其他 Route，
// 直接删整条会误伤；重新 Apply（ListRoutes 剩余 → 有 path 则重建，无则删）才正确。
func (a *K8sRouteApplier) Delete(ctx context.Context, tid, host string) error {
	return a.Apply(ctx, tid, host)
}

// deleteIngress 直接删整条 Ingress（not found 忽略，幂等）。Apply 无剩余 path 时调用。
func (a *K8sRouteApplier) deleteIngress(ctx context.Context, tid, host string) error {
	ns := tenant.Namespace(tid)
	ingName := "route-" + tenant.SanitizeName(host)
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: ingName, Namespace: ns}}
	if err := client.IgnoreNotFound(a.Client.Delete(ctx, ing)); err != nil {
		return fmt.Errorf("delete ingress: %w", err)
	}
	log.Printf("[route-applier] Delete ns=%s host=%s（无剩余 enabled route）", ns, host)
	return nil
}

// orDefault：path 空兜底 "/"（K8s Ingress path 不能空）。
func orDefault(p, def string) string {
	if p == "" {
		return def
	}
	return p
}

// ctxWithTid 把 tid 注入 ctx（RouteStore/ServiceStore 从 ctx 取 tenant 过滤）。
// applier 调用时 ctx 可能无 tenant（如来自 ApplyRepo 的请求 ctx 已有 tenant，但保险注入一致）。
func ctxWithTid(ctx context.Context, tid string) context.Context {
	return tenant.WithTenant(ctx, tid)
}
