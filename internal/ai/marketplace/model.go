// Package marketplace 实现 AI 编排广场（跨租户共享能力市场）：
// 任何租户可把 Skill / Prompt / Tool / Agent 整包发布到广场（脱敏快照，不可变），
// 其他租户浏览/搜索/按分类筛选后「安装 = fork 副本」到自己租户，之后独立演进。
//
// 设计要点：
//   - 快照不可变：发布即定格，源实体后续修改不影响广场条目；更新 = 下架重发（一期无版本链）。
//   - 平台级公开：marketplace_items 无 tenant 过滤（同 maas 模型目录先例）；
//     发布只能发本租户实体，安装 fork 落本租户后即租户隔离。
//   - Tool 凭证：发布时 SanitizeConfig 自动剔除敏感 key（apiKey/token/password/secret/authorization），
//     安装者自行补填；快照不含任何凭证。
//   - Agent 整包：snapshot 内嵌引用的 skills/prompt/tools，安装时全部 fork 并重写 Agent 引用 ID。
package marketplace

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

// EntityType 广场条目类型。
const (
	EntitySkill  = "skill"
	EntityPrompt = "prompt"
	EntityTool   = "tool"
	EntityAgent  = "agent"
)

// Item 广场条目（平台级公开）。Snapshot 按 EntityType 反序列化。
type Item struct {
	ID             string          `json:"id"`
	EntityType     string          `json:"entityType"`
	Name           string          `json:"name"`
	Description    string          `json:"description"`
	Category       string          `json:"category"`
	Snapshot       json.RawMessage `json:"snapshot"`
	PublisherTenant string         `json:"publisherTenant"`
	PublisherName  string          `json:"publisherName"`
	Installs       int             `json:"installs"`
	CreatedAt      time.Time       `json:"createdAt"`
}

// AgentSnapshot Agent 整包快照（安装时全部 fork + 重写引用）。payload 是 JSON 透传
//（agent/skill/prompt/tool 实体字段以原始 JSON 存，安装侧反序列化回实体——forker.go）。
type AgentSnapshot struct {
	Agent  json.RawMessage   `json:"agent"`
	Skills []json.RawMessage `json:"skills,omitempty"`
	Prompt *json.RawMessage  `json:"prompt,omitempty"`
	Tools  []json.RawMessage `json:"tools,omitempty"`
}

// Validate 校验。
func (i Item) Validate() error {
	switch i.EntityType {
	case EntitySkill, EntityPrompt, EntityTool, EntityAgent:
	default:
		return fieldErr("entityType 必须是 skill/prompt/tool/agent")
	}
	if i.Name == "" {
		return fieldErr("name 不能为空")
	}
	if len(i.Snapshot) == 0 {
		return fieldErr("snapshot 不能为空")
	}
	return nil
}

// sensitiveKeyRe 敏感配置 key 匹配（不区分大小写，与前端 SENSITIVE_KEYS 语义对齐——后端是真源）。
var sensitiveKeyRe = regexp.MustCompile(`(?i)apikey|api_key|token|password|passwd|secret|authorization|auth`)

// SanitizeConfig 剔除敏感 key（发布 Tool / Agent 内嵌 Tool 时调）。返回副本，不改入参。
func SanitizeConfig(cfg map[string]string) map[string]string {
	out := make(map[string]string, len(cfg))
	for k, v := range cfg {
		if sensitiveKeyRe.MatchString(k) {
			continue // 凭证不进广场，安装者自行补填
		}
		out[k] = v
	}
	return out
}

// SanitizeJSONConfig 与 SanitizeConfig 同语义，作用于 JSON 反序列化的 map[string]any（嵌套 config 常见形态）。
func SanitizeJSONConfig(cfg map[string]any) map[string]any {
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if sensitiveKeyRe.MatchString(k) {
			continue
		}
		out[k] = v
	}
	return out
}

// sentinel 错误（handler 映射 HTTP 状态）。
var (
	ErrItemNotFound  = errors.New("marketplace item 不存在")
	ErrItemExists    = errors.New("同名条目已发布（重发布会覆盖）")
	ErrNotPublisher  = errors.New("仅发布者可下架")
	ErrEmptyCategory = errors.New("category 不能为空（发布前请补全分类）")
)

type fieldErr string

func (e fieldErr) Error() string { return string(e) }

func IsFieldErr(err error) bool {
	_, ok := err.(fieldErr)
	return ok
}

// NormalizeQuery 搜索关键字小写化（List q 过滤用，name/description 不区分大小写包含匹配）。
func NormalizeQuery(q string) string { return strings.ToLower(strings.TrimSpace(q)) }
