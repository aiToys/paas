//go:build integration

package pg

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/ai/skill"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

// 使用独立 search_path/schema，不清空共享 public 或其他包的测试数据。
func TestPGSkillStateCollisionAndIsolation(t *testing.T) {
	dsn := os.Getenv("PAAS_TEST_PG_URL")
	if dsn == "" {
		t.Skip("PAAS_TEST_PG_URL 未设置")
	}
	ctx := context.Background()
	admin, err := storagepg.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("skill_test_%d", time.Now().UnixNano())
	if _, err := admin.Pool().Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Pool().Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	db, err := storagepg.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := storagepg.RunMigrations(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := NewStore(db)
	a, b := tenant.WithTenant(ctx, "a"), tenant.WithTenant(ctx, "b")
	one, err := s.Create(a, skill.Skill{ID: "collision", Name: "private", Instructions: "original", Enabled: false})
	if err != nil || one.Enabled {
		t.Fatalf("禁用状态未保留：%+v %v", one, err)
	}
	if _, err := s.Create(b, skill.Skill{ID: one.ID, Name: "other", Instructions: "replace", Enabled: true}); err != skill.ErrSkillExists {
		t.Fatalf("主键碰撞应拒绝：%v", err)
	}
	got, err := s.Get(a, one.ID)
	if err != nil || got.Instructions != "original" || got.Enabled {
		t.Fatalf("原记录被覆盖：%+v %v", got, err)
	}
	if _, err := s.Get(b, one.ID); err != skill.ErrSkillNotFound {
		t.Fatalf("跨租户读取：%v", err)
	}
	got.Enabled = true
	got.InstalledFrom = "builtin:test@1.0.0#hash"
	got, err = s.Update(a, got)
	if err != nil || !got.Enabled || got.InstalledFrom == "" {
		t.Fatalf("状态更新失败：%+v %v", got, err)
	}
	if err := s.Delete(b, one.ID); err != skill.ErrSkillNotFound {
		t.Fatalf("跨租户删除：%v", err)
	}
	if err := s.Delete(a, one.ID); err != nil {
		t.Fatal(err)
	}
}
