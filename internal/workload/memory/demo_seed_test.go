package memory

import (
	"context"
	"testing"
)

func TestDemoWorkloadSeedRequiresOptIn(t *testing.T) {
	for _, tc := range []struct {
		name, enable, disable string
		want                  int
	}{
		{"default-empty", "", "", 0},
		{"explicitly-disabled", "false", "", 0},
		{"invalid-opt-in", "yes", "", 0},
		{"explicit-opt-in", "true", "", 7},
		{"global-disable-wins", "true", "true", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PAAS_ENABLE_DEMO_WORKLOADS", tc.enable)
			t.Setenv("PAAS_DISABLE_DEMO_SEED", tc.disable)
			rows, err := NewStore().ListAll(context.Background())
			if err != nil || len(rows) != tc.want {
				t.Fatalf("seed count=%d want=%d err=%v", len(rows), tc.want, err)
			}
		})
	}
}

func TestExplicitFixtureDoesNotBypassGlobalDisable(t *testing.T) {
	t.Setenv("PAAS_DISABLE_DEMO_SEED", "true")
	rows, err := NewStore(WithDemoSeed(true)).ListAll(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatalf("global disable bypassed: count=%d err=%v", len(rows), err)
	}
}
