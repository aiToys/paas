// Package dataplane 提供数据面 SDK 接入 API（/dp/）：把 K8s Endpoints 暴露为
// zeus 兼容的服务发现真源，让数据面 SDK（如 zeus 经 paas-registry 插件）真实发现实例。
//
// 与控制台 /api/（人类 API Key + 控制面 CRUD）正交：/dp/ 面向数据面 SDK 消费，
// 鉴权复用 gateway.APIKeyAuth（dp token = 专用 API Key），Instance 真源 = K8s Endpoints
// （readiness probe 驱动 ready 集合，非 governance 手动注册表）。
package dataplane

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	LaneID   string            `json:"laneId,omitempty"` // 泳道标识（default=基线，其他=feature 泳道），由 Service 名派生（L2 跨泳道降级发现）
	Metadata map[string]string `json:"metadata,omitempty"`
}

// LaneDefault 是基线泳道标识。与 workload.LaneDefault / pipeline.LaneDefault 同值；
// 在 dataplane 本地定义避免反向依赖业务包。
const LaneDefault = "default"

// ServiceInfo 是 /dp/services 返回的服务元信息。
type ServiceInfo struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
}

// EndpointsReader 读 K8s Endpoints 的 ready 实例（数据面服务发现真源）。
// 真源 = K8s Endpoints（readiness probe 驱动 ready 集合），非 governance 手动注册表。
type EndpointsReader interface {
	// Instances 返回某服务的 ready 实例列表。
	//
	// lane 维度驱动跨泳道降级发现（L2）：
	//   - lane 空或 LaneDefault：返 default 基线实例（向后兼容）。
	//   - lane=feature-x：先查 <serviceName>-<lane> 的 Endpoints（feature 泳道），
	//     isNotFound 或无 ready addresses 则降级查 <serviceName>（default 基线）。
	//
	// 多租户隔离：两个候选 Service 名均经同名 Service 的 tenant label 校验归属本租户，
	// 跨租户/不存在统一返空（不泄漏存在性）。
	Instances(ctx context.Context, namespace, serviceName, lane string) ([]Instance, error)
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

func (r *k8sEndpointsReader) Instances(ctx context.Context, namespace, serviceName, lane string) ([]Instance, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		// fail-closed：无租户上下文不返回任何实例（防跨租户泄漏）。
		return nil, nil
	}
	// lane 空/default → 直接查基线（现状，向后兼容）。
	// lane 非 default → 先查 <service>-<lane> 泳道，无/空降级 <service> 基线。
	if lane == "" || lane == LaneDefault {
		return r.fetchInstances(ctx, namespace, serviceName, tid, LaneDefault)
	}
	// feature 泳道：优先查 <service>-<lane>（L1 Service 命名约定；清洗与 BaselineWorkloadName
	// 同款——集成分支 lane 含 / 时 K8s Service 名已清洗为 -，两侧必须一致才能命中）。
	featureName := dns1035Name(serviceName + "-" + lane)
	insts, err := r.fetchInstances(ctx, namespace, featureName, tid, lane)
	if err == nil && len(insts) > 0 {
		return insts, nil
	}
	// 降级基线（feature 泳道无 ready 实例 = 未变更服务走基线，L2 核心语义）。
	return r.fetchInstances(ctx, namespace, serviceName, tid, LaneDefault)
}

// fetchInstances 校验同名 Service 归属本租户后读 Endpoints 转 Instance。
// resolvedLane 为最终命中的泳道（feature-x 或 default），写入 Instance.LaneID。
//
// 多租户隔离：Endpoints 不带 tenant label，先经同名 Service 的 tenant label 校验归属。
// 跨租户或不存在统一返空（不泄漏存在性），与平台 Repository 隔离语义一致。
func (r *k8sEndpointsReader) fetchInstances(ctx context.Context, namespace, serviceName, tid, resolvedLane string) ([]Instance, error) {
	svc, err := r.cs.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		// 服务不存在（含泳道降级候选）返空；其它错误（apiserver 故障）透传，不与 not found 混淆（审计第 6 轮 M3）。
		if errors.Is(err, apierrors.NewNotFound(schema.GroupResource{}, "")) || strings.Contains(err.Error(), "not found") {
			return nil, nil
		}
		return nil, fmt.Errorf("get service: %w", err)
	}
	if svc.Labels[labels.KeyTenant] != tid {
		return nil, nil
	}
	ep, err := r.cs.CoreV1().Endpoints(namespace).Get(ctx, serviceName, metav1.GetOptions{}) //nolint:staticcheck // Endpoints 在 K8s v0.36 仍主流；EndpointSlice 迁移留后续
	if err != nil {
		return nil, fmt.Errorf("get endpoints: %w", err)
	}
	return endpointsToInstances(ep, resolvedLane), nil
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
// resolvedLane 由调用方传入（基于实际命中的 Service 名派生：feature 候选或 default 基线），
// 写入 Instance.LaneID 供 governance 列表/governance 分组消费（Task 4 启用）。
//
//nolint:staticcheck // corev1.Endpoints/Subset 在 K8s v0.36 仍主流；EndpointSlice 迁移留后续
func endpointsToInstances(ep *corev1.Endpoints, resolvedLane string) []Instance {
	if resolvedLane == "" {
		resolvedLane = LaneDefault
	}
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
					LaneID:   resolvedLane,
				})
			}
		}
	}
	return out
}

// dns1035Name 清洗为 K8s Service 名合法字符（与 devops.BaselineWorkloadName 同款规则，
// feature 泳道 Endpoints 查询与 Service 命名两侧对齐；独立实现避免 dataplane→devops import）。
// 首字符数字前缀 n（DNS-1035 首字符须字母）——与 devops 侧 dns1035 语义严格一致（审计第 6 轮 I1）。
func dns1035Name(name string) string {
	var b []byte
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b = append(b, byte(r)) //nolint:gosec // case 已限定 ASCII 区间，rune->byte 无溢出
		case r >= 'A' && r <= 'Z':
			b = append(b, byte(r-'A'+'a')) //nolint:gosec // case 已限定 A-Z 区间，算术结果必在 a-z
		default:
			b = append(b, '-')
		}
	}
	if len(b) > 63 {
		b = b[:63]
	}
	out := strings.Trim(string(b), "-")
	if out != "" && (out[0] < 'a' || out[0] > 'z') {
		out = "n" + out
	}
	return out
}
