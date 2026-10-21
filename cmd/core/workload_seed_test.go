package main

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

// 阻止启动时Create的副作用，验证PG装配路径不会暗中向数据面投影。
type seedRecordingRepo struct {
	workload.Repository
	created  []workload.Workload
	badScope bool
}

func (r *seedRecordingRepo) Create(ctx context.Context, w workload.Workload) error {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok || tid != w.TenantID {
		r.badScope = true
	}
	r.created = append(r.created, w)
	return nil
}

func TestPersistentDemoWorkloadSeedRequiresOptIn(t *testing.T) {
	for _, tc := range []struct {
		name, enable, disable string
		want                  int
	}{
		{"default-no-create", "", "", 0},
		{"explicitly-disabled", "false", "", 0},
		{"explicit-opt-in", "true", "", 7},
		{"global-disable-wins", "true", "true", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PAAS_ENABLE_DEMO_WORKLOADS", tc.enable)
			t.Setenv("PAAS_DISABLE_DEMO_SEED", tc.disable)
			repo := &seedRecordingRepo{}
			seedWorkloads(context.Background(), repo)
			if repo.badScope || len(repo.created) != tc.want {
				t.Fatalf("Create count=%d want=%d badScope=%v", len(repo.created), tc.want, repo.badScope)
			}
		})
	}
}
