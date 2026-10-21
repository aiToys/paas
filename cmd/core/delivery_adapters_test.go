package main

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/aitoys/paas/internal/workload"
	wmemory "github.com/aitoys/paas/internal/workload/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

type unavailableDeliveryStatus struct{}

func (unavailableDeliveryStatus) FillStatus(context.Context, []workload.Workload) error {
	return errors.New("unavailable")
}
func (unavailableDeliveryStatus) Instances(context.Context, string) ([]workload.Instance, error) {
	return nil, nil
}
func (unavailableDeliveryStatus) PodLogs(context.Context, string, string, int64, bool) (io.ReadCloser, error) {
	return nil, errors.New("unavailable")
}
func TestDeliveryDoesNotSucceedFromStaleReadyWhenObservationFails(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	repo := wmemory.NewStore()
	err := repo.Create(ctx, workload.Workload{ID: "wl-delivery-observation", AppID: "app-cs", EnvID: "env-acme-test", Name: "frontend", Type: workload.TypeService, Image: "local:test", Replicas: 1, Ready: 1, Status: workload.StatusRunning})
	if err != nil {
		t.Fatal(err)
	}
	bridge := releaseBridge{workloads: repo, status: unavailableDeliveryStatus{}}
	if err := bridge.PollWorkloadReady(ctx, "wl-delivery-observation"); err == nil {
		t.Fatal("stale ready treated as observed success")
	}
}
