package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/internal/ai/prompt"
	"github.com/aitoys/paas/internal/ai/prompt/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

func ctxT(tid string) context.Context { return tenant.WithTenant(context.Background(), tid) }

func TestVersionIncrementAndActive(t *testing.T) {
	s := memory.NewStore()
	ctx := ctxT("t-a")
	p1, err := s.Create(ctx, prompt.Prompt{Name: "greet", Template: "v1 {{.name}}", Variables: []string{"name"}})
	if err != nil {
		t.Fatalf("Create v1: %v", err)
	}
	if p1.Version != 1 || !p1.Active {
		t.Fatalf("首版应 version=1 active=true，got %+v", p1)
	}
	p2, err := s.Create(ctx, prompt.Prompt{Name: "greet", Template: "v2"})
	if err != nil {
		t.Fatalf("Create v2: %v", err)
	}
	if p2.Version != 2 || !p2.Active {
		t.Fatalf("新版应 version=2 active=true，got %+v", p2)
	}
	// v1 应已 deactive
	got1, _ := s.Get(ctx, p1.ID)
	if got1.Active {
		t.Fatalf("新版创建后旧版应 deactive")
	}
	// GetActive 返 v2
	active, err := s.GetActive(ctx, "greet")
	if err != nil {
		t.Fatalf("GetActive: %v", err)
	}
	if active.Version != 2 {
		t.Fatalf("active 应是 v2，got v%d", active.Version)
	}
}

func TestSetActiveSwitch(t *testing.T) {
	s := memory.NewStore()
	ctx := ctxT("t-a")
	p1, _ := s.Create(ctx, prompt.Prompt{Name: "p", Template: "v1"})
	p2, _ := s.Create(ctx, prompt.Prompt{Name: "p", Template: "v2"})
	// 激活 v1
	if _, err := s.SetActive(ctx, p1.ID); err != nil {
		t.Fatalf("SetActive v1: %v", err)
	}
	active, _ := s.GetActive(ctx, "p")
	if active.Version != 1 {
		t.Fatalf("应切回 v1，got v%d", active.Version)
	}
	got2, _ := s.Get(ctx, p2.ID)
	if got2.Active {
		t.Fatalf("v2 应 deactive")
	}
}

func TestDeleteActivePromotesLatest(t *testing.T) {
	s := memory.NewStore()
	ctx := ctxT("t-a")
	p1, _ := s.Create(ctx, prompt.Prompt{Name: "p", Template: "v1"})
	_, _ = s.Create(ctx, prompt.Prompt{Name: "p", Template: "v2"})
	p3, _ := s.Create(ctx, prompt.Prompt{Name: "p", Template: "v3"}) // active=v3
	// 删 active v3 → 应激活 v2（最新剩余）
	if err := s.Delete(ctx, p3.ID); err != nil {
		t.Fatalf("Delete v3: %v", err)
	}
	active, err := s.GetActive(ctx, "p")
	if err != nil {
		t.Fatalf("删 active 后应有新 active: %v", err)
	}
	if active.Version != 2 {
		t.Fatalf("应激活 v2，got v%d", active.Version)
	}
	// 删非 active 不影响
	if err := s.Delete(ctx, p1.ID); err != nil {
		t.Fatalf("Delete v1: %v", err)
	}
	active, _ = s.GetActive(ctx, "p")
	if active.Version != 2 {
		t.Fatalf("删非 active 不应改变 active")
	}
}

func TestTenantIsolation(t *testing.T) {
	s := memory.NewStore()
	p1, _ := s.Create(ctxT("t-a"), prompt.Prompt{Name: "p", Template: "x"})
	if _, err := s.Get(ctxT("t-b"), p1.ID); !errors.Is(err, prompt.ErrPromptNotFound) {
		t.Fatalf("跨租户应 not found")
	}
	// t-b 同名独立版本
	pb, err := s.Create(ctxT("t-b"), prompt.Prompt{Name: "p", Template: "y"})
	if err != nil {
		t.Fatalf("跨租户同名应允许: %v", err)
	}
	if pb.Version != 1 {
		t.Fatalf("跨租户版本独立，t-b 首版应 v1，got v%d", pb.Version)
	}
}

func TestNoActive(t *testing.T) {
	s := memory.NewStore()
	if _, err := s.GetActive(ctxT("t-a"), "none"); !errors.Is(err, prompt.ErrNoActivePrompt) {
		t.Fatalf("无激活应 ErrNoActivePrompt，got %v", err)
	}
}
