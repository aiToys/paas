// Package environment 是平台的物理环境领域模型。
// 环境是独立一等公民（非应用子节点），分两类：生产(prod)/测试(test)。
// 应用 × 环境多对多，交叉点 = 部署实例（Workload/Binding 带 EnvID）。
// 环境内再分基线(LaneID=default)与泳道(预留，本期不实现路由)。
package environment

import (
	"errors"
	"time"
)

// 环境类型。
const (
	TypeProd = "prod" // 生产环境
	TypeTest = "test" // 测试环境
)

var validTypes = map[string]struct{}{
	TypeProd: {},
	TypeTest: {},
}

// ErrNoPromoteTarget 表示当前环境已是最高阶序，无晋升目标（promote 不可用）。
var ErrNoPromoteTarget = errors.New("no promote target environment: already at highest order")

// DefaultPromoteOrder 按 type 返回默认阶序（test=10, prod=20，留间隔便于插 staging=15）。
// 0 = 不参与发布流水线（promote 跳过该环境）。新建环境未指定 order 时按 type 填默认。
func DefaultPromoteOrder(envType string) int {
	switch envType {
	case TypeTest:
		return 10
	case TypeProd:
		return 20
	}
	return 0
}

// Environment 是物理隔离单元。
type Environment struct {
	ID           string    `json:"id"`
	TenantID     string    `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	Name         string    `json:"name"`               // 生产-北京 / 测试
	Type         string    `json:"type"`               // prod | test
	Cluster      string    `json:"cluster,omitempty"`  // 物理落点 prod-bj/prod-sh；test 可空
	Desc         string    `json:"desc,omitempty"`
	PromoteOrder int       `json:"promoteOrder,omitempty"` // 发布流水线阶序（升序），0=不参与；test=10/prod=20 默认
	CreatedAt    time.Time `json:"createdAt"`
}

// Validate 校验环境字段。
func (e Environment) Validate() error {
	if _, ok := validTypes[e.Type]; !ok {
		return errInvalid("type")
	}
	if e.Name == "" {
		return errInvalid("name")
	}
	// prod 建议有 cluster（多区），但不强制；test 通常无 cluster
	return nil
}

type fieldErr struct{ field string }

func (e fieldErr) Error() string { return "字段非法或缺失: " + e.field }

func errInvalid(field string) error { return fieldErr{field: field} }
