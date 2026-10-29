// Toolset 实现 agent.DiagnosticToolset：8 个内置只读工具的进程内注册表。
//
// 依赖（窄接口，依赖倒置 + 可测试）：
//   - K8sReader：controller.K8sRuntimeReader 的诊断 raw 方法集（S2.1–S2.3）
//   - PromQuerier/LokiQuerier/JaegerQuerier：observability real 的 raw 查询入口（S2.4–S2.6）
//
// 任一为 nil 时对应工具仍注册但调用返 error 态（信封统一，不 panic）。
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aitoys/paas/internal/ai/agent"
	"github.com/aitoys/paas/internal/controller"
	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/internal/observability/real"
	"github.com/aitoys/paas/internal/workload"
)

// K8sReader 诊断所需 K8s 只读面（*controller.K8sRuntimeReader 实现全集）。
type K8sReader interface {
	DeploymentSpec(ctx context.Context, ns, name string) (*controller.DeploymentSpecInfo, error)
	PodsBySelector(ctx context.Context, ns string, matchLabels map[string]string) ([]workload.RuntimeInstance, error)
	NamespaceEvents(ctx context.Context, ns, objectName string, sinceMin int, warningOnly bool) ([]controller.DiagEvent, error)
	RawPodLogs(ctx context.Context, ns, pod, container string, tail int64, previous bool, grep string) (string, error)
	PodWorkloadRef(ctx context.Context, ns, podName string) (*controller.DeploymentSpecInfo, error)
	HasLivePods(ctx context.Context, ns, deployment string) (bool, error)
}

// PromQuerier 诊断所需 Prometheus raw 面。
type PromQuerier interface {
	QueryInstant(ctx context.Context, promql string) ([]real.RawSample, error)
	QueryRange(ctx context.Context, promql string, start, end time.Time, stepSec int) ([]real.RawSeries, error)
}

// LokiQuerier 诊断所需 Loki raw 面。
type LokiQuerier interface {
	RawLoki(ctx context.Context, logql string, sinceMin, limit int) ([]real.RawLogLine, error)
}

// JaegerQuerier 诊断所需 Jaeger raw 面。
type JaegerQuerier interface {
	QueryTracesRaw(ctx context.Context, service, operation string, minDurMs int64, sinceMin, limit int) ([]observability.Trace, error)
}

// 编译期断言：*controller.K8sRuntimeReader 实现 K8sReader 全集。
var _ K8sReader = (*controller.K8sRuntimeReader)(nil)

// Toolset 注册表。handlers 顺序即暴露给 LLM 的顺序（稳定）。
type Toolset struct {
	k8s    K8sReader
	prom   PromQuerier
	loki   LokiQuerier
	jaeger JaegerQuerier
	names  []string
	table  map[string]registryEntry
}

// New 构造工具集。real 适配器可为 nil（未配置对应数据源时该类工具返 error 态，
// 注明「数据源未接入」——比 missing 更准确的独立信号，诊断报告据此标注覆盖缺口）。
func New(k8s K8sReader, prom PromQuerier, loki LokiQuerier, jaeger JaegerQuerier) *Toolset {
	ts := &Toolset{k8s: k8s, prom: prom, loki: loki, jaeger: jaeger, table: map[string]registryEntry{}}
	for _, reg := range registry {
		ts.names = append(ts.names, reg.name)
		ts.table[reg.name] = reg
	}
	return ts
}

// registryEntry 静态注册表项（name + meta + 逻辑分派到 k8s_tools/obs_tools 的实现函数）。
type registryEntry struct {
	name string
	meta agent.BuiltinToolMeta
	fn   func(ctx context.Context, ts *Toolset, argsJSON string) ToolResult
}

// registry 8 工具注册表（T1–T8，spec handoff §4 工具清单）。
var registry = []registryEntry{
	{"get_workload_status", agent.BuiltinToolMeta{Description: "读工作负载运行状态：Deployment 期望副本 + 全部 Pod 实例（状态/就绪/重启/容器级 reason）。selector 从 deployment spec 反查，不假设 label 规约。", Schema: schemaNSWorkload}, invokeWorkloadStatus},
	{"get_k8s_events", agent.BuiltinToolMeta{Description: "查命名空间事件（K8s events，可按对象名过滤，支持只取 Warning）。注意事件有 TTL：窗口内无事件是 empty（正常），不代表无故障。", Schema: schemaEvents}, invokeK8sEvents},
	{"get_pod_logs", agent.BuiltinToolMeta{Description: "读 Pod 容器日志（可指定容器/tail 行数/关键字过滤 grep/上次终止容器 previous）。崩溃排查优先 previous=true。", Schema: schemaPodLogs}, invokePodLogs},
	{"query_loki", agent.BuiltinToolMeta{Description: "查 Loki 日志（按命名空间 + 可选工作负载名构造 LogQL，或直接给完整 logql）。区分「查到无日志 empty」与「日志源不覆盖 missing」。", Schema: schemaLoki}, invokeLoki},
	{"query_prometheus", agent.BuiltinToolMeta{Description: "执行任意 PromQL 即时/范围查询（Prometheus）。cadvisor 指标用 image!=\"\" 排 sandbox 容器。", Schema: schemaPromQL}, invokePrometheus},
	{"get_pod_metrics", agent.BuiltinToolMeta{Description: "读 Pod 实时资源用量（CPU 核数/内存字节，Prometheus cadvisor，内建 image!=\"\" 过滤与 sum 聚合）。limit 一律以 get_workload_spec 的 deploy spec 为准（Prometheus limit 指标不可信），本工具一并返回反查到的 spec limits。", Schema: schemaPodMetrics}, invokePodMetrics},
	{"query_jaeger_traces", agent.BuiltinToolMeta{Description: "查 Jaeger 链路（按 service，可选 operation/最小耗时/时间窗）。给 ns 时先做活体校验：无活体 pod 的 service 只有历史 trace，不可作为当前状态证据。", Schema: schemaJaeger}, invokeJaegerTraces},
	{"get_workload_spec", agent.BuiltinToolMeta{Description: "读工作负载部署规格：selector 真源 + 容器 requests/limits（资源上限以此为准，勿信 Prometheus 的 container_spec_memory_limit_bytes）。", Schema: schemaNSWorkload}, invokeWorkloadSpec},
}

// Handlers 返回全部 handler 名（agent.DiagnosticToolset）。
func (ts *Toolset) Handlers() []string {
	out := make([]string, len(ts.names))
	copy(out, ts.names)
	return out
}

// Lookup 按 handler 名取元信息 + 调用器（agent.DiagnosticToolset）。
func (ts *Toolset) Lookup(handler string) (agent.BuiltinToolMeta, func(context.Context, string) (string, error), bool) {
	e, ok := ts.table[handler]
	if !ok {
		return agent.BuiltinToolMeta{}, nil, false
	}
	invoke := func(ctx context.Context, argsJSON string) (string, error) {
		return e.fn(ctx, ts, argsJSON).JSON(), nil
	}
	return e.meta, invoke, true
}

// compile-time assertion: Toolset 实现 agent.DiagnosticToolset。
var _ agent.DiagnosticToolset = (*Toolset)(nil)

// toolArgs 统一参数解析容器：宽松反序列化（缺字段零值），解析失败由各工具自报 error 态。
type toolArgs map[string]any

func parseArgs(argsJSON string) (toolArgs, error) {
	m := toolArgs{}
	if argsJSON == "" {
		return m, nil
	}
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return nil, fmt.Errorf("参数不是合法 JSON 对象")
	}
	return m, nil
}

func (a toolArgs) str(key string) string {
	s, _ := a[key].(string)
	return s
}

func (a toolArgs) intOr(key string, def int) int {
	switch v := a[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return def
}

func (a toolArgs) boolOr(key string, def bool) bool {
	switch v := a[key].(type) {
	case bool:
		return v
	}
	return def
}
