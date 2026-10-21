package observability

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// 通知渠道类型（当前仅 webhook；新增类型必须先落地出站实现再放开校验，
// 允许「配置成功但永不投递」的枚举是最差失败形态）。
const (
	ChannelKindWebhook = "webhook"
)

// 渠道容量上限（防单租户无界增长与请求体滥用）。
const (
	MaxChannelsPerTenant = 50
	MaxChannelHeaders    = 20
	maxChannelCooldown   = 7 * 24 * 60 // 冷却上限 7 天（分钟）
)

// MaskedPlaceholder 是响应中替换敏感值的占位符。
// 编辑语义：客户端回传该占位符（或空值）= 保留服务端原值，不回填明文。
const MaskedPlaceholder = "***"

// NotificationChannel 是告警通知渠道（租户级）。
//
// 抑制策略（三段，语义互不重叠）：
//   - Enabled=false：渠道整体停用，不投递。
//   - MuteUntil：临时静默截止时刻（运维窗口）。静默期内不投递；静默结束后
//     仅靠状态转变/重复提醒自然恢复，不补发积压通知。
//   - CooldownMinutes：同规则同渠道的重复提醒抑制窗口。0 = 不做重复提醒
//     （只在 firing/resolved 状态转变时通知一次）；>0 = firing 持续期间每隔
//     该时长重发一次提醒，直到恢复（值班场景防「一次通知后无人再看」）。
type NotificationChannel struct {
	ID              string            `json:"id"`
	TenantID        string            `json:"tenantId,omitempty"`
	Name            string            `json:"name"`
	Kind            string            `json:"kind"`              // webhook
	URL             string            `json:"url,omitempty"`     // 出站地址；响应经 Masked() 掩码
	Headers         map[string]string `json:"headers,omitempty"` // 自定义请求头；响应值掩码
	Enabled         bool              `json:"enabled"`
	CooldownMinutes int               `json:"cooldownMinutes"`
	MuteUntil       time.Time         `json:"muteUntil,omitempty"`
	CreatedAt       time.Time         `json:"createdAt"`
	UpdatedAt       time.Time         `json:"updatedAt"`
}

// Validate 校验渠道字段（memory/PG 双实现同源调用）。
func (c NotificationChannel) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("渠道名称不能为空")
	}
	kind := c.Kind
	if kind == "" {
		kind = ChannelKindWebhook
	}
	if kind != ChannelKindWebhook {
		return fmt.Errorf("不支持的通知渠道类型: %s（当前仅支持 webhook）", c.Kind)
	}
	if err := ValidateChannelURL(c.URL); err != nil {
		return err
	}
	if len(c.Headers) > MaxChannelHeaders {
		return fmt.Errorf("自定义请求头不能超过 %d 个", MaxChannelHeaders)
	}
	for k, v := range c.Headers {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("自定义请求头名称不能为空")
		}
		if strings.ContainsAny(k, "\r\n:") {
			return fmt.Errorf("自定义请求头名称非法: %s", k)
		}
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("自定义请求头 %s 的值含换行符", k)
		}
	}
	if c.CooldownMinutes < 0 || c.CooldownMinutes > maxChannelCooldown {
		return fmt.Errorf("重复提醒间隔必须在 0～%d 分钟之间（0 = 只在状态转变时通知）", maxChannelCooldown)
	}
	return nil
}

// ValidateChannelURL 校验出站地址：必须是 http/https 且带主机名。
// 空串视为非法——渠道没有地址无法投递，静默应通过 Enabled/MuteUntil 表达。
func ValidateChannelURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("通知地址不能为空")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("通知地址格式非法")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("通知地址必须以 http:// 或 https:// 开头")
	}
	return nil
}

// Masked 返回脱敏副本（URL 只保留 scheme+host，路径与查询串全掩码；
// 自定义头只保留名称）。渠道 URL 常含 token/webhook key，列表与详情一律掩码，
// 明文仅出站投递时内部使用。
func (c NotificationChannel) Masked() NotificationChannel {
	out := c
	out.URL = MaskChannelURL(c.URL)
	if len(c.Headers) > 0 {
		masked := make(map[string]string, len(c.Headers))
		for k := range c.Headers {
			masked[k] = MaskedPlaceholder
		}
		out.Headers = masked
	}
	return out
}

// MaskChannelURL 把出站地址掩码为 scheme://host/***（解析失败返 "***"，不泄漏原始串）。
func MaskChannelURL(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return MaskedPlaceholder
	}
	return u.Scheme + "://" + u.Host + "/" + MaskedPlaceholder
}

// IsMasked 判断回传值是否为脱敏占位（编辑保留原值的信号）。
func IsMasked(v string) bool {
	return v == MaskedPlaceholder || strings.Contains(v, "/"+MaskedPlaceholder)
}

// MergeSecretUpdates 把「编辑请求」合并到已有渠道：请求中 URL 为掩码占位或空则保留原值；
// Headers 值为占位符的键保留原值，未出现的键删除（整体替换语义，与前端表单一致）。
func (c NotificationChannel) MergeSecretUpdates(old NotificationChannel) NotificationChannel {
	out := c
	if strings.TrimSpace(out.URL) == "" || IsMasked(out.URL) {
		out.URL = old.URL
	}
	if len(out.Headers) > 0 {
		merged := make(map[string]string, len(out.Headers))
		for k, v := range out.Headers {
			if v == MaskedPlaceholder {
				if ov, ok := old.Headers[k]; ok {
					merged[k] = ov
				}
				continue
			}
			merged[k] = v
		}
		out.Headers = merged
	}
	out.ID = old.ID
	out.TenantID = old.TenantID
	out.CreatedAt = old.CreatedAt
	return out
}

// Normalize 补齐缺省值（Kind 缺省 webhook；Headers 空 map 归一为 nil）。
func (c *NotificationChannel) Normalize() {
	if c.Kind == "" {
		c.Kind = ChannelKindWebhook
	}
	if len(c.Headers) == 0 {
		c.Headers = nil
	}
}

// Active 判断渠道此刻是否应投递（启用且不在静默期）。
func (c NotificationChannel) Active(now time.Time) bool {
	return c.Enabled && (c.MuteUntil.IsZero() || !now.Before(c.MuteUntil))
}

// SortChannels 按名称稳定排序（列表输出顺序确定，便于前端与测试断言）。
func SortChannels(list []NotificationChannel) {
	sort.Slice(list, func(i, j int) bool {
		if list[i].Name != list[j].Name {
			return list[i].Name < list[j].Name
		}
		return list[i].ID < list[j].ID
	})
}
