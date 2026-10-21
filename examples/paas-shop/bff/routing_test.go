package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/aitoys/paas-examples/paas-shop/internal/dpdisc"
)

func TestProxyDoesNotHideDiscoveryFailures(t *testing.T) {
	oldDP, oldFallback := dp, fallback
	t.Cleanup(func() { dp, fallback = oldDP, oldFallback })
	staticCalls := 0
	static := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { staticCalls++; _, _ = w.Write([]byte(`[]`)) }))
	defer static.Close()
	fallback = map[string]string{"product": static.URL, "order": static.URL, "chatbot": static.URL}
	for _, code := range []int{200, 403, 503} {
		platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"instances":[]}`))
		}))
		t.Setenv("PAAS_DP_ENDPOINT", platform.URL)
		dp = dpdisc.New()
		for _, path := range []string{"/api/products", "/api/products/1", "/api/orders", "/api/chat"} {
			r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
			r.Header.Set("X-Paas-Lane", "feature-x")
			w := httptest.NewRecorder()
			buildMux(newEventRing(1)).ServeHTTP(w, r)
			if w.Code != 503 {
				t.Fatalf("status=%d path=%s response=%s", w.Code, path, w.Body)
			}
		}
		platform.Close()
	}
	if staticCalls != 0 {
		t.Fatalf("silently hit static baseline %d times", staticCalls)
	}
}

func TestProxyReportsResolvedLaneAndKeepsRequestColor(t *testing.T) {
	oldDP := dp
	t.Cleanup(func() { dp = oldDP })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Paas-Lane") != "feature-x" {
			t.Error("lost request lane needed by downstream services")
		}
		w.Header().Set("X-Paas-Service", "baseline-pod @ v1")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	portNum, _ := strconv.Atoi(port)
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"instances":[{"ip":%q,"port":%d,"name":"baseline-pod","laneId":"default"}]}`, host, portNum)
	}))
	defer platform.Close()
	t.Setenv("PAAS_DP_ENDPOINT", platform.URL)
	dp = dpdisc.New()
	r := httptest.NewRequest(http.MethodGet, "/api/products", nil)
	r.Header.Set("X-Paas-Lane", "feature-x")
	w := httptest.NewRecorder()
	buildMux(newEventRing(1)).ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("X-Paas-Requested-Lane") != "feature-x" || w.Header().Get("X-Paas-Resolved-Lane") != "default" || w.Header().Get("X-Paas-Route-Reason") != "baseline-fallback" || w.Header().Get("X-Paas-Target-Instance") != "baseline-pod @ v1" {
		t.Fatalf("response=%d headers=%v", w.Code, w.Header())
	}
}

func TestStaticModeRejectsFeatureLane(t *testing.T) {
	old := dp
	dp = nil
	t.Cleanup(func() { dp = old })
	r := httptest.NewRequest(http.MethodGet, "/api/products", nil)
	r.Header.Set("X-Paas-Lane", "feature-x")
	w := httptest.NewRecorder()
	buildMux(newEventRing(1)).ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatalf("got %d", w.Code)
	}
}

func TestProductionRejectsFeatureLane(t *testing.T) {
	t.Setenv("PAAS_ENV_TYPE", "prod")
	r := httptest.NewRequest(http.MethodPost, "/api/orders", strings.NewReader(`{}`))
	r.Header.Set("X-Paas-Lane", "feature-x")
	w := httptest.NewRecorder()
	buildMux(newEventRing(1)).ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("production accepted lane: %d", w.Code)
	}
}
