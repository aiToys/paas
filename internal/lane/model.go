// Package lane 是泳道领域模型（环境维度的运行时隔离单元）。
//
// 泳道是一等实体：大项目独立上线（随项目存续数周）、周期火车常驻联调（Mode=permanent，
// GC 永不回收）、临时 feature 联调（standard，TTL 兜底）三种生命周期一套模型。
// 泳道属环境（联调泳道只在测试环境，生产建泳道被 EnvTypeResolver 护栏拒绝）。
//
// lane 解析优先级（deploy 链路消费）：显式指定 > 实体匹配（EnsureByName 懒建）> 分支名
// （现状路径，向后兼容）。裸分支隐式泳道首次触及时同样懒建实体。
//
// 边界：泳道实体只有技术属性（名/模式/状态/外部链接），不装需求排期负责人——那是
// 项目管理工具的领域；ExternalLink 仅作展示引用（如 Jira issue key）。
package lane

import (
	"errors"
	"time"

	"github.com/aitoys/paas/pkg/names"
)

// 泳道模式。
const (
	ModeStandard  = "standard"  // 常规：闲置 TTL 可回收（laneGC 兜底）
	ModePermanent = "permanent" // 常驻：GC 永不回收（如周期火车联调泳道）
)

var validModes = map[string]struct{}{
	ModeStandard:  {},
	ModePermanent: {},
}

// 泳道状态。
const (
	StatusActive = "active" // 活跃（可部署）
	StatusClosed = "closed" // 已关闭（资源已回收，记录保留供审计追溯）
)

var validStatus = map[string]struct{}{
	StatusActive:  {},
	StatusClosed:  {},
}

// Weight 上限（入口流量权重百分比，本期留位不实现切流，恒 0）。
const WeightMax = 100

var (
	ErrLaneNotFound     = errors.New("lane not found")
	ErrLaneExists       = errors.New("lane already exists")
	ErrLaneNameInvalid  = errors.New("lane name invalid")
)

// Lane 是租户×环境内的一条泳道。
type Lane struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	EnvID        string    `json:"envId"`              // 归属环境
	Name         string    `json:"name"`               // 租户×环境内唯一，DNS-1035 合法（作 K8s 资源名后缀）
	Mode         string    `json:"mode"`               // standard | permanent
	Status       string    `json:"status"`             // active | closed
	Weight       int       `json:"weight"`             // 入口流量权重 0-100（留位，本期恒 0）
	ExternalLink string    `json:"externalLink,omitempty"` // 外部关联（如 Jira issue key），仅展示
	Description  string    `json:"description,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// ValidateName 校验泳道名（DNS-1035：作 K8s Service 名后缀的前提）。
func ValidateName(name string) error {
	if !names.ValidDNS1035(name) {
		return ErrLaneNameInvalid
	}
	return nil
}

// Validate 校验领域约束（Name/EnvID 必填、Mode/Status/Weight 枚举与边界）。
func (l Lane) Validate() error {
	if l.EnvID == "" {
		return errors.New("envId 必填")
	}
	if err := ValidateName(l.Name); err != nil {
		return err
	}
	if _, ok := validModes[l.Mode]; !ok {
		return errors.New("mode 非法（standard|permanent）")
	}
	if l.Status == "" {
		l.Status = StatusActive // 调用方未设时按默认校验
	}
	if _, ok := validStatus[l.Status]; !ok {
		return errors.New("status 非法（active|closed）")
	}
	if l.Weight < 0 || l.Weight > WeightMax {
		return errors.New("weight 超界（0-100）")
	}
	return nil
}
