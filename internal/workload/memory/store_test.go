package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/internal/workload"
	"github.com/aitoys/paas/pkg/tenant"
)

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }

func TestListByType(t *testing.T) {
	s := NewStore()
	// acme: cs-api(service), rec-svc(service)；globex: etl-nightly(cronjob), etl-backfill(job), agent-gw(service)
	svcs, err := s.List(acmeCtx(), "", workload.TypeService)
	require.NoError(t, err)
	require.Len(t, svcs, 2)
	for _, w := range svcs {
		assert.Equal(t, workload.TypeService, w.Type)
		assert.Equal(t, "t-acme", w.TenantID)
	}
}

func TestListIsolatedByTenant(t *testing.T) {
	s := NewStore()
	acme, _ := s.List(acmeCtx(), "", "")
	globex, _ := s.List(globexCtx(), "", "")
	for _, w := range acme {
		assert.Equal(t, "t-acme", w.TenantID)
	}
	for _, w := range globex {
		assert.Equal(t, "t-globex", w.TenantID)
	}
	// acme 不应见 globex 的 agent-gw
	for _, w := range acme {
		assert.NotEqual(t, "wl-agent-gw", w.ID)
	}
}

func TestListByApp(t *testing.T) {
	s := NewStore()
	// app-etl 属 globex，挂 cronjob + job
	got, err := s.List(globexCtx(), "app-etl", "")
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestCreateValidateAndTenant(t *testing.T) {
	s := NewStore()
	// 缺 image → Validate 失败
	err := s.Create(acmeCtx(), workload.Workload{ID: "x", Type: workload.TypeService, Name: "n"})
	assert.Error(t, err)

	// 合法创建：TenantID 取自 ctx
	err = s.Create(acmeCtx(), workload.Workload{
		ID: "wl-new", AppID: "app-cs", Type: workload.TypeService, Name: "new", Image: "img",
		Replicas: 1, Status: workload.StatusPending,
	})
	require.NoError(t, err)
	got, err := s.Get(acmeCtx(), "wl-new")
	require.NoError(t, err)
	assert.Equal(t, "t-acme", got.TenantID)
}

func TestGetRejectsCrossTenant(t *testing.T) {
	s := NewStore()
	// wl-cs-api 属 acme，globex 访问应 not found
	_, err := s.Get(globexCtx(), "wl-cs-api")
	assert.Error(t, err)
}

func TestUpdateReplicas(t *testing.T) {
	s := NewStore()
	// rec-svc 3/4 deploying → 扩到 6 running
	w, err := s.Update(acmeCtx(), "wl-rec-svc", 6, workload.StatusRunning)
	require.NoError(t, err)
	assert.Equal(t, 6, w.Replicas)
	assert.Equal(t, workload.StatusRunning, w.Status)
}

func TestUpdateRejectsCrossTenant(t *testing.T) {
	s := NewStore()
	_, err := s.Update(globexCtx(), "wl-cs-api", 1, workload.StatusRunning)
	assert.Error(t, err)
}

func TestDelete(t *testing.T) {
	s := NewStore()
	require.NoError(t, s.Delete(acmeCtx(), "wl-cs-api"))
	_, err := s.Get(acmeCtx(), "wl-cs-api")
	assert.Error(t, err)
	// 跨租户删除 → not found
	err = s.Delete(globexCtx(), "wl-rec-svc")
	assert.Error(t, err)
}

func TestMissingTenantRejected(t *testing.T) {
	s := NewStore()
	_, err := s.List(context.Background(), "", "")
	assert.Error(t, err)
}

func TestCronJobRequiresSchedule(t *testing.T) {
	s := NewStore()
	err := s.Create(acmeCtx(), workload.Workload{
		ID: "wl-c", AppID: "app-cs", Type: workload.TypeCronJob, Name: "c", Image: "img",
	})
	assert.Error(t, err, "cronjob 须有 schedule")
}
