package workload

import (
	"context"
	"testing"
	"time"

	"github.com/aitoys/paas/pkg/tenant"
)

type gcFakeRepo struct {
	stubRepo
	items    []Workload
	deleted  []string
	delCtxOK []bool // Delete 时 ctx 是否含租户（防「无租户 ctx GC 静默失效」回归）
}

func (f *gcFakeRepo) ListAll(ctx context.Context) ([]Workload, error) { return f.items, nil }
func (f *gcFakeRepo) Delete(ctx context.Context, id string) error {
	_, ok := tenant.TenantFrom(ctx)
	f.delCtxOK = append(f.delCtxOK, ok)
	f.deleted = append(f.deleted, id)
	return nil
}

type gcFakeRuns struct {
	last   map[string]time.Time
	active map[string]bool
}

func (f gcFakeRuns) LastActive(ctx context.Context, tenantID, appID, lane string) (LaneActivity, error) {
	return LaneActivity{Active: f.active[appID+"/"+lane], Last: f.last[appID+"/"+lane]}, nil
}

type errRuns struct{}

func (errRuns) LastActive(context.Context, string, string, string) (LaneActivity, error) {
	return LaneActivity{}, context.DeadlineExceeded
}

func newGC(items []Workload, last map[string]time.Time) (*LaneGC, *gcFakeRepo) {
	repo := &gcFakeRepo{items: items}
	g := &LaneGC{
		Repos: repo, Runs: gcFakeRuns{last: last},
		EnvType: func(context.Context, string) (string, error) { return "test", nil },
		TTL:     72 * time.Hour, MaxSweep: 20,
		Now: func() time.Time { return time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC) },
	}
	return g, repo
}

func wl(id, lane string, createdAt time.Time) Workload {
	return Workload{ID: id, TenantID: "t1", AppID: "a1", EnvID: "e1", LaneID: lane, CreatedAt: createdAt}
}

var gcOld = time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)    // 5 天前 > 72h
var gcRecent = time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC) // 1 天前 < 72h

func TestSweepDeletesIdleLane(t *testing.T) {
	g, repo := newGC([]Workload{wl("w1", "feature-x", gcOld)}, nil)
	if n := g.Sweep(context.Background()); n != 1 || len(repo.deleted) != 1 {
		t.Fatalf("应删 1 个, got n=%d deleted=%v", n, repo.deleted)
	}
	if !repo.delCtxOK[0] {
		t.Fatal("Delete ctx 应含租户（无租户 ctx 真实 store 会拒删，GC 静默失效）")
	}
}

func TestSweepKeepsDefaultAndFresh(t *testing.T) {
	g, repo := newGC([]Workload{wl("w1", "default", gcOld), wl("w2", "feature-x", gcRecent)}, nil)
	if n := g.Sweep(context.Background()); n != 0 || len(repo.deleted) != 0 {
		t.Fatalf("default/新近不应删, got %v", repo.deleted)
	}
}

func TestSweepRecentRunBlocks(t *testing.T) {
	// Workload 很旧但最近有终态 run -> 不回收（TTL 从最近活跃起算）
	g, repo := newGC([]Workload{wl("w1", "feature-x", gcOld)},
		map[string]time.Time{"a1/feature-x": gcRecent})
	if n := g.Sweep(context.Background()); n != 0 || len(repo.deleted) != 0 {
		t.Fatalf("最近终态 run 应刷新闲置计时, got %v", repo.deleted)
	}
}

func TestSweepActiveRunBlocks(t *testing.T) {
	// Workload 很旧、无终态 run 但有进行中 run（running/paused）-> 禁止回收（部署/联调中）
	g, _ := newGC([]Workload{wl("w1", "feature-x", gcOld)}, nil)
	g.Runs = gcFakeRuns{active: map[string]bool{"a1/feature-x": true}}
	if n := g.Sweep(context.Background()); n != 0 {
		t.Fatalf("进行中 run 应禁止回收, got %d", n)
	}
}

func TestSweepSkipsProd(t *testing.T) {
	g, _ := newGC([]Workload{wl("w1", "feature-x", gcOld)}, nil)
	g.EnvType = func(context.Context, string) (string, error) { return "prod", nil }
	if n := g.Sweep(context.Background()); n != 0 {
		t.Fatalf("prod 泳道不回收, got %d", n)
	}
}

func TestSweepEnvTypeErrorSkips(t *testing.T) {
	// prod 护栏 fail-closed：EnvType 查询失败也跳过（环境 store 抖动不误删生产泳道）
	g, _ := newGC([]Workload{wl("w1", "feature-x", gcOld)}, nil)
	g.EnvType = func(context.Context, string) (string, error) { return "", context.DeadlineExceeded }
	if n := g.Sweep(context.Background()); n != 0 {
		t.Fatalf("EnvType 查询失败应跳过（fail-closed）, got %d", n)
	}
}

func TestSweepMaxCap(t *testing.T) {
	items := make([]Workload, 25)
	for i := range items {
		items[i] = wl(string(rune('a'+i)), "feature-x", gcOld)
	}
	g, repo := newGC(items, nil)
	if n := g.Sweep(context.Background()); n != 20 || len(repo.deleted) != 20 {
		t.Fatalf("单轮上限 20, got %d", n)
	}
}

func TestSweepRunCheckErrorSkips(t *testing.T) {
	g, _ := newGC([]Workload{wl("w1", "feature-x", gcOld)}, nil)
	g.Runs = errRuns{}
	if n := g.Sweep(context.Background()); n != 0 {
		t.Fatalf("查询失败应跳过（fail-open 下轮再试）, got %d", n)
	}
}

func TestSweepQuotaDec(t *testing.T) {
	g, repo := newGC([]Workload{wl("w1", "feature-x", gcOld)}, nil)
	var quotaTenant string
	g.Quota = func(ctx context.Context, tenantID string) { quotaTenant = tenantID }
	if n := g.Sweep(context.Background()); n != 1 {
		t.Fatalf("应删 1 个, got %d", n)
	}
	if quotaTenant != "t1" || len(repo.deleted) != 1 {
		t.Fatalf("配额应按租户回退, got tenant=%q", quotaTenant)
	}
}

// fakeLaneStatus 是 LaneStatusStore 的测试替身（permanent 联动 + MarkClosed 幂等验证）。
type fakeLaneStatus struct {
	modes      map[string]string // key: envID+"/"+name -> mode
	closedKeys []string
}

func (f *fakeLaneStatus) Mode(_ context.Context, envID, name string) (string, error) {
	m, ok := f.modes[envID+"/"+name]
	if !ok {
		return "", errNotFound // 无实体（纯遗留泳道）
	}
	return m, nil
}
func (f *fakeLaneStatus) MarkClosed(_ context.Context, envID, name string) error {
	if _, ok := f.modes[envID+"/"+name]; !ok {
		return nil // 无实体忽略
	}
	f.closedKeys = append(f.closedKeys, envID+"/"+name)
	return nil
}

func TestSweepPermanentLaneSkipped(t *testing.T) {
	g, repo := newGC([]Workload{wl("w1", "feature-x", gcOld)}, nil)
	g.Lanes = &fakeLaneStatus{modes: map[string]string{"e1/feature-x": "permanent"}}
	if n := g.Sweep(context.Background()); n != 0 || len(repo.deleted) != 0 {
		t.Fatalf("permanent 泳道 GC 永不回收, got %v", repo.deleted)
	}
}

func TestSweepLaneStatusErrorSkips(t *testing.T) {
	// 实体查询失败保守跳过（fail-closed，下轮再试），不误删
	g, _ := newGC([]Workload{wl("w1", "feature-x", gcOld)}, nil)
	g.Lanes = errLaneStatus{}
	if n := g.Sweep(context.Background()); n != 0 {
		t.Fatalf("Lane 实体查询失败应跳过, got %d", n)
	}
}

type errLaneStatus struct{}

func (errLaneStatus) Mode(context.Context, string, string) (string, error) {
	return "", context.DeadlineExceeded
}
func (errLaneStatus) MarkClosed(context.Context, string, string) error { return nil }

func TestSweepMarksLaneClosed(t *testing.T) {
	g, _ := newGC([]Workload{wl("w1", "feature-x", gcOld)}, nil)
	ls := &fakeLaneStatus{modes: map[string]string{"e1/feature-x": "standard"}}
	g.Lanes = ls
	if n := g.Sweep(context.Background()); n != 1 {
		t.Fatalf("应删 1 个, got %d", n)
	}
	if len(ls.closedKeys) != 1 || ls.closedKeys[0] != "e1/feature-x" {
		t.Fatalf("回收后应 MarkClosed, got %v", ls.closedKeys)
	}
}

func TestReclaimLaneDeletesAndMarksClosed(t *testing.T) {
	repo := &gcFakeRepo{items: []Workload{
		wl("w1", "feature-x", gcOld), wl("w2", "feature-x", gcOld), wl("w3", "default", gcOld),
	}}
	// ReclaimLane 走 List（stubRepo.List 忽略过滤参数返全量），fake 需按 lane 过滤——重写 List。
	repo.list = repo.items
	g := &LaneGC{
		Repos:   &laneFilterRepo{gcFakeRepo: repo, lane: "feature-x"},
		Runs:    gcFakeRuns{},
		EnvType: func(context.Context, string) (string, error) { return "test", nil },
	}
	ls := &fakeLaneStatus{modes: map[string]string{"e1/feature-x": "standard"}}
	g.Lanes = ls
	n, err := g.ReclaimLane(context.Background(), "t1", "e1", "feature-x")
	if err != nil || n != 2 {
		t.Fatalf("应回收 2 个, got n=%d err=%v", n, err)
	}
	if len(ls.closedKeys) != 1 {
		t.Fatalf("回收后应 MarkClosed, got %v", ls.closedKeys)
	}
}

// laneFilterRepo 装饰 gcFakeRepo：List 按 LaneID 过滤（stubRepo.List 忽略过滤参数）。
type laneFilterRepo struct {
	*gcFakeRepo
	lane string
}

func (f *laneFilterRepo) List(_ context.Context, _, _, laneID, _, _ string) ([]Workload, error) {
	out := make([]Workload, 0)
	for _, w := range f.items {
		if w.LaneID == laneID {
			out = append(out, w)
		}
	}
	return out, nil
}

type fakeOverrideCleaner struct {
	cleaned []string // "tenantID/envID/laneID"
	err     error
}

func (f *fakeOverrideCleaner) CleanLane(ctx context.Context, tenantID, envID, laneID string) error {
	if f.err != nil {
		return f.err
	}
	f.cleaned = append(f.cleaned, tenantID+"/"+envID+"/"+laneID)
	return nil
}

// 回收路径级联清配置中心泳道覆盖：Sweep（同泳道全删完才清，与 MarkClosed 同款时机）
func TestSweepCleansLaneOverrides(t *testing.T) {
	g, _ := newGC([]Workload{
		wl("w1", "feature-x", gcOld), wl("w2", "feature-x", gcOld), wl("w3", "default", gcOld),
	}, nil)
	oc := &fakeOverrideCleaner{}
	g.Cleaners = []LaneOverrideCleaner{oc}
	g.Sweep(context.Background())
	if len(oc.cleaned) != 1 || oc.cleaned[0] != "t1/e1/feature-x" {
		t.Fatalf("同泳道全部回收后应清覆盖, got %v", oc.cleaned)
	}
}

// 部分删除（MaxSweep 截断）不清：同泳道仍有剩余 workload，覆盖保留
func TestSweepPartialDeleteSkipsClean(t *testing.T) {
	g, _ := newGC([]Workload{
		wl("w1", "feature-x", gcOld), wl("w2", "feature-x", gcOld),
	}, nil)
	g.MaxSweep = 1 // 只删 1 个，另一条下轮
	oc := &fakeOverrideCleaner{}
	g.Cleaners = []LaneOverrideCleaner{oc}
	if n := g.Sweep(context.Background()); n != 1 {
		t.Fatalf("应删 1 个, got %d", n)
	}
	if len(oc.cleaned) != 0 {
		t.Fatalf("部分删除不应清覆盖, got %v", oc.cleaned)
	}
}

// 清理失败 best-effort：不 panic 不阻断（MarkClosed 照常）
func TestSweepCleanErrorBestEffort(t *testing.T) {
	g, _ := newGC([]Workload{wl("w1", "feature-x", gcOld)}, nil)
	g.Cleaners = []LaneOverrideCleaner{&fakeOverrideCleaner{err: context.DeadlineExceeded}}
	if n := g.Sweep(context.Background()); n != 1 {
		t.Fatalf("应删 1 个, got %d", n)
	}
}

// ReclaimLane（lane handler 关闭泳道）同样级联清覆盖
func TestReclaimLaneCleansOverrides(t *testing.T) {
	repo := &gcFakeRepo{items: []Workload{wl("w1", "feature-x", gcOld), wl("w2", "default", gcOld)}}
	repo.list = repo.items
	g := &LaneGC{
		Repos:   &laneFilterRepo{gcFakeRepo: repo, lane: "feature-x"},
		Runs:    gcFakeRuns{},
		EnvType: func(context.Context, string) (string, error) { return "test", nil },
	}
	oc := &fakeOverrideCleaner{}
	g.Cleaners = []LaneOverrideCleaner{oc}
	if _, err := g.ReclaimLane(context.Background(), "t1", "e1", "feature-x"); err != nil {
		t.Fatalf("ReclaimLane: %v", err)
	}
	if len(oc.cleaned) != 1 || oc.cleaned[0] != "t1/e1/feature-x" {
		t.Fatalf("ReclaimLane 应清覆盖, got %v", oc.cleaned)
	}
}

// 空泳道（permanent 关闭/已无 workload）关闭：deleted=0 也必须清覆盖——
// e2e 实测回归（train-stable permanent 泳道关闭后发现端点仍返回覆盖值）。
func TestReclaimLaneEmptyLaneStillCleansOverrides(t *testing.T) {
	repo := &gcFakeRepo{items: nil}
	g := &LaneGC{
		Repos:   &laneFilterRepo{gcFakeRepo: repo, lane: "train-stable"},
		Runs:    gcFakeRuns{},
		EnvType: func(context.Context, string) (string, error) { return "test", nil },
	}
	oc := &fakeOverrideCleaner{}
	g.Cleaners = []LaneOverrideCleaner{oc}
	if _, err := g.ReclaimLane(context.Background(), "t1", "e1", "train-stable"); err != nil {
		t.Fatalf("ReclaimLane: %v", err)
	}
	if len(oc.cleaned) != 1 || oc.cleaned[0] != "t1/e1/train-stable" {
		t.Fatalf("空泳道关闭也应清覆盖, got %v", oc.cleaned)
	}
}
