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
