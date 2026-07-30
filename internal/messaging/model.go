// Package messaging 是消息队列的 Topic 与消费组管理领域。
// Topic 归属某个 MQ 数据服务资源（dataservice kind=mq），租户私有。
// 本期进程内 mock（不接真实 Kafka/RabbitMQ）；CRUD 即生效，无真实消息收发。
package messaging

import "time"

// Topic 是消息主题，归属于某 MQ 数据服务资源。
type Topic struct {
	ID         string    `json:"id"`
	TenantID   string    `json:"tenantId"`
	MQID       string    `json:"mqId"`       // 所属 MQ 数据服务 ID
	Name       string    `json:"name"`       // topic 名（MQ 实例内唯一）
	Partitions int       `json:"partitions"` // 分区数
	Retention  string    `json:"retention"`  // 消息保留（如 "7d"）
	Status     string    `json:"status"`     // active|paused
	CreatedAt  time.Time `json:"createdAt,omitempty"`
}

// Topic 状态。
const (
	StatusActive = "active"
	StatusPaused = "paused"
)

// 消费组模式。
const (
	ModeClustering = "clustering" // 集群（分区分配）
	ModeBroadcast  = "broadcast"  // 广播
)

// ConsumerGroup 是 Topic 的消费组。
type ConsumerGroup struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId"`
	TopicID   string    `json:"topicId"`
	Name      string    `json:"name"` // 消费组名（Topic 内唯一）
	Mode      string    `json:"mode"` // clustering|broadcast
	Members   int       `json:"members"`
	CreatedAt time.Time `json:"createdAt,omitempty"`
}
