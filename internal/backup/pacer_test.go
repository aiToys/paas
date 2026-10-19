package backup

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/pkg/tenant"
)

// fakeExecutor 记录调用并返回预设 Probe 结果。
type fakeExecutor struct {
	started   []Backup
	probed    []Backup
	probeRes  ProbeResult
	probeErr  error
	startErr  error
	startSeen map[string]bool
}

func newFakeExecutor(res ProbeResult) *fakeExecutor {
	return &fakeExecutor{probeRes: res, startSeen: map[string]bool{}}
}

func (f *fakeExecutor) Start(_ context.Context, b Backup, _ Target) error {
	f.started = append(f.started, b)
	f.startSeen[b.ID] = true
	return f.startErr
}

func (f *fakeExecutor) Probe(_ context.Context, b Backup, _ Target) (ProbeResult, error) {
	f.probed = append(f.probed, b)
	return f.probeRes, f.probeErr
}

// fakeTargets 校验 ctx 租户已注入（Pacer sweep 根 ctx 无租户的坑用测试锁住，
// 集群 PG repo 会 missing tenant 全拒——内存 fake 不校验则永不触发）。
type fakeTargets struct{ lastTenant string }

func (f *fakeTargets) ResolveTarget(ctx context.Context, _, _ string) (Target, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid == "" {
		return Target{}, fmt.Errorf("missing tenant context")
	}
	f.lastTenant = tid
	return Target{Namespace: "paas-" + tid, ServiceName: "ds-1", SecretName: "ds-1-secret", Engine: "postgres"}, nil
}

func TestPacerPendingStartsJobAndAdvancesRunning(t *testing.T) {
	repo := newFakeRepo()
	repo.items["bk-a"] = Backup{ID: "bk-a", TenantID: "t-acme", ResourceID: "ds-1", Mode: ModeSnapshot, Status: StatusAccepted, CreatedAt: time.Now()}
	ex := newFakeExecutor(ProbeResult{Phase: "pending"})
	p := NewPacer(repo, ex, &fakeTargets{})
	p.Sweep(context.Background())
	if len(ex.started) != 1 || ex.started[0].ID != "bk-a" {
		t.Fatalf("pending 应触发 Start，got %v", ex.started)
	}
	if repo.items["bk-a"].Status != StatusRunning {
		t.Fatalf("accepted 应推进 running，got %q", repo.items["bk-a"].Status)
	}
}

func TestPacerSucceededCompletesWithArtifact(t *testing.T) {
	repo := newFakeRepo()
	repo.items["bk-b"] = Backup{ID: "bk-b", TenantID: "t-acme", ResourceID: "ds-1", Mode: ModeSnapshot, Status: StatusRunning, CreatedAt: time.Now()}
	ex := newFakeExecutor(ProbeResult{Phase: "succeeded", SizeBytes: 4096, Checksum: strings.Repeat("a", 64)})
	p := NewPacer(repo, ex, &fakeTargets{})
	p.Sweep(context.Background())
	b := repo.items["bk-b"]
	if b.Status != StatusCompleted || b.SizeBytes != 4096 || b.Checksum != strings.Repeat("a", 64) {
		t.Fatalf("succeeded 应 completed+产物元数据，got %+v", b)
	}
	if b.ArtifactRef == "" {
		t.Fatal("快照 completed 必须写 ArtifactRef（restore 校验依赖）")
	}
	if b.FinishedAt.IsZero() || b.VerifiedAt.IsZero() {
		t.Fatal("completed 必须写终态时间与校验时间")
	}
}

func TestPacerFailedRecordsError(t *testing.T) {
	repo := newFakeRepo()
	repo.items["bk-c"] = Backup{ID: "bk-c", TenantID: "t-acme", ResourceID: "ds-1", Mode: ModeSnapshot, Status: StatusRunning, CreatedAt: time.Now()}
	ex := newFakeExecutor(ProbeResult{Phase: "failed", ErrMsg: "backoffLimit exceeded"})
	p := NewPacer(repo, ex, &fakeTargets{})
	p.Sweep(context.Background())
	b := repo.items["bk-c"]
	if b.Status != StatusFailed || b.Error != "backoffLimit exceeded" {
		t.Fatalf("failed 应记原因，got %+v", b)
	}
}

func TestPacerTimeoutUnknown(t *testing.T) {
	repo := newFakeRepo()
	repo.items["bk-d"] = Backup{ID: "bk-d", TenantID: "t-acme", ResourceID: "ds-1", Mode: ModeSnapshot,
		Status: StatusRunning, CreatedAt: time.Now().Add(-20 * time.Minute)}
	ex := newFakeExecutor(ProbeResult{Phase: "running"})
	p := NewPacer(repo, ex, &fakeTargets{})
	p.Sweep(context.Background())
	b := repo.items["bk-d"]
	if b.Status != StatusFailed || b.Error == "" {
		t.Fatalf("超时应 failed+说明（结果未知），got %+v", b)
	}
}

func TestPacerRestoreTargetsRestoredResource(t *testing.T) {
	repo := newFakeRepo()
	repo.items["bk-r"] = Backup{ID: "bk-r", TenantID: "t-acme", ResourceID: "ds-src", Mode: ModeRestore,
		RestoredResourceID: "ds-new", Status: StatusAccepted, CreatedAt: time.Now()}
	ex := newFakeExecutor(ProbeResult{Phase: "pending"})
	p := NewPacer(repo, ex, &fakeTargets{})
	p.Sweep(context.Background())
	if len(ex.started) != 1 {
		t.Fatal("restore pending 应触发 Start")
	}
	if repo.items["bk-r"].Status != StatusRunning {
		t.Fatalf("restore accepted 应推进 running，got %q", repo.items["bk-r"].Status)
	}
}

// TestPacerRecordModeIgnored 记录模式无作业（completed 终态不入 ListActive）。
func TestPacerRecordModeIgnored(t *testing.T) {
	actives, _ := newFakeRepo().ListActive(context.Background())
	for _, b := range actives {
		if b.EffectiveMode() == ModeRecord {
			t.Fatalf("记录模式不应出现在活动集: %+v", b)
		}
	}
}
