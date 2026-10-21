package observability

import (
	"strings"
	"testing"
	"time"
)

func TestValidateChannelURL(t *testing.T) {
	ok := []string{
		"https://hooks.example.com/robot/send?token=abc",
		"http://127.0.0.1:9093/alert",
	}
	for _, u := range ok {
		if err := ValidateChannelURL(u); err != nil {
			t.Fatalf("%q 应合法: %v", u, err)
		}
	}
	bad := []string{"", "hooks.example.com/x", "ftp://host/x", "https://", "://x"}
	for _, u := range bad {
		if err := ValidateChannelURL(u); err == nil {
			t.Fatalf("%q 应非法", u)
		}
	}
}

func TestNotificationChannelValidate(t *testing.T) {
	base := NotificationChannel{
		Name: "值班群", Kind: ChannelKindWebhook,
		URL: "https://hooks.example.com/robot", Enabled: true,
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("合法渠道不应报错: %v", err)
	}

	cases := []struct {
		name string
		mod  func(*NotificationChannel)
	}{
		{"空名称", func(c *NotificationChannel) { c.Name = "  " }},
		{"未知类型", func(c *NotificationChannel) { c.Kind = "email" }},
		{"空地址", func(c *NotificationChannel) { c.URL = "" }},
		{"负冷却", func(c *NotificationChannel) { c.CooldownMinutes = -1 }},
		{"冷却超上限", func(c *NotificationChannel) { c.CooldownMinutes = maxChannelCooldown + 1 }},
		{"头名称含冒号", func(c *NotificationChannel) { c.Headers = map[string]string{"X:Bad": "v"} }},
		{"头值含换行", func(c *NotificationChannel) { c.Headers = map[string]string{"X": "a\r\nb"} }},
	}
	for _, tc := range cases {
		c := base
		tc.mod(&c)
		if err := c.Validate(); err == nil {
			t.Fatalf("%s 应校验失败", tc.name)
		}
	}
}

func TestNotificationChannelMasked(t *testing.T) {
	c := NotificationChannel{
		ID: "ch-1", URL: "https://hooks.example.com/robot/send?token=secret-token",
		Headers: map[string]string{"Authorization": "Bearer secret"},
	}
	m := c.Masked()
	if strings.Contains(m.URL, "secret-token") || strings.Contains(m.URL, "robot") {
		t.Fatalf("URL 路径与 token 必须掩码, got %q", m.URL)
	}
	if m.URL != "https://hooks.example.com/***" {
		t.Fatalf("URL 掩码格式不符, got %q", m.URL)
	}
	if m.Headers["Authorization"] != MaskedPlaceholder {
		t.Fatalf("请求头值必须掩码, got %q", m.Headers["Authorization"])
	}
	// 原对象不被修改（掩码只作用于副本）。
	if c.Headers["Authorization"] != "Bearer secret" {
		t.Fatal("Masked 不应修改原对象")
	}
}

func TestNotificationChannelMergeSecretUpdates(t *testing.T) {
	old := NotificationChannel{
		ID: "ch-1", TenantID: "t-acme", Name: "值班群",
		URL:       "https://hooks.example.com/robot/send?token=real",
		Headers:   map[string]string{"Authorization": "Bearer real", "X-Keep": "keep"},
		CreatedAt: time.Now().Add(-time.Hour),
	}

	// 前端回传掩码占位：URL 与同名头保留原值。
	req := NotificationChannel{
		Name: "值班群2", URL: "https://hooks.example.com/***",
		Headers: map[string]string{"Authorization": MaskedPlaceholder, "X-New": "new"},
		Enabled: true,
	}
	merged := req.MergeSecretUpdates(old)
	if merged.URL != old.URL {
		t.Fatalf("掩码 URL 应保留原值, got %q", merged.URL)
	}
	if merged.Headers["Authorization"] != "Bearer real" {
		t.Fatalf("掩码头应保留原值, got %q", merged.Headers["Authorization"])
	}
	if _, ok := merged.Headers["X-Keep"]; ok {
		t.Fatal("未出现的头应删除（整体替换语义）")
	}
	if merged.Headers["X-New"] != "new" {
		t.Fatal("新头应写入")
	}
	if merged.ID != "ch-1" || merged.TenantID != "t-acme" || !merged.CreatedAt.Equal(old.CreatedAt) {
		t.Fatal("归属与创建时间必须以库内为准")
	}

	// 空 URL 同样保留原值（表单留空不等于清空——渠道没有地址不可投递）。
	req2 := NotificationChannel{Name: "x", URL: "", Enabled: true}
	if got := req2.MergeSecretUpdates(old).URL; got != old.URL {
		t.Fatalf("空 URL 应保留原值, got %q", got)
	}
}

func TestNotificationChannelActive(t *testing.T) {
	now := time.Now()
	if !(NotificationChannel{Enabled: true}).Active(now) {
		t.Fatal("启用且未静默应投递")
	}
	if (NotificationChannel{Enabled: false}).Active(now) {
		t.Fatal("停用不应投递")
	}
	if (NotificationChannel{Enabled: true, MuteUntil: now.Add(time.Minute)}).Active(now) {
		t.Fatal("静默期内不应投递")
	}
	if !(NotificationChannel{Enabled: true, MuteUntil: now.Add(-time.Minute)}).Active(now) {
		t.Fatal("静默已过期应恢复投递")
	}
}

func TestAlertRuleChannelIDsValidate(t *testing.T) {
	r := AlertRule{Name: "x", MetricName: MetricCPU, TargetType: TargetApp,
		Operator: OpGT, Threshold: 1, Severity: SeverityWarning, Enabled: true}
	if err := r.Validate(); err != nil {
		t.Fatalf("无渠道引用应合法: %v", err)
	}
	r.ChannelIDs = []string{"ch-1", "ch-1"}
	if err := r.Validate(); err == nil {
		t.Fatal("重复渠道引用应拒绝")
	}
	r.ChannelIDs = []string{"ch-1", ""}
	if err := r.Validate(); err == nil {
		t.Fatal("空渠道 ID 应拒绝")
	}
	r.ChannelIDs = make([]string, 11)
	for i := range r.ChannelIDs {
		r.ChannelIDs[i] = string(rune('a' + i))
	}
	if err := r.Validate(); err == nil {
		t.Fatal("超过 10 个渠道应拒绝")
	}
}
