//go:build integration

package pg

import (
	"context"
	"os"
	"testing"

	"github.com/aitoys/paas/internal/devops/change"
	"github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

func newTestDB(t *testing.T) *pg.DB {
	t.Helper()
	dsn := os.Getenv("PAAS_TEST_PG_URL")
	if dsn == "" {
		t.Skip("PAAS_TEST_PG_URL 未设置，跳过")
	}
	ctx := context.Background()
	db, err := pg.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("打开测试 DB 失败: %v", err)
	}
	t.Cleanup(db.Close)
	if err := pg.RunMigrations(ctx, db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { resetSchema(t, db) })
	return db
}

func resetSchema(t *testing.T, db *pg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`DROP TABLE IF EXISTS changes, integration_batches CASCADE; DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func TestPGChangeRoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	c, err := s.CreateChange(ctx, change.Change{AppID: "app-1", RepoID: "r1", Title: "导出", Type: change.ChangeFeat, Branch: "feat/export", BranchCreated: true, BaseBranch: "main"})
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	got, err := s.GetChange(ctx, c.ID)
	if err != nil || got.Title != "导出" || !got.BranchCreated || got.Status != change.ChangeOpen {
		t.Fatalf("往返不一致: %+v err=%v", got, err)
	}
	// Update：入批 + 状态推进
	got.Status = change.ChangeIntegrated
	got.BatchID = "batch-1"
	got.ConflictWith = "chg-prev"
	if _, err = s.UpdateChange(ctx, got); err != nil {
		t.Fatalf("UpdateChange: %v", err)
	}
	again, _ := s.GetChange(ctx, c.ID)
	if again.Status != change.ChangeIntegrated || again.BatchID != "batch-1" || again.ConflictWith != "chg-prev" {
		t.Fatalf("更新往返失败: %+v", again)
	}
	// 分支唯一
	if _, err = s.CreateChange(ctx, change.Change{AppID: "app-1", RepoID: "r1", Title: "dup", Type: change.ChangeFeat, Branch: "feat/export"}); err != change.ErrChangeExists {
		t.Fatalf("分支唯一应 ErrChangeExists, got %v", err)
	}
	// 跨租户
	gctx := tenant.WithTenant(context.Background(), "t-globex")
	if _, err = s.GetChange(gctx, c.ID); err != change.ErrChangeNotFound {
		t.Fatalf("跨租户 not found, got %v", err)
	}
}

func TestPGBatchRoundTrip(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	b, err := s.CreateBatch(ctx, change.IntegrationBatch{AppID: "app-1", RepoID: "r1", Title: "集成", Branch: "integration/20260815-1"})
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	b.Status = change.BatchReleased
	b.ChangeIDs = []string{"chg-1", "chg-2", "chg-3"}
	b.ReleaseIDs = []string{"rel-1"}
	if _, err = s.UpdateBatch(ctx, b); err != nil {
		t.Fatalf("UpdateBatch: %v", err)
	}
	got, _ := s.GetBatch(ctx, b.ID)
	if len(got.ChangeIDs) != 3 || got.ChangeIDs[2] != "chg-3" || len(got.ReleaseIDs) != 1 || got.Status != change.BatchReleased {
		t.Fatalf("JSONB 有序往返失败: %+v", got)
	}
	// 状态过滤
	list, _ := s.ListBatches(ctx, "app-1", change.BatchReleased)
	if len(list) != 1 {
		t.Fatalf("状态过滤应 1 条, got %d", len(list))
	}
}
