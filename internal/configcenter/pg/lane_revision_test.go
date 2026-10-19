//go:build integration

package pg

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aitoys/paas/internal/configcenter"
	"github.com/aitoys/paas/pkg/tenant"
)

func TestLaneRevisionConflictAndConcurrentEmptyPG(t *testing.T) {
	s := NewStore(newTestDB(t))
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	row, err := s.UpsertLaneOverride(ctx, configcenter.LaneOverride{AppID: "app-r", EnvID: "env-r", LaneID: "lane-r", Key: "k", Value: "A"})
	if err != nil {
		t.Fatal(err)
	}
	preview := configcenter.LaneRevision([]configcenter.LaneOverride{row})
	row.Value = "B"
	if _, err = s.UpsertLaneOverride(ctx, row); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyLaneOverrides(ctx, "app-r", "env-r", "lane-r", []configcenter.LaneOverrideOp{{Action: "upsert", Key: "k", Value: "C"}}, -1, preview); !errors.Is(err, configcenter.ErrLaneOverrideConflict) {
		t.Fatalf("同数量漂移未阻断: %v", err)
	}
	if _, err = s.EnsureByAppEnv(ctx, "app-r", "env-r"); err != nil {
		t.Fatal(err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for _, value := range []string{"A", "B"} {
		wg.Add(1)
		go func(v string) {
			defer wg.Done()
			_, err := s.ApplyLaneOverrides(ctx, "app-r", "env-r", "empty-lane", []configcenter.LaneOverrideOp{{Action: "upsert", Key: "k", Value: v}}, -1, configcenter.LaneRevision(nil))
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, configcenter.ErrLaneOverrideConflict) {
				t.Errorf("unexpected: %v", err)
			}
		}(value)
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("空集合并发版本校验失效: %d", successes.Load())
	}
}
