package sidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterSendsAuthAndBody(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		buf := make([]byte, 200)
		n, _ := r.Body.Read(buf)
		gotBody = string(buf[:n])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"inst-1"}}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sk-test")
	c.HTTP = srv.Client()
	id, err := c.Register(context.Background(), Instance{
		ServiceID: "svc-order", Addr: "10.0.0.1:8080", LaneID: "default",
	})
	require.NoError(t, err)
	assert.Equal(t, "inst-1", id)
	assert.Equal(t, "/api/services/svc-order/instances", gotPath)
	assert.Equal(t, "Bearer sk-test", gotAuth)
	assert.Contains(t, gotBody, `"addr":"10.0.0.1:8080"`)
}

func TestHeartbeatAndDeregister(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "sk")
	c.HTTP = srv.Client()
	require.NoError(t, c.Heartbeat(context.Background(), "inst-1"))
	require.NoError(t, c.Deregister(context.Background(), "svc", "inst-1"))
	require.Len(t, paths, 2)
	assert.True(t, strings.HasPrefix(paths[0], "PUT /api/instances/inst-1/heartbeat"))
	assert.True(t, strings.HasPrefix(paths[1], "DELETE /api/services/svc/instances/inst-1"))
}

func TestRegisterServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "sk")
	c.HTTP = srv.Client()
	_, err := c.Register(context.Background(), Instance{ServiceID: "s", Addr: "a"})
	require.Error(t, err)
}

func TestKeepAliveStopsOnCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "sk")
	c.HTTP = srv.Client()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.KeepAlive(ctx, "inst-1", 10*time.Millisecond); close(done) }()
	time.Sleep(35 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("KeepAlive 未在 ctx 取消后退出")
	}
}
