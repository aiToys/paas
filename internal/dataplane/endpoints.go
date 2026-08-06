// Package dataplane 提供数据面 SDK 接入 API（/dp/）：把 K8s Endpoints 暴露为
// zeus 兼容的服务发现真源，让数据面 SDK（如 zeus 经 paas-registry 插件）真实发现实例。
//
// 与控制台 /api/（人类 API Key + 控制面 CRUD）正交：/dp/ 面向数据面 SDK 消费，
// 鉴权复用 gateway.APIKeyAuth（dp token = 专用 API Key），Instance 真源 = K8s Endpoints
// （readiness probe 驱动 ready 集合，非 governance 手动注册表）。
package dataplane

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/aitoys/paas/pkg/labels"
	"github.com/aitoys/paas/pkg/tenant"
)

// Instance 对齐 zeus types.Instance（数据面 SDK 消费格式）。
type Instance struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Cluster  string            `json:"cluster"`  // 本期统一 "default"（泳道路由归后续）
	Protocol string            `json:"protocol"` // "http"
	IP       string            `json:"ip"`
	Port     int32             `json:"port"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// ServiceInfo 是 /dp/services 返回的服务元信息。
type ServiceInfo struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
}

// EndpointsReader 读 K8s Endpoints 的 ready 实例（数据面服务发现真源）。
// 真源 = K8s Endpoints（readiness probe 驱动 ready 集合），非 governance 手动注册表。
type EndpointsReader interface {
	// Instances 返回某 K8s Service（Endpoints 同名）的 ready 实例列表。
	Instances(ctx context.Context, namespace, serviceName string) ([]Instance, error)
	// Services 列某命名空间下平台纳管的服务（带 paas managed-by 标签）。
	Services(ctx context.Context, namespace string) ([]ServiceInfo, error)
}

// NewEndpointsReader 用 clientset 构造 K8s Endpoints reader。cs 为 nil 时返 nil
// （非集群部署降级：/dp/ 仍可工作但 instances 返空，services 走 governance 表）。
func NewEndpointsReader(cs kubernetes.Interface) EndpointsReader {
	if cs == nil {
		return nil
	}
	return &k8sEndpointsReader{cs: cs}
}

type k8sEndpointsReader struct {
	cs kubernetes.Interface
}

func (r *k8sEndpointsReader) Instances(ctx context.Context, namespace, serviceName string) ([]Instance, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		// fail-closed：无租户上下文不返回任何实例（防跨租户泄漏）。
		return nil, nil
	}
	// 多租户隔离：Endpoints 不带 tenant label，先经同名 Service 校验归属本租户。
	// 跨租户或不存在统一返空（不泄漏存在性），与平台 Repository 隔离语义一致。
	svc, err := r.cs.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil || svc.Labels[labels.KeyTenant] != tid {
		return nil, nil
	}
	ep, err := r.cs.CoreV1().Endpoints(namespace).Get(ctx, serviceName, metav1.GetOptions{}) //nolint:staticcheck // Endpoints 在 K8s v0.36 仍主流；EndpointSlice 迁移留后续
	if err != nil {
		return nil, fmt.Errorf("get endpoints: %w", err)
	}
	return endpointsToInstances(ep), nil
}

func (r *k8sEndpointsReader) Services(ctx context.Context, namespace string) ([]ServiceInfo, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return nil, nil
	}
	// 多租户隔离：label selector 限定本租户的 PaaS 纳管 Service。
	svcs, err := r.cs.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/managed-by=paas,paas.aitoys/tenant=" + tid,
	})
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	out := make([]ServiceInfo, 0, len(svcs.Items))
	for _, s := range svcs.Items {
		if len(s.Spec.Ports) == 0 {
			continue
		}
		out = append(out, ServiceInfo{Name: s.Name, Protocol: "http"})
	}
	return out, nil
}

// endpointsToInstances 把 Endpoints 转为 Instance 列表（仅 ready address）。
// NotReadyAddresses 排除（readiness probe 未通过 = 不进发现）。
//
//nolint:staticcheck // corev1.Endpoints/Subset 在 K8s v0.36 仍主流；EndpointSlice 迁移留后续
func endpointsToInstances(ep *corev1.Endpoints) []Instance {
	out := make([]Instance, 0)
	for _, sub := range ep.Subsets {
		for _, addr := range sub.Addresses {
			for _, p := range sub.Ports {
				out = append(out, Instance{
					ID:       fmt.Sprintf("%s-%s-%d", ep.Name, addr.IP, p.Port),
					Name:     ep.Name,
					Cluster:  "default",
					Protocol: "http",
					IP:       addr.IP,
					Port:     p.Port,
				})
			}
		}
	}
	return out
}
