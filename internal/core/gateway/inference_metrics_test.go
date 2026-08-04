package gateway

import (
	"strings"
	"testing"

	"github.com/prometheus/common/expfmt"

	"github.com/aitoys/paas/internal/metrics"
)

// dumpMetrics 拉取 Registry 全部指标，做文本字符串断言。
func dumpMetrics(t *testing.T, reg *metrics.Registry) string {
	t.Helper()
	got, err := reg.Prometheus().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var sb strings.Builder
	enc := expfmt.NewEncoder(&sb, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, fam := range got {
		if err := enc.Encode(fam); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}
	return sb.String()
}

// TestRecordInferenceFromServeMetrics 验证 Meter.recordInferenceMetrics 正确将
// (tenant, model, status, tokens, duration) 记入 InferenceMetrics。
// 模拟 serveStream 末尾的记录调用（直接单测方法，避免 SSE 集成复杂度）。
func TestRecordInferenceFromServeMetrics(t *testing.T) {
	reg := metrics.NewRegistry()
	m := &Meter{Inf: reg.Inference()}

	// 模拟 serveStream 末尾的记录调用（直接测 Meter.recordInferenceMetrics）。
	m.recordInferenceMetrics("t-acme", "glm-5.2", "success", 30, 0.42)

	out := dumpMetrics(t, reg)
	if !strings.Contains(out, `paas_inference_requests_total{model="glm-5.2",status="success",tenant="t-acme"} 1`) {
		t.Errorf("缺 requests:\n%s", out)
	}
	if !strings.Contains(out, `paas_inference_duration_seconds_sum{model="glm-5.2",tenant="t-acme"} 0.42`) {
		t.Errorf("缺 duration:\n%s", out)
	}
	// 粗估策略：合并 tokens 全计 completion，prompt=0。
	if !strings.Contains(out, `paas_inference_tokens_total{direction="completion",model="glm-5.2",tenant="t-acme"} 30`) {
		t.Errorf("缺 completion tokens:\n%s", out)
	}
	if !strings.Contains(out, `paas_inference_tokens_total{direction="prompt",model="glm-5.2",tenant="t-acme"} 0`) {
		t.Errorf("缺 prompt tokens:\n%s", out)
	}
}

// TestRecordInferenceNilSafe 验证 nil Inf / nil Meter 不 panic。
func TestRecordInferenceNilSafe(t *testing.T) {
	var nilInf *Meter
	nilInf.recordInferenceMetrics("t", "m", "success", 1, 0.1) // 不应 panic

	m := &Meter{}
	m.recordInferenceMetrics("t", "m", "success", 1, 0.1) // Inf=nil 不应 panic
}
