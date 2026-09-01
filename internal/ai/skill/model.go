// Package skill 实现 AI Skill（P3.x）：可复用的指令能力包，Agent 绑定后运行时注入 system prompt。
//
// Skill 与 Prompt 的区别：Prompt 是「一段模板」（单版本快照，Agent 整体引用作 system prompt）；
// Skill 是「一项能力指令」（做什么/怎么做/约束），可与 system prompt 叠加注入，一个 Agent
// 可绑定多个 Skill 组合出复杂行为（对标 Claude Skills / GPTs Instructions 的能力化拆分）。
//
// 租户私有；不绑物理环境（无 prod:write）。
package skill

import (
	"errors"
	"time"
)

// Skill 指令能力包（租户私有）。
type Skill struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenantId"`
	Name          string    `json:"name"`                    // 租户内唯一
	Description   string    `json:"description"`             // 一句话用途（给管理员看）
	Instructions  string    `json:"instructions"`            // 指令正文（注入 system prompt，给 LLM 看）
	Category      string    `json:"category,omitempty"`      // 广场分类（writing/coding/data/service/general）
	UseCases      string    `json:"useCases,omitempty"`      // 适用场景说明（给人看，降低试用门槛）
	Examples      string    `json:"examples,omitempty"`      // 使用示例 markdown（输入→期望输出）
	InstalledFrom string    `json:"installedFrom,omitempty"` // 来源 marketplace item ID（空=自建）
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// Validate 校验。
func (s Skill) Validate() error {
	if s.Name == "" {
		return fieldErr("name 不能为空")
	}
	if s.Instructions == "" {
		return fieldErr("instructions 不能为空")
	}
	return nil
}

// sentinel 错误（handler 映射 HTTP 状态）。
var (
	ErrSkillNotFound = errors.New("skill 不存在")
	ErrSkillExists   = errors.New("skill 已存在")
)

type fieldErr string

func (e fieldErr) Error() string { return string(e) }

func isFieldErr(err error) bool {
	_, ok := err.(fieldErr)
	return ok
}
