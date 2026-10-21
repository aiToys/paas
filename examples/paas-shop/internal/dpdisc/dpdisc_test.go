package dpdisc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolvePlatformResult(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		wantLane   string
		wantErr    bool
	}{
		{"feature", `{"instances":[{"name":"order-feature","ip":"127.0.0.1","port":8084,"laneId":"feature-x"}]}`, 200, "feature-x", false},
		{"platform fallback", `{"instances":[{"name":"order-base","ip":"127.0.0.1","port":8084,"laneId":"default"}]}`, 200, "default", false},
		{"unhealthy", `{"instances":[]}`, 200, "", true},
		{"null instances", `{"instances":null}`, 200, "", true},
		{"missing instances", `{}`, 200, "", true},
		{"invalid instance", `{"instances":[{"ip":"127.0.0.1"}]}`, 200, "", true},
		{"forbidden", `{"error":"denied"}`, 403, "", true},
		{"server failure", `unavailable`, 503, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("lane") != "feat/a+b" || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Errorf("query/auth mismatch: %s", r.URL)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			t.Setenv("PAAS_DP_ENDPOINT", srv.URL)
			t.Setenv("PAAS_DP_TOKEN", "test-token")
			instance, err := New().Resolve(context.Background(), "order", "feat/a+b")
			if (err != nil) != tc.wantErr || instance.Lane != tc.wantLane {
				t.Fatalf("instance=%+v err=%v", instance, err)
			}
			if tc.name == "unhealthy" && !errors.Is(err, ErrNoReadyInstances) {
				t.Fatalf("expected no-ready sentinel, got %v", err)
			}
		})
	}
}

func TestDiscoveryFailureIsNotCached(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = w.Write([]byte(`{"instances":[{"ip":"127.0.0.1","port":80,"laneId":"default"}]}`))
	}))
	defer srv.Close()
	t.Setenv("PAAS_DP_ENDPOINT", srv.URL)
	d := New()
	if _, err := d.Resolve(context.Background(), "order", "x"); err == nil {
		t.Fatal("expected first error")
	}
	for range 2 {
		if _, err := d.Resolve(context.Background(), "order", "x"); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("expected successful result cached after retry, calls=%d", calls)
	}
}
