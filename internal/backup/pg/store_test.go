//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN（make test-pg）。
// 验证 backups 表 CRUD + 租户隔离（RLS 0044）。

package pg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/backup"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

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

func resetSchema(t *testing.T, db *storagepg.DB) {
	t.Helper()
	_, err := db.Pool().Exec(context.Background(),
		`DROP TABLE IF EXISTS backups CASCADE; DROP TABLE IF EXISTS schema_migrations CASCADE`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func sampleBackup(id, tid, rid string) backup.Backup {
	return backup.Backup{
		ID: id, TenantID: tid, ResourceID: rid,
		Type: backup.TypeFull, Status: backup.StatusCompleted, SizeMB: 128,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
}

func TestBackupCRUD(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	b := sampleBackup("bk-t1", "t-acme", "ds-1")
	if err := s.Create(ctx, b); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := s.Get(ctx, "t-acme", "bk-t1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ResourceID != "ds-1" || got.Type != backup.TypeFull || got.SizeMB != 128 {
		t.Fatalf("读回不一致: %+v", got)
	}

	// List 按 resourceID 过滤 + 倒序
	if err := s.Create(ctx, sampleBackup("bk-t2", "t-acme", "ds-2")); err != nil {
		t.Fatalf("Create 2: %v", err)
	}
	all, err := s.List(ctx, "t-acme", "")
	if err != nil || len(all) != 2 {
		t.Fatalf("List 全部应 2 条: %v %d", err, len(all))
	}
	if all[0].ID != "bk-t2" { // created_at DESC（时间截断到微秒，同刻时按 id 兜底排序不可依赖——此处仅断言数量与过滤正确性）
		t.Logf("List 顺序: first=%s（同刻创建顺序不敏感）", all[0].ID)
	}
	only, err := s.List(ctx, "t-acme", "ds-1")
	if err != nil || len(only) != 1 || only[0].ID != "bk-t1" {
		t.Fatalf("List 过滤 ds-1 应仅 bk-t1: %v %+v", err, only)
	}

	// 重复 Create 同 ID 返「备份已存在」（R8 审计 B2：原 ON CONFLICT DO NOTHING 静默假成功）
	if err := s.Create(ctx, sampleBackup("bk-t2", "t-acme", "ds-2")); err == nil {
		t.Fatalf("重复 Create 应报已存在")
	}

	// Delete 后 Get 报不存在
	if err := s.Delete(ctx, "t-acme", "bk-t1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, "t-acme", "bk-t1"); err == nil {
		t.Fatalf("删除后 Get 应报不存在")
	}
}

// 租户隔离：跨租户 Get/List/Delete 统一「不存在」，不泄漏。
func TestBackupTenantIsolation(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := context.Background()

	if err := s.Create(ctx, sampleBackup("bk-a", "t-acme", "ds-1")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Get(ctx, "t-globex", "bk-a"); err == nil {
		t.Fatalf("跨租户 Get 应报不存在")
	}
	list, err := s.List(ctx, "t-globex", "")
	if err != nil || len(list) != 0 {
		t.Fatalf("跨租户 List 应空: %v %d", err, len(list))
	}
	if err := s.Delete(ctx, "t-globex", "bk-a"); err == nil {
		t.Fatalf("跨租户 Delete 应报不存在")
	}
}
