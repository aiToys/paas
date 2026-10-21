package pipeline

import (
	"context"
	"errors"
	"time"
)

// ErrDeliveryNotFound 投递记录不存在（跨租户同样返此错，不泄漏存在性）。
var ErrDeliveryNotFound = errors.New("webhook delivery not found")

// Webhook 投递记录（D5-5：webhook 运维与幂等重放）。
//
// 为什么需要：webhook 是无人值守入口，此前「跳过/失败」只有服务端 log.Printf，用户侧
// 完全黑盒——push 了不触发无法归因；也没有任何补发路径。本实体把每次投递落成可查记录，
// 并让「重放」成为带幂等保障的显式动作：
//   - 幂等键 = deliveryID（Gitea X-Gitea-Delivery / GitHub X-GitHub-Delivery，缺失时平台生成）。
//     同一键重复到达（webhook 源重试）不重复触发 run，记 duplicate。
//   - 重放直接复用记录中的 branch/commit；已有 RunID 且 run 仍存在时返回该 run（no-op），
//     不因运维手滑造出第二个 run。
//
// 不记录 token 校验失败：webhook 无租户身份，未授权请求落库等于开放无界写入面（审计放大）。
const (
	// DeliveryAccepted 已受理并触发 run（RunID 非空）。
	DeliveryAccepted = "accepted"
	// DeliveryDuplicate 同一 deliveryID 重复到达，复用已有结果（不新建 run）。
	DeliveryDuplicate = "duplicate"
	// DeliverySkipped 有意跳过（非分支 push / 分支删除 / 已禁用 / glob 不匹配）。
	DeliverySkipped = "skipped"
	// DeliveryRejected 触发被拒（生产门禁缺审批、模板缺失、并发 active run 之外的服务端拒绝）。
	DeliveryRejected = "rejected"
	// DeliveryFailed 触发链路失败（解析模板/解析占位符/创建 run 报错）。
	DeliveryFailed = "failed"

	// maxDeliveriesPerPipeline 单 pipeline 保留记录上限（内存实现裁剪用；PG 只按 limit 查询）。
	maxDeliveriesPerPipeline = 200
)

// WebhookDelivery 一次 webhook 投递的可查记录。
type WebhookDelivery struct {
	ID           string     `json:"id"` // 幂等键（源 delivery ID 或平台生成）
	TenantID     string     `json:"tenantId"`
	PipelineID   string     `json:"pipelineId"`
	AppID        string     `json:"appId"`
	Event        string     `json:"event,omitempty"`  // push / 其他（当前只处理 push）
	Branch       string     `json:"branch,omitempty"` // 解析后的分支（非分支事件为空）
	Commit       string     `json:"commit,omitempty"`
	Status       string     `json:"status"`
	Reason       string     `json:"reason,omitempty"` // 中文原因（跳过/拒绝/失败的可归因说明）
	RunID        string     `json:"runId,omitempty"`
	Replays      int        `json:"replays"`    // 人工重放次数（0 = 仅原始投递）
	ReceivedAt   time.Time  `json:"receivedAt"` // 首次接收时刻
	LastReplayAt *time.Time `json:"lastReplayAt,omitempty"`
}

// DeliveryRepository webhook 投递记录读写（memory/PG 双实现）。
// 查询一律按 ctx 租户过滤；webhook 路径用服务端派生租户 ctx 写入。
type DeliveryRepository interface {
	// ListDeliveries 按 pipeline 列最近记录（received_at 倒序，limit<=0 用默认 50）。
	ListDeliveries(ctx context.Context, pipelineID string, limit int) ([]WebhookDelivery, error)
	// GetDelivery 按 (pipelineID, 幂等键) 查单条；不存在返 ErrNotFound。
	GetDelivery(ctx context.Context, pipelineID, id string) (WebhookDelivery, error)
	// CreateDelivery 落库首次投递；同 (pipelineID, id) 重复创建返 ErrActiveRunExists 之外
	// 的普通错误，由调用方转为 duplicate 语义（调用方应先查后建）。
	CreateDelivery(ctx context.Context, d WebhookDelivery) (WebhookDelivery, error)
	// UpdateDelivery 更新状态/原因/RunID/重放计数（归属字段以库内为准）。
	UpdateDelivery(ctx context.Context, d WebhookDelivery) (WebhookDelivery, error)
}

// cloneDelivery 深拷贝（指针字段独立），防调用方改动污染存储。
func cloneDelivery(d WebhookDelivery) WebhookDelivery {
	out := d
	if d.LastReplayAt != nil {
		t := *d.LastReplayAt
		out.LastReplayAt = &t
	}
	return out
}
