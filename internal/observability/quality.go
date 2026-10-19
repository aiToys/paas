package observability

import (
	"context"
	"time"
)

// 采集质量状态（spec 2026-10-17 专项04 §5「为什么没有数据」五态的探测侧子集：
// ok/empty/failed + 总报告级「未接入」）。
const (
	QualityOK         = "ok"     // 查询成功且有数据
	QualityEmpty      = "empty"  // 查询成功无数据（时间范围/目标/埋点检查，非平台故障）
	QualityFailed     = "failed" // 查询失败（源不可用/查询错误，附脱敏原因）
	QualitySourceProm = "prometheus"
	QualitySourceMock = "memory"
)

// MetricQuality 单指标采集质量探测结果（即时查询，单点轻量）。
type MetricQuality struct {
	Metric       string    `json:"metric"`
	Status       string    `json:"status"`          // ok | empty | failed
	SampleSeries int       `json:"sampleSeries"`    // status=ok 时的 series 数（覆盖面参考）
	Error        string    `json:"error,omitempty"` // failed 时的脱敏原因（不含上游 URL/凭证）
	CheckedAt    time.Time `json:"checkedAt"`
}

// QualityReport 实体维度的采集质量总报告（诊断端点响应）。
// 「查询成功，无数据」与「查询失败」分开表达：前者引导用户检查时间范围/目标/
// 埋点，后者才是采集源问题——两者混在一起会把「未接入埋点」误报为平台故障。
type QualityReport struct {
	Source  string          `json:"source"`         // prometheus | memory（未接入真实采集源）
	Note    string          `json:"note,omitempty"` // source=memory 时的接入说明
	Metrics []MetricQuality `json:"metrics"`
}

// QualityProber 采集质量探测（独立诊断能力，不在 ListMetrics 主查询路径——
// 告警引擎消费 MetricsReader 零影响）。依赖倒置：compose 持有可选注入，
// real 实现真探测，memory/未装配返回「未接入」报告。
type QualityProber interface {
	// ProbeQuality 对实体 (targetType, targetID) 逐约定 metric 发即时查询。
	// targetType 为空返回未接入说明（全局视图的采集覆盖矩阵留后续）。
	ProbeQuality(ctx context.Context, targetType, targetID string) (QualityReport, error)
}
