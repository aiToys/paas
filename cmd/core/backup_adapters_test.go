package main

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/internal/dataservice"
	dsmemory "github.com/aitoys/paas/internal/dataservice/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

type failingRestoreRepo struct{ dataservice.Repository }

func (f failingRestoreRepo) Create(context.Context, dataservice.DataService) (dataservice.DataService, error) {
	return dataservice.DataService{}, errors.New("storage failure")
}

func TestRestoreQuotaAndIdempotentTarget(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	repo := dsmemory.NewStore()
	source, err := repo.Create(ctx, dataservice.DataService{Name: "source", Kind: "db", EnvID: "env-test", Spec: map[string]string{"engine": "postgres"}})
	if err != nil {
		t.Fatal(err)
	}
	used, limit := 1, 1
	r := backupRestorer{repo: repo, quota: func(_ context.Context, delta int) error {
		if used+delta > limit {
			return errors.New("quota exceeded")
		}
		used += delta
		return nil
	}}
	if _, err = r.CreateRestored(ctx, "t-acme", source.ID, "ds-restore-a", "restored"); err == nil {
		t.Fatal("超配额不能恢复建实例")
	}
	if _, err = repo.Get(ctx, "ds-restore-a"); err == nil {
		t.Fatal("超配额不得残留目标")
	}
	limit = 2
	for i := 0; i < 2; i++ {
		id, err := r.CreateRestored(ctx, "t-acme", source.ID, "ds-restore-a", "restored")
		if err != nil || id != "ds-restore-a" {
			t.Fatalf("创建/重入失败: %s %v", id, err)
		}
	}
	if used != 2 {
		t.Fatalf("重入重复占配额: %d", used)
	}
}

func TestRestoreCreationFailureReleasesQuota(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	repo := dsmemory.NewStore()
	source, err := repo.Create(ctx, dataservice.DataService{Name: "source", Kind: "db", EnvID: "env-test", Spec: map[string]string{"engine": "postgres"}})
	if err != nil {
		t.Fatal(err)
	}
	used := 0
	r := backupRestorer{repo: failingRestoreRepo{repo}, quota: func(_ context.Context, delta int) error { used += delta; return nil }}
	if _, err = r.CreateRestored(ctx, "t-acme", source.ID, "ds-restore-a", "restored"); err == nil {
		t.Fatal("expected storage failure")
	}
	if used != 0 {
		t.Fatalf("创建失败配额未回退: %d", used)
	}
}
