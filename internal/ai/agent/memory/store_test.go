package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/internal/ai/agent"
	"github.com/aitoys/paas/internal/ai/agent/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

func ctxT(tid string) context.Context { return tenant.WithTenant(context.Background(), tid) }

func TestCreateAndGet(t *testing.T) {
	s := memory.NewStore()
	a, err := s.Create(ctxT("t-a"), agent.Agent{Name: "bot", Model: "glm-5.2", SystemPrompt: "你是助手"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if a.ID == "" || a.TenantID != "t-a" || a.MaxSteps != agent.DefaultMaxSteps || !a.Enabled {
		t.Fatalf("Create 回填错: %+v", a)
	}
	// 读返深拷贝
	a.Tools = append(a.Tools, "t1")
	a2, _ := s.Get(ctxT("t-a"), a.ID)
	if len(a2.Tools) != 0 {
		t.Fatalf("深拷贝失败")
	}
}

func TestValidate(t *testing.T) {
	s := memory.NewStore()
	cases := []struct {
		name    string
		a       agent.Agent
		wantErr bool
	}{
		{"空 name", agent.Agent{Model: "m"}, true},
		{"空 model", agent.Agent{Name: "x"}, true},
		{"合法", agent.Agent{Name: "x", Model: "m"}, false},
	}
	for _, c := range cases {
		_, err := s.Create(ctxT("t-a"), c.a)
		if (err != nil) != c.wantErr {
			t.Fatalf("%s: wantErr=%v got=%v", c.name, c.wantErr, err)
		}
	}
}

func TestTenantIsolationAndUnique(t *testing.T) {
	s := memory.NewStore()
	a, _ := s.Create(ctxT("t-a"), agent.Agent{Name: "dup", Model: "m"})
	if _, err := s.Get(ctxT("t-b"), a.ID); !errors.Is(err, agent.ErrAgentNotFound) {
		t.Fatalf("跨租户应 not found")
	}
	if _, err := s.Create(ctxT("t-a"), agent.Agent{Name: "dup", Model: "m"}); !errors.Is(err, agent.ErrAgentExists) {
		t.Fatalf("重名应 ErrAgentExists")
	}
	// 跨租户同名允许
	if _, err := s.Create(ctxT("t-b"), agent.Agent{Name: "dup", Model: "m"}); err != nil {
		t.Fatalf("跨租户同名应允许: %v", err)
	}
}

func TestVirtualModelID(t *testing.T) {
	a := agent.Agent{ID: "abc"}
	if a.VirtualModelID() != "agent:abc" {
		t.Fatalf("VirtualModelID 错: %s", a.VirtualModelID())
	}
}
