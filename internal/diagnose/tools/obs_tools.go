// 观测侧诊断工具（T4/T5/T6/T7，spec impl-spec S3.4–S3.7）。
//
// §8 坑落地：
//   - 坑2（Prometheus 过滤陷阱）：T6 的 cadvisor PromQL 内建 image!=""（排 sandbox）
//   - sum 聚合；limit 一律从 PodWorkloadRef 反查的 deploy spec 取，
//     绝不信 container_spec_memory_limit_bytes（部分集群恒 0）。
//   - 坑3（empty vs missing）：T4 结果为空时经 K8s 活体探测分流——ns 内有运行中
//     Pod 而 Loki 无日志 → missing（源不覆盖）；无 Pod → empty。
//   - 坑4（活体 vs 历史）：T7 给 ns 时先 HasLivePods 校验，无活体 pod 明确标注
//     「仅历史 trace」，不冒充当前状态证据。
package tools

import (
	"context"
	"fmt"
	"regexp"
	"time"
)

// —— JSON Schema 定义 ——

var schemaLoki = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"ns":       map[string]any{"type": "string", "description": "K8s 命名空间（构造默认 LogQL 用；与 logql 至少给一个）"},
		"app":      map[string]any{"type": "string", "description": "可选：工作负载名，默认 LogQL 按 pod=~\"<app>-.*\" 限定"},
		"logql":    map[string]any{"type": "string", "description": "可选：完整 LogQL（须以 { 开头的 stream selector；给了则忽略 ns/app 构造）"},
		"sinceMin": map[string]any{"type": "integer", "description": "时间窗（分钟），默认 60"},
	},
}

var schemaPromQL = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"promql":   map[string]any{"type": "string", "description": "完整 PromQL 表达式"},
		"sinceMin": map[string]any{"type": "integer", "description": "给定时用范围查询（默认 30 分钟）；空则即时查询"},
		"stepSec":  map[string]any{"type": "integer", "description": "范围查询步长（秒），默认 30"},
	},
	"required": []string{"promql"},
}

var schemaPodMetrics = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"ns":  map[string]any{"type": "string", "description": "K8s 命名空间"},
		"pod": map[string]any{"type": "string", "description": "Pod 名"},
	},
	"required": []string{"ns", "pod"},
}

var schemaJaeger = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"service":  map[string]any{"type": "string", "description": "OTel service.name（通常 = 工作负载名）"},
		"ns":       map[string]any{"type": "string", "description": "可选：服务所在命名空间（给了则先做活体校验）"},
		"op":       map[string]any{"type": "string", "description": "可选：按 operation 过滤"},
		"minDurMs": map[string]any{"type": "integer", "description": "可选：最小耗时（毫秒）"},
		"sinceMin": map[string]any{"type": "integer", "description": "时间窗（分钟），默认 60"},
	},
	"required": []string{"service"},
}

// invokeLoki T4：Loki 查询。默认 LogQL = {namespace="<ns>"} （app 非空追加 pod 正则）；
// 结果空的活体分流见坑3。
func invokeLoki(ctx context.Context, ts *Toolset, argsJSON string) ToolResult {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return errorResult(err.Error())
	}
	if ts.loki == nil {
		return errorResult("Loki 数据源未接入（PAAS_LOKI_URL 未配置），无日志证据可用")
	}
	ns, app, logql := args.str("ns"), args.str("app"), args.str("logql")
	if logql == "" && ns == "" {
		return errorResult("logql 与 ns 至少给一个")
	}
	sinceMin := args.intOr("sinceMin", 60)
	if logql == "" {
		podRe := ""
		if app != "" {
			podRe = regexp.QuoteMeta(app) + `-.*`
		}
		if podRe != "" {
			logql = fmt.Sprintf(`{namespace=%q,pod=~%q}`, ns, podRe)
		} else {
			logql = fmt.Sprintf(`{namespace=%q}`, ns)
		}
	}
	lines, err := ts.loki.RawLoki(ctx, logql, sinceMin, 500)
	if err != nil {
		return errorResult(err.Error())
	}
	if len(lines) == 0 {
		// 坑3 分流：ns 有活体 Pod 而 Loki 空 → 源不覆盖（missing）；否则 empty。
		if app != "" && ts.k8s != nil {
			if live, lerr := ts.k8s.HasLivePods(ctx, ns, app); lerr == nil && live {
				return missingResult("Loki 查询成功但无日志，而 " + ns + "/" + app + " 有运行中 Pod——日志源大概率不覆盖该命名空间（promtail 未采集此 ns），勿据此判定「无异常日志」")
			}
		}
		return emptyResult(lines, "查询成功但无日志（窗口内无输出 / Pod 刚启动 / 无该 ns 的日志流）")
	}
	return okResult(lines, "")
}

// invokePrometheus T5：任意 PromQL（即时/范围）。语法错/不可达 error；成功无数据 empty。
func invokePrometheus(ctx context.Context, ts *Toolset, argsJSON string) ToolResult {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return errorResult(err.Error())
	}
	if ts.prom == nil {
		return errorResult("Prometheus 数据源未接入（PAAS_PROM_URL 未配置），无指标证据可用")
	}
	promql := args.str("promql")
	if promql == "" {
		return errorResult("promql 必填")
	}
	sinceMin := args.intOr("sinceMin", 0)
	if sinceMin <= 0 {
		samples, err := ts.prom.QueryInstant(ctx, promql)
		if err != nil {
			return errorResult(err.Error())
		}
		if len(samples) == 0 {
			return emptyResult(samples, "查询成功无数据：检查指标名/label 匹配/时间窗（cadvisor 指标需 image!=\"\" 排 sandbox）")
		}
		return okResult(samples, "")
	}
	series, err := ts.prom.QueryRange(ctx, promql, time.Now().Add(-time.Duration(sinceMin)*time.Minute), time.Now(), args.intOr("stepSec", 30))
	if err != nil {
		return errorResult(err.Error())
	}
	if len(series) == 0 {
		return emptyResult(series, "查询成功无序列：检查指标名/label 匹配/时间窗")
	}
	return okResult(series, "")
}

// invokePodMetrics T6：pod 实时用量（坑2：image!="" + sum；limit 从 deploy spec 反查）。
func invokePodMetrics(ctx context.Context, ts *Toolset, argsJSON string) ToolResult {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return errorResult(err.Error())
	}
	if ts.prom == nil {
		return errorResult("Prometheus 数据源未接入（PAAS_PROM_URL 未配置），无指标证据可用")
	}
	ns, pod := args.str("ns"), args.str("pod")
	if ns == "" || pod == "" {
		return errorResult("ns 与 pod 必填")
	}
	// 坑2：container= 精确过滤对部分 cadvisor 指标返空，统一 image!="" + sum 聚合。
	lbl := fmt.Sprintf(`{namespace=%q,pod=%q,image!=""}`, ns, pod)
	cpuQ := fmt.Sprintf("sum(rate(container_cpu_usage_seconds_total%s[5m]))", lbl)
	memQ := fmt.Sprintf("sum(container_memory_working_set_bytes%s)", lbl)
	cpu, cpuErr := ts.prom.QueryInstant(ctx, cpuQ)
	mem, memErr := ts.prom.QueryInstant(ctx, memQ)
	if cpuErr != nil && memErr != nil {
		return errorResult("CPU/内存查询均失败: " + cpuErr.Error())
	}
	data := map[string]any{}
	if cpuErr == nil {
		cpuCores := 0.0
		for _, s := range cpu {
			cpuCores += s.Value
		}
		data["cpuCores"] = cpuCores
	} else {
		data["cpuError"] = cpuErr.Error()
	}
	if memErr == nil {
		memBytes := 0.0
		for _, s := range mem {
			memBytes += s.Value
		}
		data["memWorkingSetBytes"] = memBytes
	} else {
		data["memError"] = memErr.Error()
	}
	// 坑2：limit 真源 = deploy spec（owner 链反查）；Prometheus limit 指标不可信，此处不查。
	if ts.k8s != nil {
		if spec, serr := ts.k8s.PodWorkloadRef(ctx, ns, pod); serr == nil && spec != nil {
			data["specLimits"] = spec.Containers
			data["deployment"] = spec.Name
		}
	}
	if _, hasCPU := data["cpuCores"]; !hasCPU {
		return emptyResult(data, "指标查询无数据（Pod 刚启动/cadvisor 未采集该节点）")
	}
	return okResult(data, "limit 以 specLimits（deployment spec）为准；Prometheus 的 container_spec_memory_limit_bytes 不可信（坑：部分集群恒 0）")
}

// invokeJaegerTraces T7：Jaeger 查询 + 活体校验（坑4）。
func invokeJaegerTraces(ctx context.Context, ts *Toolset, argsJSON string) ToolResult {
	args, err := parseArgs(argsJSON)
	if err != nil {
		return errorResult(err.Error())
	}
	if ts.jaeger == nil {
		return errorResult("Jaeger 数据源未接入（PAAS_JAEGER_URL 未配置），无链路证据可用")
	}
	service := args.str("service")
	if service == "" {
		return errorResult("service 必填")
	}
	ns := args.str("ns")
	// 坑4：先校验活体——非常驻服务（如按需拉起的临时项目）Jaeger 里只有历史 trace。
	live := true
	liveChecked := false
	if ns != "" && ts.k8s != nil {
		live, err = ts.k8s.HasLivePods(ctx, ns, service)
		if err != nil {
			live = true // 校验失败不阻塞查询，标注未确认
		} else {
			liveChecked = true
		}
	}
	traces, qerr := ts.jaeger.QueryTracesRaw(ctx, service, args.str("op"), int64(args.intOr("minDurMs", 0)), args.intOr("sinceMin", 60), 20)
	if qerr != nil {
		return errorResult(qerr.Error())
	}
	if len(traces) == 0 {
		if liveChecked && !live {
			return emptyResult(traces, ns+"/"+service+" 无活体 Pod 且窗口内无 trace（临时项目？历史 trace 已过期或超出时间窗）")
		}
		return emptyResult(traces, "窗口内无 trace（服务未上报链路/时间窗不含故障时刻/OTel 未接入）")
	}
	note := ""
	if liveChecked && !live {
		note = "⚠ " + ns + "/" + service + " 当前无活体 Pod：以下仅为历史 trace，不可作为当前运行状态证据"
	} else if !liveChecked {
		note = "未提供 ns，未做活体校验（历史 trace 与当前状态可能脱节）"
	}
	return okResult(traces, note)
}
