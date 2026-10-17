package workload

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// fakeOpsMem 是最小 OperationsRepo 内存桩（Pacer 测试用）。
type fakeOpsMem struct {
	ops     []Operation
	updates int
}

func (f *fakeOpsMem) seed(op Operation) { f.ops = append(f.ops, op) }

func (f *fakeOpsMem) CreateRestart(ctx context.Context, w Workload, reason, actor, idemKey string) (Operation, bool, error) {
	if op := f.findActive(w.ID); op != nil {
		return Operation{}, false, ErrOpConflict
	}
	op := Operation{ID: "op-auto", WorkloadID: w.ID, Kind: OpKindRestart, Status: OpStatusAccepted,
		Reason: reason, Actor: actor, TargetToken: "rt-auto", AcceptedAt: time.Now(), UpdatedAt: time.Now()}
	f.ops = append(f.ops, op)
	return op, true, nil
}
func (f *fakeOpsMem) CreateRecreate(ctx context.Context, w Workload, podName, podUID, reason, actor, idemKey string) (Operation, bool, error) {
	if op := f.findActive(w.ID); op != nil {
		return Operation{}, false, ErrOpConflict
	}
	op := Operation{ID: "op-auto", WorkloadID: w.ID, Kind: OpKindRecreate, Status: OpStatusAccepted,
		PodName: podName, PodUID: podUID, Reason: reason, Actor: actor, AcceptedAt: time.Now(), UpdatedAt: time.Now()}
	f.ops = append(f.ops, op)
	return op, true, nil
}

func (f *fakeOpsMem) findActive(workloadID string) *Operation {
	for i := range f.ops {
		if f.ops[i].WorkloadID == workloadID {
			switch f.ops[i].Status {
			case OpStatusAccepted, OpStatusReconciling, OpStatusRunning:
				return &f.ops[i]
			}
		}
	}
	return nil
}
func (f *fakeOpsMem) Get(ctx context.Context, workloadID, operationID string) (Operation, error) {
	return Operation{}, errors.New("not implemented")
}
func (f *fakeOpsMem) ListByWorkload(ctx context.Context, workloadID string, limit, offset int) ([]Operation, error) {
	return nil, nil
}
func (f *fakeOpsMem) Active(ctx context.Context, workloadID string) (Operation, error) {
	return Operation{}, nil
}
func (f *fakeOpsMem) ListActive(ctx context.Context) ([]Operation, error) { return f.ops, nil }
func (f *fakeOpsMem) UpdateStatus(ctx context.Context, op Operation) error {
	for i := range f.ops {
		if f.ops[i].ID == op.ID {
			f.ops[i] = op
			f.updates++
			return nil
		}
	}
	return nil
}

// fakeRuntimeReader 返回固定 phase 的观察桩。
type fakeRuntimeReader struct {
	restartPhase, recreatePhase, detail string
	restartErr                          error
}

func (f *fakeRuntimeReader) Runtime(ctx context.Context, workloadID string) (Runtime, error) {
	return Runtime{}, nil
}
func (f *fakeRuntimeReader) PodEvents(ctx context.Context, workloadID, podName, podUID string) ([]PodEvent, error) {
	return nil, nil
}
func (f *fakeRuntimeReader) ContainerLogs(ctx context.Context, workloadID, podName, podUID, container string, tail int64, previous bool) (io.ReadCloser, error) {
	return nil, nil
}
func (f *fakeRuntimeReader) RestartProgress(ctx context.Context, workloadID, token string) (string, string, error) {
	return f.restartPhase, f.detail, f.restartErr
}
func (f *fakeRuntimeReader) RecreateProgress(ctx context.Context, workloadID, oldUID string) (string, string, error) {
	return f.recreatePhase, f.detail, nil
}

func TestOperationPacerRestartLifecycle(t *testing.T) {
	ops := &fakeOpsMem{}
	ops.seed(Operation{ID: "op-1", TenantID: "acme", WorkloadID: "wl-x", Kind: OpKindRestart,
		Status: OpStatusAccepted, TargetToken: "rt-1", AcceptedAt: time.Now().Add(-time.Minute)})
	pacer := &OperationPacer{Ops: ops, Reader: &fakeRuntimeReader{restartPhase: "running", detail: "1/2 updated"}}
	pacer.sweep(context.Background())
	if ops.ops[0].Status != OpStatusRunning {
		t.Fatalf("accepted+running phase 应推进 running，got %s", ops.ops[0].Status)
	}
	// running → completed。
	ops.ops[0].Status = OpStatusRunning
	pacer.Reader = &fakeRuntimeReader{restartPhase: "completed", detail: "all ready"}
	pacer.sweep(context.Background())
	if ops.ops[0].Status != OpStatusSucceeded || ops.ops[0].FinishedAt == nil {
		t.Fatalf("completed phase 应推进 succeeded+FinishedAt，got %s", ops.ops[0].Status)
	}
	// superseded：被新发布覆盖。
	ops.ops[0].Status = OpStatusAccepted
	ops.ops[0].FinishedAt = nil
	pacer.Reader = &fakeRuntimeReader{restartPhase: "superseded", detail: "new revision"}
	pacer.sweep(context.Background())
	if ops.ops[0].Status != OpStatusSuperseded {
		t.Fatalf("superseded phase 应推进 superseded，got %s", ops.ops[0].Status)
	}
}

func TestOperationPacerEvidenceDedup(t *testing.T) {
	ops := &fakeOpsMem{}
	ops.seed(Operation{ID: "op-2", TenantID: "acme", WorkloadID: "wl-x", Kind: OpKindRestart,
		Status: OpStatusAccepted, TargetToken: "rt-2", AcceptedAt: time.Now().Add(-time.Minute)})
	reader := &fakeRuntimeReader{restartPhase: "running", detail: "1/2 updated"}
	pacer := &OperationPacer{Ops: ops, Reader: reader}
	pacer.sweep(context.Background())
	before := ops.updates
	pacer.sweep(context.Background()) // 同 phase 二轮：evidence 去重，但 UpdateStatus 仍会调
	if got := ops.ops[0].Evidence; got != "1/2 updated" {
		t.Fatalf("同 detail 不应重复追加，got %q", got)
	}
	if ops.updates != before+1 {
		t.Fatalf("第二轮仍应有一次 UpdateStatus 调用（幂等写），got updates %d→%d", before, ops.updates)
	}
}

func TestOperationPacerTimeoutUnknown(t *testing.T) {
	ops := &fakeOpsMem{}
	ops.seed(Operation{ID: "op-3", TenantID: "acme", WorkloadID: "wl-x", Kind: OpKindRestart,
		Status: OpStatusRunning, TargetToken: "rt-3", AcceptedAt: time.Now().Add(-11 * time.Minute)})
	pacer := &OperationPacer{Ops: ops, Reader: &fakeRuntimeReader{restartPhase: "running", detail: "slow"},
		Timeout: 10 * time.Minute}
	pacer.sweep(context.Background())
	if ops.ops[0].Status != OpStatusUnknown || ops.ops[0].FinishedAt == nil {
		t.Fatalf("观察超时应标 unknown+FinishedAt，got %s", ops.ops[0].Status)
	}
}

func TestOperationPacerRecreateLifecycle(t *testing.T) {
	ops := &fakeOpsMem{}
	ops.seed(Operation{ID: "op-4", TenantID: "acme", WorkloadID: "wl-x", Kind: OpKindRecreate,
		Status: OpStatusAccepted, PodUID: "uid-old", AcceptedAt: time.Now().Add(-time.Minute)})
	pacer := &OperationPacer{Ops: ops, Reader: &fakeRuntimeReader{recreatePhase: "progressing", detail: "old pod terminating"}}
	pacer.sweep(context.Background())
	if ops.ops[0].Status != OpStatusRunning {
		t.Fatalf("recreate progressing 应推进 running，got %s", ops.ops[0].Status)
	}
	pacer.Reader = &fakeRuntimeReader{recreatePhase: "completed", detail: "new pod ready"}
	pacer.sweep(context.Background())
	if ops.ops[0].Status != OpStatusSucceeded {
		t.Fatalf("recreate completed 应推进 succeeded，got %s", ops.ops[0].Status)
	}
}

func TestAppendEvidence(t *testing.T) {
	if got := appendEvidence("", "a"); got != "a" {
		t.Fatalf("空基础应直接返回，got %q", got)
	}
	if got := appendEvidence("a", "a"); got != "a" {
		t.Fatalf("重复文本不追加，got %q", got)
	}
	if got := appendEvidence("a", "b"); got != "a\nb" {
		t.Fatalf("新文本换行追加，got %q", got)
	}
	if got := appendEvidence("a", ""); got != "a" {
		t.Fatalf("空追加不变化，got %q", got)
	}
}

// TestOperationPacerSupersededGrace：受理初期投影尚未生效（Deployment 还是旧 token），
// 宽限期内「superseded」不终态化；超宽限后正常判被覆盖。
func TestOperationPacerSupersededGrace(t *testing.T) {
	// 宽限期内：刚受理 + reader 报 superseded（投影窗口内旧 RS 仍是最新）。
	ops := &fakeOpsMem{}
	ops.seed(Operation{ID: "op-g", TenantID: "acme", WorkloadID: "wl-x", Kind: OpKindRestart,
		Status: OpStatusAccepted, TargetToken: "rt-1", AcceptedAt: time.Now().Add(-2 * time.Second)})
	pacer := &OperationPacer{Ops: ops, Reader: &fakeRuntimeReader{restartPhase: "superseded", detail: "旧版本仍是最新"}}
	pacer.sweep(context.Background())
	if ops.ops[0].Status != OpStatusAccepted || ops.ops[0].FinishedAt != nil {
		t.Fatalf("宽限期内 superseded 不应终态化，got status=%s finished=%v", ops.ops[0].Status, ops.ops[0].FinishedAt)
	}
	// 超宽限：正常判被覆盖。
	ops.ops[0].AcceptedAt = time.Now().Add(-supersededGracePeriod - time.Second)
	pacer.sweep(context.Background())
	if ops.ops[0].Status != OpStatusSuperseded || ops.ops[0].FinishedAt == nil {
		t.Fatalf("超宽限 superseded 应终态化，got status=%s", ops.ops[0].Status)
	}
	// reconciling 之后不再宽限（投影已见过新期望，token 不匹配即真被覆盖）。
	ops2 := &fakeOpsMem{}
	ops2.seed(Operation{ID: "op-g2", TenantID: "acme", WorkloadID: "wl-x", Kind: OpKindRestart,
		Status: OpStatusReconciling, TargetToken: "rt-2", AcceptedAt: time.Now()})
	pacer2 := &OperationPacer{Ops: ops2, Reader: &fakeRuntimeReader{restartPhase: "superseded", detail: "被覆盖"}}
	pacer2.sweep(context.Background())
	if ops2.ops[0].Status != OpStatusSuperseded {
		t.Fatalf("reconciling 后 superseded 应立即终态化，got %s", ops2.ops[0].Status)
	}
}
