package workload

import (
	"context"
	"testing"
	"time"
)

type gcFakeRepo struct {
	stubRepo
	items   []Workload
	deleted []string
}

func (f *gcFakeRepo) ListAll(ctx context.Context) ([]Workload, error) { return f.items, nil }
func (f *gcFakeRepo) Delete(ctx context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

type gcFakeRuns struct{ last map[string]time.Time }

func (f gcFakeRuns) LastActive(ctx context.Context, tenantID, appID, lane string) (time.Time, error) {
	return f.last[appID+"/"+lane], nil
}

type errRuns struct{}

func (errRuns) LastActive(context.Context, string, string, string) (time.Time, error) {
	return time.Time{}, context.DeadlineExceeded
}

func newGC(items []Workload, last map[string]time.Time) (*LaneGC, *gcFakeRepo) {
	repo := &gcFakeRepo{items: items}
	g := &LaneGC{
		Repos: repo, Runs: gcFakeRuns{last: last},
		EnvType:  func(context.Context, string) (string, error) { return "test", nil },
		TTL:      72 * time.Hour, MaxSweep: 20,
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
}

func TestSweepKeepsDefaultAndFresh(t *testing.T) {
	g, repo := newGC([]Workload{wl("w1", "default", gcOld), wl("w2", "feature-x", gcRecent)}, nil)
	if n := g.Sweep(context.Background()); n != 0 || len(repo.deleted) != 0 {
		t.Fatalf("default/新近不应删, got %v", repo.deleted)
	}
}

func TestSweepRecentRunBlocks(t *testing.T) {
	// Workload 很旧但最近有活跃 run（联调中）-> 不回收
	g, repo := newGC([]Workload{wl("w1", "feature-x", gcOld)},
		map[string]time.Time{"a1/feature-x": gcRecent})
	if n := g.Sweep(context.Background()); n != 0 || len(repo.deleted) != 0 {
		t.Fatalf("活跃 run 应阻止回收, got %v", repo.deleted)
	}
}

func TestSweepSkipsProd(t *testing.T) {
	g, _ := newGC([]Workload{wl("w1", "feature-x", gcOld)}, nil)
	g.EnvType = func(context.Context, string) (string, error) { return "prod", nil }
	if n := g.Sweep(context.Background()); n != 0 {
		t.Fatalf("prod 泳道不回收, got %d", n)
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
		t.Fatalf("查询失败应跳过（fail-open）, got %d", n)
	}
}
