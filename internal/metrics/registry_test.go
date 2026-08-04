package metrics

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/prometheus/common/expfmt"
)

func TestNewRegistryRegistersAllMetrics(t *testing.T) {
	r := NewRegistry()
	// Prometheus Registry.Gather 默认不输出无样本的 MetricFamily，
	// 故先各打一个样本再 gather，验证 5 个指标均已注册且可采集。
	r.httpReqs.WithLabelValues("GET", "/x", "200").Inc()
	r.httpDuration.WithLabelValues("GET", "/x").Observe(0.01)
	r.Inference().RecordInference("t-acme", "m", "success", 1, 1, 0.1)

	gathered, err := r.Prometheus().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	names := map[string]bool{}
	for _, m := range gathered {
		names[m.GetName()] = true
	}
	want := []string{
		"http_requests_total",
		"http_request_duration_seconds",
		"paas_inference_requests_total",
		"paas_inference_tokens_total",
		"paas_inference_duration_seconds",
	}
	for _, w := range want {
		if !names[w] {
			t.Errorf("缺少指标 %s（已有: %v）", w, keys(names))
		}
	}
}

func TestRecordInferenceEmitsTokensByDirection(t *testing.T) {
	r := NewRegistry()
	r.Inference().RecordInference("t-acme", "glm-5.2", "success", 10, 20, 0.5)

	out := dump(t, r)
	if !strings.Contains(out, `paas_inference_tokens_total{direction="completion",model="glm-5.2",tenant="t-acme"} 20`) {
		t.Errorf("缺 completion tokens 行:\n%s", out)
	}
	if !strings.Contains(out, `paas_inference_tokens_total{direction="prompt",model="glm-5.2",tenant="t-acme"} 10`) {
		t.Errorf("缺 prompt tokens 行:\n%s", out)
	}
	if !strings.Contains(out, `paas_inference_requests_total{model="glm-5.2",status="success",tenant="t-acme"} 1`) {
		t.Errorf("缺 requests 行:\n%s", out)
	}
}

// 额外覆盖：nil InferenceMetrics 安全（防御 nil 调用）。
func TestRecordInferenceNilSafe(t *testing.T) {
	var m *InferenceMetrics
	// 不应 panic
	m.RecordInference("t", "m", "ok", 1, 2, 0.1)
}

// 额外覆盖：duration 直方图计数递增。
func TestRecordInferenceDurationObserved(t *testing.T) {
	r := NewRegistry()
	r.Inference().RecordInference("t-acme", "glm-5.2", "success", 1, 1, 0.5)
	if n := testutil.CollectAndCount(r.infDuration); n != 1 {
		t.Errorf("infDuration 指标数 = %d, want 1", n)
	}
}

// keys 收集 map 键切片（测试辅助）。
func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// dump 将 registry 全部指标编码为 text/plain 格式字符串（测试辅助）。
func dump(t *testing.T, r *Registry) string {
	t.Helper()
	mfs, err := r.Prometheus().Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var sb strings.Builder
	enc := expfmt.NewEncoder(&sb, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		if err := enc.Encode(mf); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	return sb.String()
}
