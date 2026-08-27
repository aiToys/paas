package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/internal/lane"
	"github.com/aitoys/paas/pkg/tenant"
)

func ctxT(tid string) context.Context { return tenant.WithTenant(context.Background(), tid) }

func mkLane(name string) lane.Lane {
	return lane.Lane{EnvID: "env-1", Name: name, Mode: lane.ModeStandard}
}

func TestCreateListGet(t *testing.T) {
	s := NewStore()
	l, err := s.Create(ctxT("t1"), mkLane("feature-x"))
	if err != nil || l.ID == "" || l.TenantID != "t1" || l.Status != lane.StatusActive {
		t.Fatalf("创建失败: %+v err=%v", l, err)
	}
	// 重复创建冲突
	if _, err := s.Create(ctxT("t1"), mkLane("feature-x")); !errors.Is(err, lane.ErrLaneExists) {
		t.Fatalf("重复创建应 ErrLaneExists, got %v", err)
	}
	// 同名不同环境合法
	if _, err := s.Create(ctxT("t1"), func() lane.Lane { l := mkLane("feature-x"); l.EnvID = "env-2"; return l }()); err != nil {
		t.Fatalf("不同环境同名应合法: %v", err)
	}
	// 同名不同租户合法（隔离）
	if _, err := s.Create(ctxT("t2"), mkLane("feature-x")); err != nil {
		t.Fatalf("跨租户同名应合法: %v", err)
	}
	// List 租户过滤
	got, _ := s.List(ctxT("t1"), "")
	if len(got) != 2 {
		t.Fatalf("t1 应有 2 条, got %d", len(got))
	}
	got, _ = s.List(ctxT("t1"), "env-1")
	if len(got) != 1 {
		t.Fatalf("env-1 过滤应 1 条, got %d", len(got))
	}
	// Get 跨租户不泄漏
	if _, err := s.Get(ctxT("t2"), l.ID); !errors.Is(err, lane.ErrLaneNotFound) {
		t.Fatalf("跨租户 Get 应 NotFound, got %v", err)
	}
	// GetByName
	if _, err := s.GetByName(ctxT("t1"), "env-1", "feature-x"); err != nil {
		t.Fatalf("GetByName 失败: %v", err)
	}
}

func TestCreateTenantFromCtx(t *testing.T) {
	s := NewStore()
	in := mkLane("feature-y")
	in.TenantID = "t-evil" // 请求体伪造租户应被忽略
	l, err := s.Create(ctxT("t1"), in)
	if err != nil || l.TenantID != "t1" {
		t.Fatalf("租户应以 ctx 为准: %+v err=%v", l, err)
	}
}

func TestUpdateImmutableFields(t *testing.T) {
	s := NewStore()
	l, _ := s.Create(ctxT("t1"), mkLane("feature-x"))
	in := l
	in.Name = "renamed"
	in.EnvID = "env-9"
	in.Description = "联调"
	got, err := s.Update(ctxT("t1"), l.ID, in)
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if got.Name != "feature-x" || got.EnvID != "env-1" {
		t.Fatalf("name/env 不可改: %+v", got)
	}
	if got.Description != "联调" {
		t.Fatalf("description 应更新: %+v", got)
	}
	// 非法 mode 拒绝
	in = l
	in.Mode = "bogus"
	if _, err := s.Update(ctxT("t1"), l.ID, in); err == nil {
		t.Fatal("非法 mode 应拒绝")
	}
}

func TestCloseIdempotent(t *testing.T) {
	s := NewStore()
	l, _ := s.Create(ctxT("t1"), mkLane("feature-x"))
	got, err := s.Close(ctxT("t1"), l.ID)
	if err != nil || got.Status != lane.StatusClosed {
		t.Fatalf("关闭失败: %+v err=%v", got, err)
	}
	// 幂等
	got2, err := s.Close(ctxT("t1"), l.ID)
	if err != nil || got2.Status != lane.StatusClosed {
		t.Fatalf("重复关闭应幂等: %+v err=%v", got2, err)
	}
}

func TestEnsureByName(t *testing.T) {
	s := NewStore()
	// 懒建 standard
	got, err := s.EnsureByName(ctxT("t1"), "env-1", "feature-x")
	if err != nil || got.Mode != lane.ModeStandard || got.ID == "" {
		t.Fatalf("懒建失败: %+v err=%v", got, err)
	}
	// 已存在（含 permanent）不覆盖
	s.Update(ctxT("t1"), got.ID, lane.Lane{Mode: lane.ModePermanent})
	got2, err := s.EnsureByName(ctxT("t1"), "env-1", "feature-x")
	if err != nil || got2.Mode != lane.ModePermanent || got2.ID != got.ID {
		t.Fatalf("Ensure 不应覆盖 permanent: %+v err=%v", got2, err)
	}
	// 非法名拒绝
	if _, err := s.EnsureByName(ctxT("t1"), "env-1", "9bad"); !errors.Is(err, lane.ErrLaneNameInvalid) {
		t.Fatalf("非法名应拒绝, got %v", err)
	}
}

func TestNoTenantRejected(t *testing.T) {
	s := NewStore()
	if _, err := s.List(context.Background(), ""); err == nil {
		t.Fatal("无租户 ctx 应拒绝")
	}
	if _, err := s.Create(context.Background(), mkLane("x")); err == nil {
		t.Fatal("无租户 ctx 应拒绝")
	}
}
