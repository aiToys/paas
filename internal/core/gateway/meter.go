package gateway

import (
	"log"
	"sync"

	"github.com/aitoys/paas/internal/metrics"
)

// Meter 记录 Token 用量。本切片仅 log + 内存累计；Plan 2 接 PG/ClickHouse。
type Meter struct {
	mu    sync.Mutex
	total int
	// OnTokens 用量回写钩子（main.go 注入 billing.IncUsage，P3-2 计量采集）；nil 则不回写。
	// appID 非空 = 应用级 Key 归因（强制计费维度）；user 是 OpenAI 兼容软标签（agent 细分，可空）。
	OnTokens func(tenantID, appID, user string, tokens int)
	// Inf 是 Prometheus 推理指标记录器（main.go 注入）；nil 则不记。
	Inf *metrics.InferenceMetrics
}

// recordInferenceMetrics 记录推理 Prometheus 指标（成功/失败统一入口）。
// nil Meter / nil Inf 安全（防御未注入场景）。
//
// promptTokens/completionTokens 当前合并估算（stream 按 rune 粗计），
// 全计为 completion，prompt=0；后续接上游 usage 精确拆分时改签名补 prompt 参数。
func (m *Meter) recordInferenceMetrics(tenant, model, status string, tokens int, durationSec float64) {
	if m == nil || m.Inf == nil {
		return
	}
	// 粗估：合并 tokens 全计为 completion（无上游 usage 拆分）；prompt 侧后续接 usage 时补。
	m.Inf.RecordInference(tenant, model, status, 0, tokens, durationSec)
}

// Record 记录一次请求的 token 用量，并可选回写计费用量。
// appID 非空时用量归因到应用（模型推理计费）；user 是软标签（agent 细分，可空，仅看板聚合）。
func (m *Meter) Record(tenantID, appID, model, user string, tokens int) {
	m.mu.Lock()
	m.total += tokens
	m.mu.Unlock()
	log.Printf("[meter] tenant=%s app=%s model=%s user=%s tokens=%d", tenantID, appID, model, user, tokens)
	if m.OnTokens != nil && tenantID != "" {
		m.OnTokens(tenantID, appID, user, tokens)
	}
}

// Count 返回累计 token（测试用）。
func (m *Meter) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.total
}
