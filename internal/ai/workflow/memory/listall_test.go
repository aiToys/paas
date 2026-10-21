package memory

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/ai/workflow"
	"github.com/aitoys/paas/pkg/tenant"
)

// ListAll/ListAllRuns 跨租户语义（admin 总览）：无 tenant ctx 也能取全表，返回对象带 TenantID。
func TestListAllCrossTenant(t *testing.T) {
	s := NewStore()
	ctxA := tenant.WithTenant(context.Background(), "t-acme")
	ctxB := tenant.WithTenant(context.Background(), "t-globex")

	if _, err := s.Create(ctxA, workflow.WorkflowDef{Name: "wf-a", Nodes: []workflow.NodeDef{{ID: "e", Type: "end"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctxB, workflow.WorkflowDef{Name: "wf-b", Nodes: []workflow.NodeDef{{ID: "e", Type: "end"}}}); err != nil {
		t.Fatal(err)
	}
	da, _ := s.List(ctxA)
	if _, err := s.CreateRun(ctxA, workflow.WorkflowRun{WorkflowID: da[0].ID, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRun(ctxB, workflow.WorkflowRun{WorkflowID: "wf-b", Status: "succeeded"}); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListAll(context.Background())
	if err != nil || len(all) != 2 {
		t.Fatalf("ListAll 应聚齐 2 条（无 tenant ctx），got %d err %v", len(all), err)
	}
	runs, err := s.ListAllRuns(context.Background())
	if err != nil || len(runs) != 2 {
		t.Fatalf("ListAllRuns 应聚齐 2 条，got %d err %v", len(runs), err)
	}
	tenants := map[string]bool{}
	for _, r := range runs {
		tenants[r.TenantID] = true
	}
	if !tenants["t-acme"] || !tenants["t-globex"] {
		t.Fatalf("ListAllRuns 应含两租户运行: %+v", tenants)
	}
}
