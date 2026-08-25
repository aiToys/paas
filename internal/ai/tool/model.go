// Package tool 实现 AI 工具管理（P2）：Tool 是 Agent 可调用的外部能力单元。
//
// 三种类型：
//   - mcp：Model Context Protocol server（标准化工具协议，tools/list + tools/call）
//   - http：任意 HTTP 端点（request 模板 → response 提取，简易 webhook 工具）
//   - builtin：平台内置工具（知识库检索/网络搜索等，按 name 分发到平台实现）
//
// 租户私有；不绑物理环境（无 prod:write）。Config 存类型相关配置（MCP: serverURL/apiKey；
// HTTP: endpoint/method/headers；builtin: handler）。Agent 运行时按 Tool.Type 选执行体。
package tool

import (
	"errors"
	"time"
)

// Type 常量：工具类型。
const (
	TypeMCP     = "mcp"     // MCP server（JSON-RPC 2.0 over HTTP）
	TypeHTTP    = "http"    // 通用 HTTP 端点
	TypeBuiltin = "builtin" // 平台内置（按 name 分发）
)

// Tool 工具定义（租户私有）。
type Tool struct {
	ID            string            `json:"id"`
	TenantID      string            `json:"tenantId"`
	Name          string            `json:"name"`        // 租户内唯一
	Description   string            `json:"description"` // 给 LLM 看的用途说明
	Type          string            `json:"type"`        // mcp | http | builtin
	Config        map[string]string `json:"config"`      // 类型相关配置
	Category      string            `json:"category,omitempty"`      // 广场分类
	InstalledFrom string            `json:"installedFrom,omitempty"` // 来源 marketplace item ID（空=自建）
	Enabled       bool              `json:"enabled"`
	CreatedAt     time.Time         `json:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
}

// MCP Config key 约定（Config map 的 key，type=mcp 时用）。
const (
	CfgMCPServerURL = "serverURL" // MCP server HTTP 根地址（如 http://srv:8080）
	CfgMCPAPIKey    = "apiKey"    // MCP server 鉴权 key（Bearer，可空）
)

// Validate 校验工具定义（创建/更新前调）。
func (t Tool) Validate() error {
	if t.Name == "" {
		return fieldErr("name 不能为空")
	}
	switch t.Type {
	case TypeMCP, TypeHTTP, TypeBuiltin:
	default:
		return fieldErr("type 必须是 mcp/http/builtin")
	}
	if t.Type == TypeMCP && t.Config[CfgMCPServerURL] == "" {
		return fieldErr("mcp 类型需配置 serverURL")
	}
	if t.Type == TypeHTTP && t.Config["endpoint"] == "" {
		return fieldErr("http 类型需配置 endpoint")
	}
	if t.Type == TypeBuiltin && t.Config["handler"] == "" {
		return fieldErr("builtin 类型需配置 handler")
	}
	return nil
}

// sentinel 错误（handler 映射 HTTP 状态）。
var (
	ErrToolNotFound = errors.New("tool 不存在")
	ErrToolExists   = errors.New("tool 已存在")
)

// fieldErr 字段校验错误（handler 映射 400）。
type fieldErr string

func (e fieldErr) Error() string { return string(e) }

func isFieldErr(err error) bool {
	_, ok := err.(fieldErr)
	return ok
}
