package workload

import (
	"context"
	"testing"
)

// fakeRepo 是最小 Repository mock（不依赖 memory 包，避免循环 import）。
type fakeRepo struct {
	created   []Workload
	updated   []string
	deleted   []string
	createErr error
}

func (f *fakeRepo) List(ctx context.Context, envID, appID, laneID, wtype, service string) ([]Workload, error) {
	return nil, nil
}
func (f *fakeRepo) ListAll(ctx context.Context) ([]Workload, error) { return nil, nil }
func (f *fakeRepo) Get(ctx context.Context, id string) (Workload, error) { return Workload{}, nil }
func (f *fakeRepo) Create(ctx context.Context, w Workload) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, w)
	return nil
}
func (f *fakeRepo) Update(ctx context.Context, id string, replicas int, status string) (Workload, error) {
	f.updated = append(f.updated, id)
	return Workload{ID: id, Replicas: replicas, Status: status}, nil
}
func (f *fakeRepo) UpdateImage(ctx context.Context, id, image, imageRef string) (Workload, error) {
	return Workload{ID: id, Image: image, ImageRef: imageRef}, nil
}
func (f *fakeRepo) UpdateSchedule(ctx context.Context, id, schedule string) (Workload, error) {
	return Workload{ID: id, Schedule: schedule}, nil
}
func (f *fakeRepo) Delete(ctx context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

// fakeApplier 记录 Apply/EnsureIfMissing/Delete 调用。
type fakeApplier struct {
	applied []Workload
	deleted []string
}

func (a *fakeApplier) Apply(ctx context.Context, w Workload) error {
	a.applied = append(a.applied, w)
	return nil
}
// EnsureIfMissing 测试桩：视为 CRD 总不存在，走 Apply 补建并报 created=true。
func (a *fakeApplier) EnsureIfMissing(ctx context.Context, w Workload) (bool, error) {
	a.applied = append(a.applied, w)
	return true, nil
}
func (a *fakeApplier) Delete(ctx context.Context, id string) error {
	a.deleted = append(a.deleted, id)
	return nil
}

func TestApplyRepoCreateInvokesApplier(t *testing.T) {
	inner := &fakeRepo{}
	ap := &fakeApplier{}
	repo := NewApplyRepo(inner, ap)
	w := Workload{ID: "wl-1", Name: "n", Image: "img"}
	if err := repo.Create(context.Background(), w); err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	if len(inner.created) != 1 || inner.created[0].ID != "wl-1" {
		t.Fatalf("inner.Create 应被调用: %+v", inner.created)
	}
	if len(ap.applied) != 1 || ap.applied[0].ID != "wl-1" {
		t.Fatalf("applier.Apply 应被投影: %+v", ap.applied)
	}
}

func TestApplyRepoDeleteInvokesApplier(t *testing.T) {
	inner := &fakeRepo{}
	ap := &fakeApplier{}
	repo := NewApplyRepo(inner, ap)
	if err := repo.Delete(context.Background(), "wl-2"); err != nil {
		t.Fatalf("Delete 失败: %v", err)
	}
	if len(inner.deleted) != 1 || len(ap.deleted) != 1 || ap.deleted[0] != "wl-2" {
		t.Fatalf("Delete 应同时调 inner + applier: inner=%+v applier=%+v", inner.deleted, ap.deleted)
	}
}

func TestApplyRepoNilApplierTransparent(t *testing.T) {
	inner := &fakeRepo{}
	repo := NewApplyRepo(inner, nil) // applier=nil：透传，无投影
	if err := repo.Create(context.Background(), Workload{ID: "wl-3"}); err != nil {
		t.Fatalf("nil applier 下 Create 应正常: %v", err)
	}
	if len(inner.created) != 1 {
		t.Fatalf("inner.Create 应被调用")
	}
}

func TestApplyRepoCreateErrorSkipsApplier(t *testing.T) {
	inner := &fakeRepo{createErr: errSome}
	ap := &fakeApplier{}
	repo := NewApplyRepo(inner, ap)
	if err := repo.Create(context.Background(), Workload{ID: "wl-4"}); err != errSome {
		t.Fatalf("应返回 inner 错误")
	}
	if len(ap.applied) != 0 {
		t.Fatalf("inner 失败时不应投影 applier")
	}
}

var errSome = newSomeErr()

type someErr struct{}

func (someErr) Error() string { return "some error" }
func newSomeErr() error       { return someErr{} }
