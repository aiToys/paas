//go:build integration

package pg

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/aitoys/paas/internal/lane"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// open 创建测试 DB 连接并跑迁移；结束 DROP lanes 表避免残留（与其他模块 pg 测试同款模式）。
func open(t *testing.T) (*Store, func()) {
	t.Helper()
	dsn := os.Getenv("PAAS_TEST_PG_URL")
	if dsn == "" {
		t.Skip("PAAS_TEST_PG_URL 未设置，跳过 PG 集成测试")
	}
	db, err := storagepg.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("打开测试 DB 失败: %v", err)
	}
	if err := storagepg.RunMigrations(context.Background(), db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	cleanup := func() {
		// 必须连 schema_migrations 一起清：否则 version 停在 0035 而表已删，
		// 后续 RunMigrations 误认为已迁移不重建（与其他模块 resetSchema 同款教训）。
		db.Pool().Exec(context.Background(), `DROP TABLE IF EXISTS lanes CASCADE; DROP TABLE IF EXISTS schema_migrations CASCADE`)
		db.Close()
	}
	return NewStore(db), cleanup
}

func ctxT(tid string) context.Context { return tenant.WithTenant(context.Background(), tid) }

func mkLane(name string) lane.Lane {
	return lane.Lane{EnvID: "env-test", Name: name, Mode: lane.ModeStandard}
}

func TestLaneCRUDRoundTrip(t *testing.T) {
	s, cleanup := open(t)
	defer cleanup()

	l, err := s.Create(ctxT("t1"), mkLane("feature-x"))
	if err != nil || l.ID == "" || l.TenantID != "t1" {
		t.Fatalf("创建失败: %+v err=%v", l, err)
	}
	// 唯一冲突
	if _, err := s.Create(ctxT("t1"), mkLane("feature-x")); !errors.Is(err, lane.ErrLaneExists) {
		t.Fatalf("重复创建应 ErrLaneExists, got %v", err)
	}
	// Get roundtrip
	got, err := s.Get(ctxT("t1"), l.ID)
	if err != nil || got.Name != "feature-x" || got.Mode != lane.ModeStandard || got.Status != lane.StatusActive {
		t.Fatalf("Get roundtrip 失败: %+v err=%v", got, err)
	}
	// 跨租户不泄漏
	if _, err := s.Get(ctxT("t2"), l.ID); !errors.Is(err, lane.ErrLaneNotFound) {
		t.Fatalf("跨租户应 NotFound, got %v", err)
	}
	// GetByName
	if _, err := s.GetByName(ctxT("t1"), "env-test", "feature-x"); err != nil {
		t.Fatalf("GetByName 失败: %v", err)
	}
	// List envID 过滤
	if ls, _ := s.List(ctxT("t1"), "env-other"); len(ls) != 0 {
		t.Fatalf("env 过滤应 0 条, got %d", len(ls))
	}
	if ls, _ := s.List(ctxT("t1"), "env-test"); len(ls) != 1 {
		t.Fatalf("env-test 应 1 条, got %d", len(ls))
	}
}

func TestLaneUpdateClose(t *testing.T) {
	s, cleanup := open(t)
	defer cleanup()

	l, _ := s.Create(ctxT("t1"), mkLane("feature-y"))
	// Update 可变字段 + name/env 不可改
	in := l
	in.Name = "renamed"
	in.Description = "联调泳道"
	got, err := s.Update(ctxT("t1"), l.ID, in)
	if err != nil || got.Name != "feature-y" || got.Description != "联调泳道" {
		t.Fatalf("Update 失败: %+v err=%v", got, err)
	}
	// Close + 幂等
	got, err = s.Close(ctxT("t1"), l.ID)
	if err != nil || got.Status != lane.StatusClosed {
		t.Fatalf("Close 失败: %+v err=%v", got, err)
	}
	if got2, err := s.Close(ctxT("t1"), l.ID); err != nil || got2.Status != lane.StatusClosed {
		t.Fatalf("重复 Close 应幂等: %+v err=%v", got2, err)
	}
	// Update mode 改 permanent
	l2, _ := s.Create(ctxT("t1"), mkLane("feature-z"))
	upd := l2
	upd.Mode = lane.ModePermanent
	if got3, err := s.Update(ctxT("t1"), l2.ID, upd); err != nil || got3.Mode != lane.ModePermanent {
		t.Fatalf("mode 改 permanent 失败: %+v err=%v", got3, err)
	}
}

func TestLaneEnsureByName(t *testing.T) {
	s, cleanup := open(t)
	defer cleanup()

	// 懒建 standard
	got, err := s.EnsureByName(ctxT("t1"), "env-test", "feature-e")
	if err != nil || got.Mode != lane.ModeStandard || got.ID == "" {
		t.Fatalf("懒建失败: %+v err=%v", got, err)
	}
	// 改 permanent 后 Ensure 不覆盖
	upd := got
	upd.Mode = lane.ModePermanent
	s.Update(ctxT("t1"), got.ID, upd)
	got2, err := s.EnsureByName(ctxT("t1"), "env-test", "feature-e")
	if err != nil || got2.Mode != lane.ModePermanent || got2.ID != got.ID {
		t.Fatalf("Ensure 不应覆盖 permanent: %+v err=%v", got2, err)
	}
	// 非法名拒绝
	if _, err := s.EnsureByName(ctxT("t1"), "env-test", "9bad"); !errors.Is(err, lane.ErrLaneNameInvalid) {
		t.Fatalf("非法名应拒绝, got %v", err)
	}
	// 租户隔离：t2 Ensure 同名应建独立行
	got3, err := s.EnsureByName(ctxT("t2"), "env-test", "feature-e")
	if err != nil || got3.TenantID != "t2" || got3.ID == got.ID {
		t.Fatalf("跨租户应独立: %+v err=%v", got3, err)
	}
}
