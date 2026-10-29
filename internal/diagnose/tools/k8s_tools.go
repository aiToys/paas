// K8s 侧诊断工具（T1/T2/T3/T8，spec impl-spec S3.1–S3.3/S3.8）。
//
// §8 坑1 落地：T1/T8 的 pod 选择一律从 DeploymentSpec.spec.selector 反查真实
// matchLabels，绝不假设 app=<name> 之类的 label 规约（paas-core 的真实 label 是
// app.kubernetes.io/name=paas-core，-l app= 直接选空）。
package tools

import (
	"context"
	"strings"
)

// —— JSON Schema 定义 ——

var schemaNSWorkload = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"ns":       map[string]any{"type": "string", "description": "K8s 命名空间（如 paas、paas-t-acme、observability）"},
		"workload": map[string]any{"type": "string", "description": "工作负载名（Deployment 名，如 paas-core、wl-xxx）"},
	},
	"required": []string{"ns", "workload"},
}

var schemaEvents = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"ns":          map[string]any{"type": "string", "description": "K8s 命名空间"},
		"object":      map[string]any{"type": "string", "description": "可选：按 involvedObject 名过滤（如 pod 名/deployment 名）"},
		"sinceMin":    map[string]any{"type": "integer", "description": "时间窗（分钟），默认 60"},
		"warningOnly": map[string]any{"type": "boolean", "description": "只取 Warning 事件，默认 false"},
	},
	"required": []string{"ns"},
}

var schemaPodLogs = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"ns":        map[string]any{"type": "string", "description": "K8s 命名空间"},
		"pod":       map[string]any{"type": "string", "description": "Pod 名"},
		"container": map[string]any{"type": "string", "description": "容器名（空=默认容器）"},
		"tail":      map[string]any{"type": "integer", "description": "尾部行数，默认 200，上限 2000"},
		"previous":  map[string]any{"type": "boolean", "description": "取上次终止容器日志（崩溃排查），默认 false"},
		"grep":      map[string]any{"type": "string", "description": "服务端逐行关键字过滤（如 error），空=全量"},
	},
	"required": []string{"ns", "pod"},
}

// invokeWorkloadStatus T1：deployment spec（selector 真源）→ 选 pod → 汇总运行状态。
func invokeWorkloadStatus(ctx context.Context, ts *Toolset, argsJSON string) ToolResult {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return errorResult(err.Error())
	}
	ns, wl := args.str("ns"), args.str("workload")
	if ns == "" || wl == "" {
		return errorResult("ns 与 workload 必填")
	}
	spec, err := ts.k8s.DeploymentSpec(ctx, ns, wl)
	if err != nil {
		return errorResult(err.Error())
	}
	if spec == nil {
		return missingResult("Deployment " + ns + "/" + wl + " 不存在（未部署或已删除）")
	}
	pods, err := ts.k8s.PodsBySelector(ctx, ns, spec.Selector) // 坑1：真实 selector
	if err != nil {
		return errorResult(err.Error())
	}
	ready, running := 0, 0
	for _, p := range pods {
		if p.Status == "Running" && p.DeletionTimestamp == nil {
			running++
		}
		// Ready 判定：Ready 条件 True 且非终止（Running≠Ready，SKILL 理念）。
		for _, c := range p.Conditions {
			if c.Type == "Ready" && c.Status == "True" && p.DeletionTimestamp == nil {
				ready++
			}
		}
	}
	data := map[string]any{
		"deployment": map[string]any{
			"namespace": ns, "name": wl, "replicas": spec.Replicas,
			"selector": spec.Selector,
		},
		"summary": map[string]any{"total": len(pods), "running": running, "ready": ready},
		"pods":    pods,
	}
	note := ""
	if len(pods) == 0 {
		note = "Deployment 存在但 selector 未选中任何 Pod（可能正在滚动/缩容为 0/Pod 未调度）"
		return emptyResult(data, note)
	}
	if ready < int(spec.Replicas) {
		note = "就绪数低于期望副本，注意 Pod Running 不等于 Ready"
	}
	return okResult(data, note)
}

// invokeK8sEvents T2：ns 事件查询。TTL 语义显式注入 note（坑3：empty≠无故障）。
func invokeK8sEvents(ctx context.Context, ts *Toolset, argsJSON string) ToolResult {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return errorResult(err.Error())
	}
	ns := args.str("ns")
	if ns == "" {
		return errorResult("ns 必填")
	}
	object := args.str("object")
	sinceMin := args.intOr("sinceMin", 60)
	warningOnly := args.boolOr("warningOnly", false)
	evs, err := ts.k8s.NamespaceEvents(ctx, ns, object, sinceMin, warningOnly)
	if err != nil {
		return errorResult(err.Error())
	}
	if len(evs) == 0 {
		return emptyResult(evs, "窗口内无事件。K8s 事件有 TTL（默认 1h 转储），稳定服务查不到事件是 empty（正常），不代表无故障；如需深挖配 get_pod_logs/query_loki。")
	}
	return okResult(evs, "")
}

// invokePodLogs T3：容器日志（grep 服务端过滤 + previous 崩溃容器）。
func invokePodLogs(ctx context.Context, ts *Toolset, argsJSON string) ToolResult {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return errorResult(err.Error())
	}
	ns, pod := args.str("ns"), args.str("pod")
	if ns == "" || pod == "" {
		return errorResult("ns 与 pod 必填")
	}
	tail := int64(args.intOr("tail", 200))
	logs, err := ts.k8s.RawPodLogs(ctx, ns, pod, args.str("container"), tail, args.boolOr("previous", false), args.str("grep"))
	if err != nil {
		msg := err.Error()
		// apiserver NotFound（pod 不存在/容器未启动）分流 missing，其余（apiserver 不可达等）error。
		if strings.Contains(msg, "not found") || strings.Contains(msg, "不存在") {
			return missingResult("Pod " + ns + "/" + pod + " 或其容器不存在（日志不可读）")
		}
		return errorResult(msg)
	}
	if strings.TrimSpace(logs) == "" {
		return emptyResult(logs, "日志为空（容器刚启动/previous 容器无残留/grep 无命中）")
	}
	return okResult(logs, "")
}

// invokeWorkloadSpec T8：deployment spec 直出（selector + requests/limits 真源）。
func invokeWorkloadSpec(ctx context.Context, ts *Toolset, argsJSON string) ToolResult {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return errorResult(err.Error())
	}
	ns, wl := args.str("ns"), args.str("workload")
	if ns == "" || wl == "" {
		return errorResult("ns 与 workload 必填")
	}
	spec, err := ts.k8s.DeploymentSpec(ctx, ns, wl)
	if err != nil {
		return errorResult(err.Error())
	}
	if spec == nil {
		return missingResult("Deployment " + ns + "/" + wl + " 不存在（未部署或已删除）")
	}
	data := map[string]any{
		"namespace": spec.Namespace, "name": spec.Name, "replicas": spec.Replicas,
		"selector":       spec.Selector,
		"containers":     spec.Containers,
		"initContainers": spec.InitContainers,
	}
	note := ""
	if len(spec.Containers) > 0 && spec.Containers[0].Limits.Memory == "" {
		note = "注意：容器未设置 memory limit（BestEffort 风险，OOM 时被直接杀）"
	}
	return okResult(data, note)
}
