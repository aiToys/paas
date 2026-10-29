package scoped

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/controller"
	"github.com/aitoys/paas/internal/observability/real"
	"github.com/aitoys/paas/internal/workload"
)

type reviewK8s struct{}

func (reviewK8s) DeploymentSpec(context.Context, string, string) (*controller.DeploymentSpecInfo, error) {
	return &controller.DeploymentSpecInfo{Name: "wl-1", UID: "uid-1", Selector: map[string]string{"app": "review"}}, nil
}
func (reviewK8s) PodsBySelector(context.Context, string, map[string]string) ([]workload.RuntimeInstance, error) {
	return []workload.RuntimeInstance{{Instance: workload.Instance{Name: "wl-1-pod"}, UID: "pod-uid"}}, nil
}
func (reviewK8s) NamespaceEvents(context.Context, string, string, int, bool) ([]controller.DiagEvent, error) {
	return nil, nil
}
func (reviewK8s) RawPodLogs(context.Context, string, string, string, int64, bool, string) (string, error) {
	return "fatal: review-database-authentication-failed", nil
}
func (reviewK8s) HasLivePods(context.Context, string, string) (bool, error) { return true, nil }

type reviewEmptyProm struct{}

func (reviewEmptyProm) QueryInstant(context.Context, string) ([]real.RawSample, error) {
	return nil, nil
}

func TestRegressionModelMustReceiveLogEvidence(t *testing.T) {
	ts := New(testTarget(), 30, reviewK8s{}, nil, nil, nil)
	_, invoke, _ := ts.Lookup("get_pod_logs")
	out, e := invoke(context.Background(), `{}`)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(ts.Ledger().Snapshot()[0].Snippet, "review-database-authentication-failed") {
		t.Fatal("fixture missing")
	}
	if !strings.Contains(out, "review-database-authentication-failed") {
		t.Fatalf("模型工具结果缺失实际日志，只有计数：%s", out)
	}
}

func TestRegressionEmptyMetricsMustNotBeZero(t *testing.T) {
	ts := New(testTarget(), 30, reviewK8s{}, reviewEmptyProm{}, nil, nil)
	_, invoke, _ := ts.Lookup("get_pod_metrics")
	out, e := invoke(context.Background(), `{}`)
	if e != nil {
		t.Fatal(e)
	}
	var env envelope
	if e = json.Unmarshal([]byte(out), &env); e != nil {
		t.Fatal(e)
	}
	if env.Status == StatusOK {
		t.Fatalf("空指标序列被返回 ok 和 0 值：%s", out)
	}
}

func TestRegressionUnconfiguredLokiMustNotPanic(t *testing.T) {
	// main.go 将 *LogsStore(nil) 转成 LokiQuerier，接口本身不再等于 nil。
	var loki *real.LogsStore
	ts := New(testTarget(), 30, reviewK8s{}, nil, loki, nil)
	_, invoke, _ := ts.Lookup("query_loki")
	defer func() {
		if e := recover(); e != nil {
			t.Fatalf("未配置 Loki 时工具 panic；Worker 无 recover 会退出 core：%v", e)
		}
	}()
	_, _ = invoke(context.Background(), `{}`)
}
