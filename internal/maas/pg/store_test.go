//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。
// 每测 newTestDB 自动迁移建表，结束时 resetSchema DROP 全部表（含 maas_*）避免残留。

package pg

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/aitoys/paas/internal/maas"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/provider"
)

// newTestDB 创建测试 DB 连接并跑迁移；测试结束自动 DROP 全表。
func newTestDB(t *testing.T) *storagepg.DB {
	t.Helper()
	dsn := os.Getenv("PAAS_TEST_PG_URL")
	if dsn == "" {
		t.Skip("PAAS_TEST_PG_URL 未设置，跳过 PG 集成测试")
	}
	ctx := context.Background()
	db, err := storagepg.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("打开测试 DB 失败: %v", err)
	}
	t.Cleanup(db.Close)
	if err := storagepg.RunMigrations(ctx, db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { resetSchema(t, db) })
	return db
}

// resetSchema 清空所有业务表 + 迁移版本表，避免跨包测试残留污染（含 maas_*）。
func resetSchema(t *testing.T, db *storagepg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(), `
DROP TABLE IF EXISTS
  maas_channels, maas_models,
  audit_logs, secrets, billing_records, billing_usages, billing_quotas,
  cc_publishes, cc_items, cc_namespaces, gov_breakers, gov_routes, gov_instances, gov_services,
  releases, images, build_runs, code_repos, workloads, data_services, app_configs,
  environments, application_bindings, applications, api_key_roles, api_keys, user_roles, users, tenants
CASCADE;
DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func TestPGMaasStore(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	if n, _ := s.ModelsCount(ctx); n != 0 {
		t.Fatalf("空表 ModelsCount want 0, got %d", n)
	}

	m := &provider.Model{ID: "gpt-4o", Name: "GPT-4o", Vendor: "OpenAI", ContextWindow: 128000,
		Capabilities: []string{"chat", "vision"}, InputPrice: 2.5, OutputPrice: 10, Description: "旗舰"}
	if err := s.CreateModel(ctx, m); err != nil {
		t.Fatalf("CreateModel: %v", err)
	}
	if err := s.CreateModel(ctx, m); !errors.Is(err, maas.ErrModelExists) {
		t.Fatalf("重复 want ErrModelExists, got %v", err)
	}

	// GetModel 无 channels
	got, err := s.GetModel(ctx, "gpt-4o")
	if err != nil || got.Name != "GPT-4o" || len(got.Capabilities) != 2 || len(got.Channels) != 0 {
		t.Fatalf("GetModel got %+v err=%v", got, err)
	}
	if _, err := s.GetModel(ctx, "nope"); !errors.Is(err, maas.ErrModelNotFound) {
		t.Fatalf("GetModel not found want ErrModelNotFound, got %v", err)
	}

	// CreateChannel
	c := &provider.Channel{ID: "gpt-4o#openai", Type: "openai-compatible", Priority: 0, Status: "healthy",
		Endpoint: "https://api.openai.com/v1", Vendor: "openai", UpstreamModel: "gpt-4o", CredentialRef: "sec-openai"}
	if err := s.CreateChannel(ctx, "gpt-4o", c); err != nil {
		t.Fatalf("CreateChannel: %v", err)
	}
	if err := s.CreateChannel(ctx, "none", c); !errors.Is(err, maas.ErrModelNotFound) {
		t.Fatalf("CreateChannel model 不存在 want ErrModelNotFound, got %v", err)
	}
	if err := s.CreateChannel(ctx, "gpt-4o", c); !errors.Is(err, maas.ErrChannelExists) {
		t.Fatalf("重复通道 want ErrChannelExists, got %v", err)
	}

	// ListModels 聚合 channels
	list, err := s.ListModels(ctx)
	if err != nil || len(list) != 1 || len(list[0].Channels) != 1 {
		t.Fatalf("ListModels got %d models, channels=%v err=%v", len(list), list, err)
	}
	// GetModel 含 channels
	got, _ = s.GetModel(ctx, "gpt-4o")
	if len(got.Channels) != 1 || got.Channels[0].UpstreamModel != "gpt-4o" {
		t.Fatalf("GetModel channels got %+v", got.Channels)
	}

	// UpdateChannel
	if err := s.UpdateChannel(ctx, "gpt-4o", &provider.Channel{ID: "gpt-4o#openai", Priority: 5, Status: "offline"}); err != nil {
		t.Fatalf("UpdateChannel: %v", err)
	}
	chs, _ := s.ListChannels(ctx, "gpt-4o")
	if len(chs) != 1 || chs[0].Priority != 5 || chs[0].Status != "offline" {
		t.Fatalf("UpdateChannel 后 %+v", chs[0])
	}
	if err := s.UpdateChannel(ctx, "gpt-4o", &provider.Channel{ID: "nope"}); !errors.Is(err, maas.ErrChannelNotFound) {
		t.Fatalf("UpdateChannel not found want ErrChannelNotFound, got %v", err)
	}

	// UpdateModel 标量，channels 保留
	if err := s.UpdateModel(ctx, &provider.Model{ID: "gpt-4o", Name: "改名", Vendor: "新"}); err != nil {
		t.Fatalf("UpdateModel: %v", err)
	}
	got, _ = s.GetModel(ctx, "gpt-4o")
	if got.Name != "改名" || len(got.Channels) != 1 {
		t.Fatalf("UpdateModel 后 Name=%s Channels=%v", got.Name, got.Channels)
	}

	// DeleteChannel
	if err := s.DeleteChannel(ctx, "gpt-4o", "gpt-4o#openai"); err != nil {
		t.Fatalf("DeleteChannel: %v", err)
	}
	if chs, _ := s.ListChannels(ctx, "gpt-4o"); len(chs) != 0 {
		t.Fatal("DeleteChannel 后仍有通道")
	}

	// DeleteModel 级联清 channels
	if err := s.CreateChannel(ctx, "gpt-4o", c); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteModel(ctx, "gpt-4o"); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	var n int
	if err := db.Pool().QueryRow(ctx, `SELECT count(*) FROM maas_channels WHERE model_id=$1`, "gpt-4o").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("DeleteModel 未级联清 channels，残留 %d", n)
	}
}
