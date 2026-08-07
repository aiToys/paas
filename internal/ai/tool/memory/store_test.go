package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/internal/ai/tool"
	"github.com/aitoys/paas/internal/ai/tool/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

func ctxWithTenant(tid string) context.Context { return tenant.WithTenant(context.Background(), tid) }

func TestCreateAndGet(t *testing.T) {
	s := memory.NewStore()
	t1 := tool.Tool{Name: "search", Type: tool.TypeMCP, Config: map[string]string{"serverURL": "http://srv"}}
	saved, err := s.Create(ctxWithTenant("t-a"), t1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if saved.ID == "" || saved.TenantID != "t-a" {
		t.Fatalf("Create 回填错误: %+v", saved)
	}
	got, err := s.Get(ctxWithTenant("t-a"), saved.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "search" || got.Config["serverURL"] != "http://srv" {
		t.Fatalf("Get 数据错: %+v", got)
	}
	// 读返深拷贝：改 Config 不污染 store
	got.Config["serverURL"] = "mutated"
	got2, _ := s.Get(ctxWithTenant("t-a"), saved.ID)
	if got2.Config["serverURL"] != "http://srv" {
		t.Fatalf("深拷贝失败，store 被外部修改污染")
	}
}

func TestTenantIsolation(t *testing.T) {
	s := memory.NewStore()
	t1, _ := s.Create(ctxWithTenant("t-a"), tool.Tool{Name: "a-tool", Type: tool.TypeBuiltin, Config: map[string]string{"handler": "kb"}})
	// t-b 看不到 t-a 的工具
	if _, err := s.Get(ctxWithTenant("t-b"), t1.ID); !errors.Is(err, tool.ErrToolNotFound) {
		t.Fatalf("跨租户应 not found，got %v", err)
	}
	// t-b 列表不含 t-a 工具
	list, _ := s.List(ctxWithTenant("t-b"))
	if len(list) != 0 {
		t.Fatalf("t-b 列表应为空，got %d", len(list))
	}
	// 无 tenant ctx 统一 not found（不泄漏）
	if _, err := s.Get(context.Background(), t1.ID); !errors.Is(err, tool.ErrToolNotFound) {
		t.Fatalf("无 tenant 应 not found，got %v", err)
	}
}

func TestNameUnique(t *testing.T) {
	s := memory.NewStore()
	ctx := ctxWithTenant("t-a")
	_, _ = s.Create(ctx, tool.Tool{Name: "dup", Type: tool.TypeBuiltin, Config: map[string]string{"handler": "x"}})
	_, err := s.Create(ctx, tool.Tool{Name: "dup", Type: tool.TypeBuiltin, Config: map[string]string{"handler": "y"}})
	if !errors.Is(err, tool.ErrToolExists) {
		t.Fatalf("重名应 ErrToolExists，got %v", err)
	}
	// 跨租户同名允许
	if _, err := s.Create(ctxWithTenant("t-b"), tool.Tool{Name: "dup", Type: tool.TypeBuiltin, Config: map[string]string{"handler": "z"}}); err != nil {
		t.Fatalf("跨租户同名应允许，got %v", err)
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		t       tool.Tool
		wantErr bool
	}{
		{"空 name", tool.Tool{Type: tool.TypeMCP, Config: map[string]string{"serverURL": "x"}}, true},
		{"坏 type", tool.Tool{Name: "x", Type: "bad"}, true},
		{"mcp 无 serverURL", tool.Tool{Name: "x", Type: tool.TypeMCP, Config: map[string]string{}}, true},
		{"http 无 endpoint", tool.Tool{Name: "x", Type: tool.TypeHTTP, Config: map[string]string{}}, true},
		{"builtin 无 handler", tool.Tool{Name: "x", Type: tool.TypeBuiltin, Config: map[string]string{}}, true},
		{"合法 mcp", tool.Tool{Name: "x", Type: tool.TypeMCP, Config: map[string]string{"serverURL": "http://s"}}, false},
	}
	s := memory.NewStore()
	for _, c := range cases {
		_, err := s.Create(ctxWithTenant("t-a"), c.t)
		if (err != nil) != c.wantErr {
			t.Fatalf("%s: wantErr=%v got err=%v", c.name, c.wantErr, err)
		}
	}
}

func TestUpdateAndDelete(t *testing.T) {
	s := memory.NewStore()
	ctx := ctxWithTenant("t-a")
	t1, _ := s.Create(ctx, tool.Tool{Name: "orig", Type: tool.TypeBuiltin, Config: map[string]string{"handler": "h"}})
	t1.Description = "updated"
	t1.Config["handler"] = "h2"
	up, err := s.Update(ctx, t1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if up.Description != "updated" || up.Config["handler"] != "h2" {
		t.Fatalf("Update 数据错: %+v", up)
	}
	if up.CreatedAt.IsZero() {
		t.Fatalf("Update 应保留 created_at")
	}
	if err := s.Delete(ctx, t1.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(ctx, t1.ID); !errors.Is(err, tool.ErrToolNotFound) {
		t.Fatalf("删后应 not found")
	}
	if err := s.Delete(ctx, t1.ID); !errors.Is(err, tool.ErrToolNotFound) {
		t.Fatalf("重复删应 not found")
	}
}
