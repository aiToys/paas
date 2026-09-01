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
	"net/url"
	"strings"
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
	if t.Type == TypeMCP {
		if t.Config[CfgMCPServerURL] == "" {
			return fieldErr("mcp 类型需配置 serverURL")
		}
		if err := validateMCPServerURL(t.Config[CfgMCPServerURL]); err != nil {
			return err
		}
	}
	if t.Type == TypeHTTP && t.Config["endpoint"] == "" {
		return fieldErr("http 类型需配置 endpoint")
	}
	if t.Type == TypeBuiltin && t.Config["handler"] == "" {
		return fieldErr("builtin 类型需配置 handler")
	}
	return nil
}

// ConfigMask 掩码占位（与 appconfig.SecretMask / security.SecretMask 同款，不泄漏长度/内容）。
const ConfigMask = "••••••"

// Masked 返回掩码副本：敏感 key（apiKey 等凭证类）替换为固定掩码，其余字段保留。
// list/get 响应一律返回掩码副本（明文仅运行时 invoke 内部使用，不回传前端）。
func (t Tool) Masked() Tool {
	masked := t
	masked.Config = t.MaskedConfig()
	return masked
}

// MaskKeys 是 Config 中的敏感 key 集合（凭证类，回传前端一律掩码）。
var MaskKeys = []string{CfgMCPAPIKey, "token", "password", "secret"}

// MaskedConfig 返回掩码后的 Config 副本（敏感 key 替换为掩码，其余原样；原 map 不动）。
func (t Tool) MaskedConfig() map[string]string {
	out := make(map[string]string, len(t.Config))
	for k, v := range t.Config {
		if isSensitiveKey(k) && v != "" {
			out[k] = ConfigMask
			continue
		}
		out[k] = v
	}
	return out
}

func isSensitiveKey(k string) bool {
	lk := strings.ToLower(k)
	for _, s := range MaskKeys {
		if lk == strings.ToLower(s) {
			return true
		}
	}
	return false
}

// validateMCPServerURL 校验 MCP server 地址，防 SSRF（参考 devops CodeRepo 的防护模式）：
//   - scheme 仅 http/https
//   - 拒环回（localhost/127.0.0.0/8/0.0.0.0/::1）+ 链路本地（169.254.0.0/16）+ 云元数据 host
//
// 与 CodeRepo 不同：不拒私网段与 *.svc.cluster.local——MCP server 可合法部署在集群内
//（dev 集群 gitea.paas.svc 同款场景），只拦「借用平台身份探本机/云元数据」的高敏目标。
func validateMCPServerURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fieldErr("serverURL 不是合法 URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fieldErr("serverURL scheme 仅支持 http/https")
	}
	host := u.Hostname()
	if isLoopbackOrMetadataHost(host) {
		return fieldErr("serverURL 不允许指向环回/链路本地/云元数据地址")
	}
	return nil
}

// isLoopbackOrMetadataHost 判定环回 + 链路本地 + 云元数据 host（SSRF 高敏目标）。
func isLoopbackOrMetadataHost(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	switch h {
	case "", "localhost", "0.0.0.0", "::1", "metadata", "metadata.google.internal":
		return true
	}
	if strings.HasPrefix(h, "127.") || strings.HasPrefix(h, "169.254.") {
		return true
	}
	return false
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
