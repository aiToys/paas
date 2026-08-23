package change

import (
	"context"
	"testing"
	"time"

	"github.com/aitoys/paas/pkg/tenant"
)

// fakeRunLister 通知聚合测试用。
type fakeRunLister struct {
	items []RunStatusItem
	err   error
}

func (f *fakeRunLister) ListRunStatuses(ctx context.Context) ([]RunStatusItem, error) {
	return f.items, f.err
}

// TestNotifications 聚合正确性：批次按状态映射通知 + run failed/paused + severity 排序 + 跨租户隔离。
func TestNotifications(t *testing.T) {
	store := NewMemoryStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	// 批次三态：conflict / testing / tested（待审批）。
	// CreateBatch 强制初始 collecting，经 UpdateBatch 推进状态。
	now := time.Now().UTC()
	for _, b := range []IntegrationBatch{
		{AppID: "app-1", RepoID: "r1", Title: "冲突批", Branch: "integration/n1", Status: BatchConflict, CreatedAt: now},
		{AppID: "app-1", RepoID: "r1", Title: "测试批", Branch: "integration/n2", Status: BatchTesting, CreatedAt: now},
		{AppID: "app-1", RepoID: "r1", Title: "待审批", Branch: "integration/n3", Status: BatchTested, CreatedAt: now},
	} {
		created, err := store.CreateBatch(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		created.Status = b.Status
		if _, err := store.UpdateBatch(ctx, created); err != nil {
			t.Fatal(err)
		}
	}

	runs := &fakeRunLister{items: []RunStatusItem{
		{ID: "run-f", AppID: "app-1", Status: "failed", Current: "构建"},
		{ID: "run-p", AppID: "app-1", Status: "paused", Current: "上线审批"},
		{ID: "run-s", AppID: "app-1", Status: "succeeded", Current: ""}, // 成功不通知
	}}

	notifs, err := Notifications(ctx, store, runs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(notifs) != 5 {
		t.Fatalf("应 5 条通知（3 批次 + 2 run），got %d: %+v", len(notifs), notifs)
	}
	// severity 排序：error(2) -> warning(2) -> info(1)
	wantSev := []string{"error", "error", "warning", "warning", "info"}
	for i, n := range notifs {
		if n.Severity != wantSev[i] {
			t.Fatalf("第 %d 条 severity 应 %s，got %s", i, wantSev[i], n.Severity)
		}
	}
	// ID 稳定（前端已读标记依赖）
	if notifs[0].ID == notifs[1].ID {
		t.Fatal("通知 ID 应唯一")
	}
	// failed run 通知存在且 target 正确（error 组内顺序无承诺，按 ID 查）
	found := false
	for _, n := range notifs {
		if n.TargetType == "run" && n.TargetID == "run-f" && n.Severity == "error" {
			found = true
		}
	}
	if !found {
		t.Fatalf("应含 failed run 通知，got %+v", notifs)
	}

	// runLister=nil 降级：只通知批次侧
	only, err := Notifications(ctx, store, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 3 {
		t.Fatalf("无 runLister 应 3 条，got %d", len(only))
	}

	// 跨租户隔离：批次侧返空；run 侧租户过滤由 bridge 的 ListRuns ctx 保证
	// （此处 fakeRunLister 不感知租户，真实 bridge 强制 tenant_id 过滤，
	// 单元层不重复断言——见 runTriggerBridge.ListRunStatuses 调 pipeline.ListRuns）。
	globex := tenant.WithTenant(context.Background(), "t-globex")
	globexRuns := &fakeRunLister{} // 模拟真实 bridge：跨租户返空
	empty, err := Notifications(globex, store, globexRuns, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("跨租户应返空，got %d", len(empty))
	}
}

// fakeAlertLister 可控告警源。
type fakeAlertLister struct{ items []AlertItem }

func (f *fakeAlertLister) ListAlertItems(ctx context.Context) ([]AlertItem, error) {
	return f.items, nil
}

// TestNotificationsAlerts：firing critical→error、pending→info、resolved 不进通知。
func TestNotificationsAlerts(t *testing.T) {
	store := NewMemoryStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	alerts := &fakeAlertLister{items: []AlertItem{
		{RuleName: "CPU 高", TargetType: "app", TargetID: "app-1", MetricName: "cpu",
			Severity: "critical", Status: "firing", At: "2026-08-23T10:00:00Z"},
		{RuleName: "观察中", TargetType: "dataservice", TargetID: "ds-1", MetricName: "mem",
			Severity: "warning", Status: "pending", At: "2026-08-23T10:01:00Z"},
		{RuleName: "已恢复", TargetType: "app", TargetID: "app-2", MetricName: "rps",
			Severity: "warning", Status: "resolved", At: "2026-08-23T10:02:00Z"},
	}}
	notifs, err := Notifications(ctx, store, nil, alerts)
	if err != nil {
		t.Fatalf("Notifications: %v", err)
	}
	var firing, pending, resolved int
	for _, n := range notifs {
		switch n.Type {
		case NotifAlertFiring:
			firing++
			if n.Severity != "error" {
				t.Fatalf("critical firing 应 error, got %s", n.Severity)
			}
		case NotifAlertPending:
			pending++
		case "alert_resolved":
			resolved++
		}
	}
	if firing != 1 || pending != 1 || resolved != 0 {
		t.Fatalf("应 firing=1 pending=1 resolved=0, got %d/%d/%d", firing, pending, resolved)
	}
}
