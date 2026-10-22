package memory

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aitoys/paas/internal/ai/skill"
	"github.com/aitoys/paas/pkg/tenant"
)

func TestCreateDoesNotOverwriteIDAcrossTenants(t *testing.T) {
	s := NewStore()
	a, b := tenant.WithTenant(context.Background(), "a"), tenant.WithTenant(context.Background(), "b")
	original, err := s.Create(a, skill.Skill{ID: "shared", Name: "original", Instructions: "keep", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	for _, ctx := range []context.Context{a, b} {
		if _, err := s.Create(ctx, skill.Skill{ID: "shared", Name: "replacement", Instructions: "replace", Enabled: true}); !errors.Is(err, skill.ErrSkillExists) {
			t.Fatalf("碰撞应拒绝：%v", err)
		}
	}
	got, err := s.Get(a, original.ID)
	if err != nil || got.Instructions != "keep" || got.Enabled {
		t.Fatalf("原记录被覆盖或禁用未保留：%+v %v", got, err)
	}
	if _, err := s.Get(b, original.ID); !errors.Is(err, skill.ErrSkillNotFound) {
		t.Fatalf("跨租户读取：%v", err)
	}
	if _, err := s.Create(context.Background(), original); !errors.Is(err, skill.ErrSkillNotFound) {
		t.Fatalf("缺租户应拒绝：%v", err)
	}
}

func TestConcurrentIDCollisionHasOneWinner(t *testing.T) {
	s := NewStore()
	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Create(tenant.WithTenant(context.Background(), "t"), skill.Skill{ID: "one", Name: "one", Instructions: "x"})
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, skill.ErrSkillExists) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("并发写入成功数=%d", winners.Load())
	}
}
