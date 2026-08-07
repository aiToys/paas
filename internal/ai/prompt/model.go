// Package prompt 实现 Prompt 模板管理（P2）：版本化提示词模板，供 Agent / 应用复用。
//
// 版本模型：同 name 多版本（每版本独立 row），Create 同 name → version=max+1 且自动激活
// （最新版默认 active，旧版 deactive）；SetActive 手动切回旧版本。Agent 用 GetActive(name) 取当前生效。
//
// 模板用 Go text/template 语法（{{.Var}}），Variables 声明变量名供 Agent 展示/校验。
// 租户私有；不绑物理环境（无 prod:write）。A-B 实验/灰度留后续。
package prompt

import (
	"errors"
	"strings"
	"time"
)

// Prompt 提示词模板（一个版本一行）。
type Prompt struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId"`
	Name      string    `json:"name"`      // 租户内唯一逻辑名（多版本共用）
	Template  string    `json:"template"`  // 模板文本（{{.Var}} 占位）
	Variables []string  `json:"variables"` // 声明变量
	Version   int       `json:"version"`   // 版本号（同 name 单调递增）
	Active    bool      `json:"active"`    // 是否当前激活版本（同 name 仅一个 active）
	CreatedAt time.Time `json:"createdAt"`
}

// Validate 校验（创建前调）。
func (p Prompt) Validate() error {
	if p.Name == "" {
		return fieldErr("name 不能为空")
	}
	if strings.TrimSpace(p.Template) == "" {
		return fieldErr("template 不能为空")
	}
	return nil
}

// sentinel 错误。
var (
	ErrPromptNotFound = errors.New("prompt 不存在")
	ErrPromptExists   = errors.New("prompt 已存在") // 同 ID 冲突（极少，ID 由 store 生成）
	ErrNoActivePrompt = errors.New("无激活版本的 prompt")
)

type fieldErr string

func (e fieldErr) Error() string { return string(e) }

func isFieldErr(err error) bool {
	_, ok := err.(fieldErr)
	return ok
}
