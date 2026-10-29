// Package scoped 租户侧受控取证（spec §6.2/§6.3）：AuthorizedTarget 服务端解析 +
// ScopedToolset 工具白名单。与平台诊断工具（internal/diagnose/tools，跨全 ns +
// 任意 PromQL/LogQL）的根本差异：用户请求只接受业务 ID，namespace/查询语句全部
// 服务端构造；工具参数不含任何可指定作用域的字段。
package scoped

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aitoys/paas/internal/controller"
	"github.com/aitoys/paas/internal/diagnose"
	"github.com/aitoys/paas/pkg/tenant"
)

// ErrUnsupportedTarget 目标类型暂不支持（spec §3.2：Job/CronJob/外部实例/数据服务
// 明确不支持并给出原因，不套用 Deployment 工具谎报「目标不存在」）。
var ErrUnsupportedTarget = errors.New("unsupported target")

// WorkloadReader workload 仓储窄接口（按 ctx 租户过滤；跨租户 404 不泄漏）。
type WorkloadReader interface {
	Get(ctx context.Context, id string) (WorkloadInfo, error)
}

// WorkloadInfo scoped 层所需的最小 workload 投影（装配层从 internal/workload 适配，
// 避免包耦合）。
type WorkloadInfo struct {
	ID       string
	TenantID string // 双保险断言用（仓储已按 ctx 过滤）
	AppID    string
	EnvID    string
	LaneID   string
	Service  string
	Type     string // service / job / cronjob
	Name     string
}

// SpecReader K8s deployment 读取窄接口（复用 controller.K8sRuntimeReader 的方法面）。
type SpecReader interface {
	DeploymentSpec(ctx context.Context, ns, name string) (*controller.DeploymentSpecInfo, error)
}

// EnvNamer 环境名解析（展示用；失败不阻塞——空名如实展示）。
type EnvNamer interface {
	EnvName(ctx context.Context, envID string) string
}

// TargetResolver 解析 AuthorizedTarget（spec §6.2 五步的落地：身份→workload 归属→
// ns/部署名→UID 快照→生成不可变目标）。
type TargetResolver struct {
	Workloads WorkloadReader
	K8s       SpecReader
	Envs      EnvNamer
}

// TenantNamespace 租户 ns 规则：复用 pkg/tenant.Namespace 真源（tid 已含 t- 前缀，
// 自己拼 "paas-t-" 会产出 paas-t-t-acme 双前缀——集群 e2e 实测踩坑）。
func TenantNamespace(tenantID string) string { return tenant.Namespace(tenantID) }

// Resolve 解析并校验目标。workload 不存在/跨租户由 Workloads.Get 返错透传（404 不泄漏）；
// 非法类型返 ErrUnsupportedTarget（带类型原因由调用方组装）。
func (r *TargetResolver) Resolve(ctx context.Context, tenantID, workloadID string) (diagnose.AuthorizedTarget, error) {
	w, err := r.Workloads.Get(ctx, workloadID)
	if err != nil {
		return diagnose.AuthorizedTarget{}, err
	}
	if w.TenantID != "" && w.TenantID != tenantID {
		// 双保险：仓储已按 ctx 过滤，这里再断言一次（fail-closed）。
		return diagnose.AuthorizedTarget{}, fmt.Errorf("workload not found")
	}
	if w.Type != "service" {
		return diagnose.AuthorizedTarget{}, fmt.Errorf("%w: 类型 %s 暂不支持（首期仅 service/Deployment；Job/CronJob、外部实例、数据服务由后续适配器扩展）", ErrUnsupportedTarget, w.Type)
	}
	ns := TenantNamespace(tenantID)
	t := diagnose.AuthorizedTarget{
		Type:         diagnose.TargetTypeWorkload,
		WorkloadID:   w.ID,
		WorkloadName: w.Name,
		AppID:        w.AppID,
		EnvID:        w.EnvID,
		LaneID:       w.LaneID,
		Service:      w.Service,
		Namespace:    ns,
		ResolvedAt:   time.Now().UTC(),
	}
	if r.Envs != nil {
		t.EnvName = r.Envs.EnvName(ctx, w.EnvID)
	}
	// Deployment UID 快照：重新发布替换 UID 时执行器停止（spec §6.2）。
	// 读取失败不阻塞创建（deployment 可能尚未拉起——如实留给工具四态呈现）。
	if r.K8s != nil {
		if spec, err := r.K8s.DeploymentSpec(ctx, ns, w.ID); err == nil && spec != nil {
			t.DeploymentUID = spec.UID
		}
	}
	return t, nil
}

// ValidateUID run 执行前校验 Deployment UID 未被替换（UID 非空时）。
// 返回 false = Deployment 已被替换，任务应停止并提示重选目标。
func ValidateUID(ctx context.Context, k8s SpecReader, t diagnose.AuthorizedTarget) bool {
	if t.DeploymentUID == "" || k8s == nil {
		return true // 创建时无快照或读取器缺失：不阻塞（工具查询会如实呈现现状）
	}
	spec, err := k8s.DeploymentSpec(ctx, t.Namespace, t.WorkloadID)
	if err != nil || spec == nil {
		return true // 读不到不判死（网络错误≠UID 替换）
	}
	return spec.UID == t.DeploymentUID
}

// ValidateFocus 验证实例 UID 和容器，聚焦信息不能仅作为未经验证的自然语言提示。
func (r *TargetResolver) ValidateFocus(ctx context.Context, t diagnose.AuthorizedTarget, pod, uid, container string) error {
	if pod == "" && uid == "" && container == "" {
		return nil
	}
	if pod == "" || uid == "" {
		return fmt.Errorf("聚焦实例必须提供 Pod 名和 UID")
	}
	reader, ok := r.K8s.(K8sReader)
	if !ok || nilInterface(reader) {
		return fmt.Errorf("实例数据不可用")
	}
	spec, err := reader.DeploymentSpec(ctx, t.Namespace, t.WorkloadID)
	if err != nil || spec == nil {
		return fmt.Errorf("目标工作负载不可读")
	}
	pods, err := reader.PodsBySelector(ctx, t.Namespace, spec.Selector)
	if err != nil {
		return fmt.Errorf("实例不可读")
	}
	for _, p := range pods {
		if p.Name == pod && p.UID == uid {
			if container == "" {
				return nil
			}
			for _, c := range p.Containers {
				if c.Name == container {
					return nil
				}
			}
		}
	}
	return fmt.Errorf("实例已变化或不属于目标，请重新选择")
}
