package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

func opsAcmeCtx() context.Context { return tenant.WithTenant(context.Background(), "t-acme") }

func svcWorkload() workload.Workload {
	return workload.Workload{ID: "wl-m1", TenantID: "t-acme", AppID: "app-1", EnvID: "env-test", Type: workload.TypeService, Name: "svc-m", Image: "nginx:1.27", Replicas: 3}
}

func seedSvc(t *testing.T, s *Store) workload.Workload {
	t.Helper()
	w := svcWorkload()
	if err := s.Create(opsAcmeCtx(), w); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestOpsCreateRestartWritesTokenToMainStore(t *testing.T) {
	store := NewStore()
	w := seedSvc(t, store)
	ops := NewOpsStore(store)
	op, created, err := ops.CreateRestart(opsAcmeCtx(), w, "cfg reload", "u1", "idem-1")
	if err != nil || !created {
		t.Fatalf("受理失败: created=%v err=%v", created, err)
	}
	if op.TargetToken == "" {
		t.Fatal("restart 受理应生成 token")
	}
	// token 必须落到主 store（Get 可见）——前端/runtime 读期望状态同源。
	got, err := store.Get(opsAcmeCtx(), w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RestartToken != op.TargetToken {
		t.Fatalf("主 store token 应与操作一致: %q != %q", got.RestartToken, op.TargetToken)
	}
	// 幂等：同 idemKey 返回既有记录且 created=false。
	again, created2, err := ops.CreateRestart(opsAcmeCtx(), w, "retry", "u1", "idem-1")
	if err != nil || created2 {
		t.Fatalf("幂等命中应 created=false: created=%v err=%v", created2, err)
	}
	if again.ID != op.ID {
		t.Fatalf("幂等应返回同记录: %s != %s", again.ID, op.ID)
	}
}

func TestOpsActiveConflict(t *testing.T) {
	store := NewStore()
	w := seedSvc(t, store)
	ops := NewOpsStore(store)
	if _, _, err := ops.CreateRestart(opsAcmeCtx(), w, "first", "u1", ""); err != nil {
		t.Fatal(err)
	}
	_, _, err := ops.CreateRestart(opsAcmeCtx(), w, "second", "u1", "")
	if !errors.Is(err, workload.ErrOpConflict) {
		t.Fatalf("活跃操作存在时应返 ErrOpConflict，got %v", err)
	}
	// 终态后可再次受理。
	active, err := ops.Active(opsAcmeCtx(), w.ID)
	if err != nil || active.ID == "" {
		t.Fatalf("Active 应返回活跃操作: %+v err=%v", active, err)
	}
	active.Status = workload.OpStatusSucceeded
	if err := ops.UpdateStatus(opsAcmeCtx(), active); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ops.CreateRestart(opsAcmeCtx(), w, "after-done", "u1", ""); err != nil {
		t.Fatalf("终态后应可再次受理，got %v", err)
	}
}

func TestOpsUpdateStatusTerminalProtection(t *testing.T) {
	store := NewStore()
	w := seedSvc(t, store)
	ops := NewOpsStore(store)
	op, _, err := ops.CreateRestart(opsAcmeCtx(), w, "r", "u1", "")
	if err != nil {
		t.Fatal(err)
	}
	op.Status = workload.OpStatusSucceeded
	if err := ops.UpdateStatus(opsAcmeCtx(), op); err != nil {
		t.Fatal(err)
	}
	// 终态后写入被忽略（不复活）。
	op.Status = workload.OpStatusFailed
	if err := ops.UpdateStatus(opsAcmeCtx(), op); err != nil {
		t.Fatal(err)
	}
	got, err := ops.Get(opsAcmeCtx(), w.ID, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != workload.OpStatusSucceeded {
		t.Fatalf("终态保护失效: %s", got.Status)
	}
}

func TestOpsListByWorkloadAndListActive(t *testing.T) {
	store := NewStore()
	w := seedSvc(t, store)
	ops := NewOpsStore(store)
	op1, _, _ := ops.CreateRestart(opsAcmeCtx(), w, "r1", "u1", "")
	op1.Status = workload.OpStatusSucceeded
	_ = ops.UpdateStatus(opsAcmeCtx(), op1)
	op2, _, _ := ops.CreateRecreate(opsAcmeCtx(), w, "pod-a", "uid-a", "r2", "u1", "")
	list, err := ops.ListByWorkload(opsAcmeCtx(), w.ID, 10, 0)
	if err != nil || len(list) != 2 {
		t.Fatalf("ListByWorkload 应返回 2 条: len=%d err=%v", len(list), err)
	}
	if list[0].ID != op2.ID {
		t.Fatalf("时间倒序：最新在前，got %s", list[0].ID)
	}
	active, err := ops.ListActive(context.Background())
	if err != nil || len(active) != 1 || active[0].ID != op2.ID {
		t.Fatalf("ListActive 应只含非终态: %+v err=%v", active, err)
	}
}
