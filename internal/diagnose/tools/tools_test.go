package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/controller"
	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/internal/observability/real"
	"github.com/aitoys/paas/internal/workload"
)

// fakeK8s 可编程 K8s 依赖（按 deployment 名返回 spec/pods/live）。
type fakeK8s struct {
	specs      map[string]*controller.DeploymentSpecInfo // key: ns/name
	pods       []workload.RuntimeInstance
	specErr    error
	liveResult bool
}

func (f *fakeK8s) DeploymentSpec(_ context.Context, ns, name string) (*controller.DeploymentSpecInfo, error) {
	if f.specErr != nil {
		return nil, f.specErr
	}
	if s, ok := f.specs[ns+"/"+name]; ok {
		return s, nil
	}
	return nil, nil
}

func (f *fakeK8s) PodsBySelector(context.Context, string, map[string]string) ([]workload.RuntimeInstance, error) {
	return f.pods, nil
}

func (f *fakeK8s) NamespaceEvents(_ context.Context, _ string, _ string, _ int, _ bool) ([]controller.DiagEvent, error) {
	return nil, nil
}

func (f *fakeK8s) RawPodLogs(_ context.Context, _, _, _ string, _ int64, _ bool, _ string) (string, error) {
	return "", nil
}

func (f *fakeK8s) PodWorkloadRef(ctx context.Context, ns, _ string) (*controller.DeploymentSpecInfo, error) {
	return f.DeploymentSpec(ctx, ns, "dep-1")
}

func (f *fakeK8s) HasLivePods(context.Context, string, string) (bool, error) {
	return f.liveResult, nil
}

// fakeProm / fakeLoki / fakeJaeger 可编程观测依赖。
type fakeProm struct {
	samples       []real.RawSample
	err           error
	capturedQuery string
}

func (f *fakeProm) QueryInstant(_ context.Context, q string) ([]real.RawSample, error) {
	f.capturedQuery = q
	return f.samples, f.err
}

func (f *fakeProm) QueryRange(context.Context, string, time.Time, time.Time, int) ([]real.RawSeries, error) {
	return nil, nil
}

type fakeLoki struct {
	lines []real.RawLogLine
	err   error
}

func (f *fakeLoki) RawLoki(context.Context, string, int, int) ([]real.RawLogLine, error) {
	return f.lines, f.err
}

type fakeJaeger struct {
	traces []observability.Trace
	err    error
}

func (f *fakeJaeger) QueryTracesRaw(context.Context, string, string, int64, int, int) ([]observability.Trace, error) {
	return f.traces, f.err
}

// statusOf 解析工具 JSON 输出的 status 字段。
func statusOf(t *testing.T, out string) string {
	t.Helper()
	var r ToolResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("工具输出应可解析为 ToolResult 信封: %v (out=%s)", err, out)
	}
	return r.Status
}

// callStatus 调 invoker 并取 status（invoker 恒返 nil error，信封内含错误态）。
func callStatus(t *testing.T, inv func(context.Context, string) (string, error), args string) string {
	t.Helper()
	out, err := inv(context.Background(), args)
	if err != nil {
		t.Fatalf("invoker 不应返 error（错误走信封）: %v", err)
	}
	return statusOf(t, out)
}

func testSpec() *controller.DeploymentSpecInfo {
	return &controller.DeploymentSpecInfo{
		Namespace: "paas", Name: "paas-core", Replicas: 1,
		Selector: map[string]string{"app.kubernetes.io/name": "paas-core"},
		Containers: []controller.ContainerSpecInfo{
			{Name: "core", Image: "paas:dev", Limits: controller.ResourceSpecInfo{CPU: "500m", Memory: "512Mi"}},
		},
	}
}

func readyPod() workload.RuntimeInstance {
	return workload.RuntimeInstance{
		Instance:   workload.Instance{Name: "paas-core-abc-def", Status: "Running", Ready: "1/1"},
		Conditions: []workload.PodCondition{{Type: "Ready", Status: "True"}},
	}
}

// —— T1/T8（坑1：selector 反查语义由 DeploymentSpec+PodsBySelector 联动承载）——

func TestWorkloadStatusFourStates(t *testing.T) {
	k8s := &fakeK8s{specs: map[string]*controller.DeploymentSpecInfo{}, pods: []workload.RuntimeInstance{readyPod()}}
	k8s.specs["paas/paas-core"] = testSpec()
	ts := New(k8s, nil, nil, nil)
	lookup := func(name string) func(context.Context, string) (string, error) {
		_, inv, ok := ts.Lookup(name)
		if !ok {
			t.Fatalf("%s 应已注册", name)
		}
		return inv
	}

	// ok：deployment 在 + pod 在。
	if s := callStatus(t, lookup("get_workload_status"), `{"ns":"paas","workload":"paas-core"}`); s != StatusOK {
		t.Fatalf("正常路径应 ok, got %s", s)
	}
	// missing：deployment 不存在。
	if s := callStatus(t, lookup("get_workload_status"), `{"ns":"ghost","workload":"none"}`); s != StatusMissing {
		t.Fatalf("deployment 不存在应 missing, got %s", s)
	}
	// empty：deployment 在但无 pod。
	k8s.pods = nil
	if s := callStatus(t, lookup("get_workload_status"), `{"ns":"paas","workload":"paas-core"}`); s != StatusEmpty {
		t.Fatalf("无 pod 应 empty, got %s", s)
	}
	// error：上游错。
	k8s.specErr = errors.New("apiserver 不可达")
	if s := callStatus(t, lookup("get_workload_status"), `{"ns":"paas","workload":"paas-core"}`); s != StatusError {
		t.Fatalf("上游错应 error, got %s", s)
	}
	// error：参数缺。
	if s := callStatus(t, lookup("get_workload_status"), `{}`); s != StatusError {
		t.Fatalf("缺参应 error, got %s", s)
	}
}

// —— T4（坑3：empty vs missing 按 K8s 活体分流）——

func TestLokiEmptyVsMissing(t *testing.T) {
	k8s := &fakeK8s{specs: map[string]*controller.DeploymentSpecInfo{}}
	k8s.specs["paas/paas-core"] = testSpec()
	loki := &fakeLoki{lines: nil} // Loki 查询成功但空
	ts := New(k8s, nil, loki, nil)
	_, inv, _ := ts.Lookup("query_loki")

	// ns 有活体 pod 而 Loki 空 → missing（源不覆盖）。
	k8s.liveResult = true
	if s := callStatus(t, inv, `{"ns":"paas","app":"paas-core"}`); s != StatusMissing {
		t.Fatalf("有活体 pod 而无日志应 missing（源不覆盖）, got %s", s)
	}
	// 无活体 pod → empty。
	k8s.liveResult = false
	if s := callStatus(t, inv, `{"ns":"paas","app":"paas-core"}`); s != StatusEmpty {
		t.Fatalf("无活体 pod 无日志应 empty, got %s", s)
	}
	// Loki 报错 → error。
	loki.err = errors.New("connection refused")
	if s := callStatus(t, inv, `{"ns":"paas"}`); s != StatusError {
		t.Fatalf("Loki 错应 error, got %s", s)
	}
}

// —— T6（坑2：image!="" 内建 + specLimits 从 deploy spec 反查）——

func TestPodMetricsPromQLAndSpecLimits(t *testing.T) {
	k8s := &fakeK8s{specs: map[string]*controller.DeploymentSpecInfo{}}
	spec := testSpec()
	spec.Name = "dep-1" // PodWorkloadRef 反查目标：ns/dep-1
	k8s.specs["paas/dep-1"] = spec
	prom := &fakeProm{samples: []real.RawSample{{Metric: map[string]string{}, Value: 0.15}}}
	ts := New(k8s, prom, nil, nil)
	_, inv, _ := ts.Lookup("get_pod_metrics")

	out, _ := inv(context.Background(), `{"ns":"paas","pod":"dep-1-x-y"}`)
	var env struct {
		Status string `json:"status"`
		Data   struct {
			SpecLimits []controller.ContainerSpecInfo `json:"specLimits"`
			Deployment string                         `json:"deployment"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Status != StatusOK {
		t.Fatalf("应 ok, got %s: %s", env.Status, out)
	}
	// 坑2：PromQL 必须内建 image!=""（排 sandbox）。
	if !strings.Contains(prom.capturedQuery, `image!=""`) {
		t.Fatalf("PromQL 应内建 image!=\"\" 过滤, got %s", prom.capturedQuery)
	}
	// 坑2：limit 真源 = deploy spec（经 PodWorkloadRef 反查）。
	if len(env.Data.SpecLimits) == 0 || env.Data.SpecLimits[0].Limits.Memory != "512Mi" {
		t.Fatalf("specLimits 应来自 deploy spec（512Mi）, got %+v", env.Data.SpecLimits)
	}
	if env.Data.Deployment != "dep-1" {
		t.Fatalf("应反查到 deployment 名 dep-1, got %s", env.Data.Deployment)
	}
}

// —— T7（坑4：活体校验 → 历史 trace 标注）——

func TestJaegerLiveVsHistorical(t *testing.T) {
	k8s := &fakeK8s{specs: map[string]*controller.DeploymentSpecInfo{}, liveResult: false}
	jaeger := &fakeJaeger{traces: []observability.Trace{{ID: "t1", Status: "success"}}}
	ts := New(k8s, nil, nil, jaeger)
	_, inv, _ := ts.Lookup("query_jaeger_traces")

	// 无活体 + 有 trace → ok 但 note 标注「历史 trace」。
	out, _ := inv(context.Background(), `{"service":"shop","ns":"paas-shop"}`)
	var env ToolResult
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Status != StatusOK || !strings.Contains(env.Note, "历史 trace") {
		t.Fatalf("无活体应有历史 trace 警告, got status=%s note=%s", env.Status, env.Note)
	}
	// 无活体 + 无 trace → empty。
	jaeger.traces = nil
	if s := callStatus(t, inv, `{"service":"shop","ns":"paas-shop"}`); s != StatusEmpty {
		t.Fatalf("无 trace 应 empty, got %s", s)
	}
	// Jaeger 错 → error。
	jaeger.err = errors.New("jaeger down")
	if s := callStatus(t, inv, `{"service":"shop"}`); s != StatusError {
		t.Fatalf("Jaeger 错应 error, got %s", s)
	}
}

// —— T2（坑3：事件 TTL → empty 语义注释）——

func TestEventsEmptyNote(t *testing.T) {
	ts := New(&fakeK8s{specs: map[string]*controller.DeploymentSpecInfo{}}, nil, nil, nil)
	_, inv, _ := ts.Lookup("get_k8s_events")
	out, _ := inv(context.Background(), `{"ns":"paas"}`)
	var env ToolResult
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatal(err)
	}
	if env.Status != StatusEmpty || !strings.Contains(env.Note, "TTL") {
		t.Fatalf("事件空应 empty + TTL 说明, got status=%s note=%s", env.Status, env.Note)
	}
}

// —— 数据源未接入 → error 态（独立信号，非 missing）——

func TestSourcesNotConfigured(t *testing.T) {
	ts := New(&fakeK8s{specs: map[string]*controller.DeploymentSpecInfo{}}, nil, nil, nil)
	for _, h := range []string{"query_loki", "query_prometheus", "get_pod_metrics", "query_jaeger_traces"} {
		_, inv, _ := ts.Lookup(h)
		if s := callStatus(t, inv, `{"ns":"a","pod":"p","service":"s","promql":"up"}`); s != StatusError {
			t.Fatalf("%s 数据源未接入应 error, got %s", h, s)
		}
	}
}

// Handlers 注册表完整性：8 工具全注册。
func TestRegistryComplete(t *testing.T) {
	ts := New(nil, nil, nil, nil)
	want := []string{
		"get_workload_status", "get_k8s_events", "get_pod_logs", "query_loki",
		"query_prometheus", "get_pod_metrics", "query_jaeger_traces", "get_workload_spec",
	}
	got := ts.Handlers()
	if len(got) != len(want) {
		t.Fatalf("应注册 8 工具, got %d: %v", len(got), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("注册表顺序漂移: [%d] want %s got %s", i, w, got[i])
		}
	}
}
