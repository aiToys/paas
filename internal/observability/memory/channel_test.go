package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/observability"
	"github.com/aitoys/paas/pkg/tenant"
)

func chCtx(tid string) context.Context { return tenant.WithTenant(context.Background(), tid) }

// TestChannelStoreLifecycle：创建/列表/更新/删除 + 敏感字段掩码占位保留原值。
func TestChannelStoreLifecycle(t *testing.T) {
	s := NewStore()
	ctx := chCtx("t-acme")

	created, err := s.CreateNotificationChannel(ctx, observability.NotificationChannel{
		Name: "值班群", Kind: observability.ChannelKindWebhook,
		URL: "https://hooks.example.com/robot?token=real", Enabled: true,
		Headers: map[string]string{"Authorization": "Bearer real"},
	})
	if err != nil {
		t.Fatalf("创建渠道失败: %v", err)
	}
	if created.ID == "" || created.TenantID != "t-acme" {
		t.Fatalf("ID/租户未填充: %+v", created)
	}

	list, err := s.ListNotificationChannels(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("列表应有 1 条, got %d err=%v", len(list), err)
	}

	// 编辑：回传掩码占位 + 修改名称/冷却。
	updated, err := s.UpdateNotificationChannel(ctx, created.ID, observability.NotificationChannel{
		Name: "值班群（夜班）", Kind: observability.ChannelKindWebhook,
		URL: observability.MaskedPlaceholder, Enabled: true, CooldownMinutes: 30,
		Headers: map[string]string{"Authorization": observability.MaskedPlaceholder},
	})
	if err != nil {
		t.Fatalf("更新渠道失败: %v", err)
	}
	if updated.URL != "https://hooks.example.com/robot?token=real" {
		t.Fatalf("掩码 URL 应保留原值, got %q", updated.URL)
	}
	if updated.Headers["Authorization"] != "Bearer real" {
		t.Fatalf("掩码头应保留原值, got %q", updated.Headers["Authorization"])
	}
	if updated.CooldownMinutes != 30 {
		t.Fatalf("冷却未更新, got %d", updated.CooldownMinutes)
	}

	if err := s.DeleteNotificationChannel(ctx, created.ID); err != nil {
		t.Fatalf("删除渠道失败: %v", err)
	}
	if list, _ := s.ListNotificationChannels(ctx); len(list) != 0 {
		t.Fatalf("删除后列表应为空, got %d", len(list))
	}
	if err := s.DeleteNotificationChannel(ctx, created.ID); err == nil {
		t.Fatal("删除不存在的渠道应报错")
	}
}

// TestChannelStoreTenantIsolation：跨租户列表不可见、更新/删除统一 not found 不泄漏。
func TestChannelStoreTenantIsolation(t *testing.T) {
	s := NewStore()
	acme, err := s.CreateNotificationChannel(chCtx("t-acme"), observability.NotificationChannel{
		Name: "acme 值班", Kind: observability.ChannelKindWebhook, URL: "https://a.example.com/x", Enabled: true,
	})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	list, _ := s.ListNotificationChannels(chCtx("t-globex"))
	if len(list) != 0 {
		t.Fatalf("异租户不应看到渠道, got %d", len(list))
	}
	if _, err := s.UpdateNotificationChannel(chCtx("t-globex"), acme.ID, observability.NotificationChannel{
		Name: "hijack", URL: "https://evil.example.com/x", Enabled: true,
	}); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("跨租户更新应 not found，got %v", err)
	}
	if err := s.DeleteNotificationChannel(chCtx("t-globex"), acme.ID); err == nil {
		t.Fatal("跨租户删除应 not found")
	}
	// 原渠道不受影响。
	list, _ = s.ListNotificationChannels(chCtx("t-acme"))
	if len(list) != 1 || list[0].Name != "acme 值班" {
		t.Fatalf("原渠道不应被跨租户操作影响, got %+v", list)
	}
}

// TestChannelStoreDuplicateName：同租户重名拒绝，异租户可同名。
func TestChannelStoreDuplicateName(t *testing.T) {
	s := NewStore()
	ch := observability.NotificationChannel{Name: "值班", Kind: observability.ChannelKindWebhook,
		URL: "https://a.example.com/x", Enabled: true}
	if _, err := s.CreateNotificationChannel(chCtx("t-acme"), ch); err != nil {
		t.Fatalf("首次创建失败: %v", err)
	}
	if _, err := s.CreateNotificationChannel(chCtx("t-acme"), ch); err == nil {
		t.Fatal("同租户重名应拒绝")
	}
	if _, err := s.CreateNotificationChannel(chCtx("t-globex"), ch); err != nil {
		t.Fatalf("异租户同名应允许: %v", err)
	}
}

// TestChannelStoreValidate：非法渠道不落库（memory 与 PG 同源校验）。
func TestChannelStoreValidate(t *testing.T) {
	s := NewStore()
	if _, err := s.CreateNotificationChannel(chCtx("t-acme"), observability.NotificationChannel{
		Name: "x", Kind: "email", URL: "https://a.example.com/x", Enabled: true,
	}); err == nil {
		t.Fatal("不支持的类型应拒绝")
	}
	if _, err := s.CreateNotificationChannel(chCtx("t-acme"), observability.NotificationChannel{
		Name: "x", Kind: observability.ChannelKindWebhook, URL: "not-a-url", Enabled: true,
	}); err == nil {
		t.Fatal("非法地址应拒绝")
	}
	if list, _ := s.ListNotificationChannels(chCtx("t-acme")); len(list) != 0 {
		t.Fatalf("校验失败不应落库, got %d", len(list))
	}
}

// TestChannelStoreMuteExpiry：静默到期后 Active 恢复（时间语义锁）。
func TestChannelStoreMuteExpiry(t *testing.T) {
	c := observability.NotificationChannel{Enabled: true, MuteUntil: time.Now().Add(-time.Second)}
	if !c.Active(time.Now()) {
		t.Fatal("静默已过期应恢复投递")
	}
}
