package memory

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aitoys/paas/internal/configcenter"
)

func TestLaneRevisionRejectsSameCountChange(t *testing.T) {
	s := NewStore()
	ctx := acmeCtx()
	row, err := s.UpsertLaneOverride(ctx, configcenter.LaneOverride{AppID: "a", EnvID: "e", LaneID: "l", Key: "key", Value: "A"})
	if err != nil {
		t.Fatal(err)
	}
	preview := configcenter.LaneRevision([]configcenter.LaneOverride{row})
	row.Value = "B"
	if _, err = s.UpsertLaneOverride(ctx, row); err != nil {
		t.Fatal(err)
	}
	_, err = s.ApplyLaneOverrides(ctx, "a", "e", "l", []configcenter.LaneOverrideOp{{Action: "upsert", Key: "key", Value: "C"}}, -1, preview)
	if !errors.Is(err, configcenter.ErrLaneOverrideConflict) {
		t.Fatalf("同数量改值必须冲突: %v", err)
	}
	rows, _ := s.ListLaneOverrides(ctx, "a", "e", "l")
	if len(rows) != 1 || rows[0].Value != "B" {
		t.Fatalf("冲突不得覆盖新值: %+v", rows)
	}
}

func TestLaneRevisionEmptyConcurrentWriters(t *testing.T) {
	s := NewStore()
	ctx := acmeCtx()
	preview := configcenter.LaneRevision(nil)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for _, value := range []string{"A", "B"} {
		wg.Add(1)
		go func(v string) {
			defer wg.Done()
			_, err := s.ApplyLaneOverrides(ctx, "a", "e", "l", []configcenter.LaneOverrideOp{{Action: "upsert", Key: "key", Value: v}}, -1, preview)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, configcenter.ErrLaneOverrideConflict) {
				t.Errorf("unexpected: %v", err)
			}
		}(value)
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("同一空集预览只能有一个写入成功: %d", successes.Load())
	}
}
