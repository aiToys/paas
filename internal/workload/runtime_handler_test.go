package workload

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRuntimeActor 可注入错误的驱逐桩。
type fakeRuntimeActor struct {
	evictErr error
	evicted  []string
}

func (f *fakeRuntimeActor) Evict(ctx context.Context, workloadID, podName, podUID string) error {
	if f.evictErr != nil {
		return f.evictErr
	}
	f.evicted = append(f.evicted, podName+":"+podUID)
	return nil
}

// newRuntimeHandler 构造带全套运行态依赖的 handler（权限默认全放行）。
func newRuntimeHandler(repo Repository, ops OperationsRepo, reader RuntimeReader, actor RuntimeActor, terminals *TerminalManager) *Handler {
	h := newHandler(repo)
	h.runtimeReader = reader
	h.runtimeActor = actor
	h.ops = ops
	h.terminals = terminals
	h.ActorFn = func(r *http.Request) string { return "u-test" }
	h.UserFn = func(r *http.Request) string { return "u-test" }
	return h
}

func svcWL() []Workload {
	return []Workload{{ID: "wl-run", AppID: "app-1", EnvID: "env-test", Type: TypeService, Name: "svc-a", Replicas: 2}}
}

func TestRuntimeDetailUnavailableWithoutReader(t *testing.T) {
	h := newRuntimeHandler(&stubRepo{list: svcWL()}, &fakeOpsMem{}, nil, nil, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodGet, "/api/workloads/wl-run/runtime", ""))
	require.Equal(t, http.StatusOK, rec.Code)
	var out struct {
		Data Runtime `json:"data"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&out))
	assert.Equal(t, ObsSourceNone, out.Data.Observation.Source)
	assert.Equal(t, ObsStateUnavailable, out.Data.Observation.State)
	assert.False(t, out.Data.Capabilities.Restart.Allowed)
	assert.Empty(t, out.Data.Instances)
}

func TestRestartValidation(t *testing.T) {
	cases := []struct {
		name string
		wl   Workload
		body string
		want int
	}{
		{"reason 必填", Workload{ID: "wl-run", Type: TypeService, Replicas: 2, EnvID: "env-test"}, `{"reason":"  "}`, http.StatusBadRequest},
		{"job 拒绝", Workload{ID: "wl-run", Type: TypeJob, Replicas: 1, EnvID: "env-test"}, `{"reason":"why"}`, http.StatusBadRequest},
		{"副本 0 拒绝", Workload{ID: "wl-run", Type: TypeService, Replicas: 0, EnvID: "env-test"}, `{"reason":"why"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRuntimeHandler(&stubRepo{list: []Workload{tc.wl}}, &fakeOpsMem{}, &fakeRuntimeReader{}, &fakeRuntimeActor{}, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/workloads/wl-run/restart", tc.body))
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

func TestRestartAcceptedAndConflict(t *testing.T) {
	ops := &fakeOpsMem{}
	h := newRuntimeHandler(&stubRepo{list: svcWL()}, ops, &fakeRuntimeReader{}, &fakeRuntimeActor{}, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/workloads/wl-run/restart", `{"reason":"配置生效"}`))
	require.Equal(t, http.StatusAccepted, rec.Code)
	var out struct {
		Data Operation `json:"data"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&out))
	assert.Equal(t, OpKindRestart, out.Data.Kind)
	assert.Equal(t, OpStatusAccepted, out.Data.Status)
	// 已有活跃操作 → 409。
	h2 := newRuntimeHandler(&stubRepo{list: svcWL()}, &fakeOpsMem{ops: []Operation{{ID: "op-live", WorkloadID: "wl-run", Status: OpStatusRunning}}}, &fakeRuntimeReader{}, &fakeRuntimeActor{}, nil)
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, acmeReq(http.MethodPost, "/api/workloads/wl-run/restart", `{"reason":"again"}`))
	assert.Equal(t, http.StatusConflict, rec2.Code)
}

func TestRestartDeniedWithoutWritePerm(t *testing.T) {
	h := newRuntimeHandler(&stubRepo{list: svcWL()}, &fakeOpsMem{}, &fakeRuntimeReader{}, &fakeRuntimeActor{}, nil)
	h.Authorize = func(r *http.Request, perm string) bool { return perm == PermWorkloadRead } // 只读角色
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/workloads/wl-run/restart", `{"reason":"x"}`))
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestRecreateEvictErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"UID 不匹配 409", ErrUIDMismatch, http.StatusConflict},
		{"PDB 阻断 429", ErrTooManyEvict, http.StatusTooManyRequests},
		{"非托管 400", ErrNotManaged, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newRuntimeHandler(&stubRepo{list: svcWL()}, &fakeOpsMem{}, &fakeRuntimeReader{}, &fakeRuntimeActor{evictErr: tc.err}, nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/workloads/wl-run/instances/pod-a/recreate", `{"podUid":"uid-1","reason":"fix"}`))
			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

func TestRecreateRequiresPodUID(t *testing.T) {
	h := newRuntimeHandler(&stubRepo{list: svcWL()}, &fakeOpsMem{}, &fakeRuntimeReader{}, &fakeRuntimeActor{}, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/workloads/wl-run/instances/pod-a/recreate", `{"reason":"fix"}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTerminalCreatePermissionAndValidation(t *testing.T) {
	terminals := NewTerminalManager(&fakeStreamer{})
	// exec 权限拒绝。
	h := newRuntimeHandler(&stubRepo{list: svcWL()}, &fakeOpsMem{}, &fakeRuntimeReader{}, &fakeRuntimeActor{}, terminals)
	h.Authorize = func(r *http.Request, perm string) bool { return perm != PermWorkloadExec }
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/workloads/wl-run/instances/pod-a/terminal-sessions", `{"podUid":"u","reason":"r"}`))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	// 未启用（Streamer nil）→ 503。
	h2 := newRuntimeHandler(&stubRepo{list: svcWL()}, &fakeOpsMem{}, &fakeRuntimeReader{}, &fakeRuntimeActor{}, NewTerminalManager(nil))
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, acmeReq(http.MethodPost, "/api/workloads/wl-run/instances/pod-a/terminal-sessions", `{"podUid":"u","reason":"r"}`))
	assert.Equal(t, http.StatusServiceUnavailable, rec2.Code)
	// podUid 缺失 → 400；正常受理 → 201 + streamPath（全新 handler：Authorize 默认全放行）。
	h4 := newRuntimeHandler(&stubRepo{list: svcWL()}, &fakeOpsMem{}, &fakeRuntimeReader{}, &fakeRuntimeActor{}, terminals)
	rec3 := httptest.NewRecorder()
	h4.ServeHTTP(rec3, acmeReq(http.MethodPost, "/api/workloads/wl-run/instances/pod-a/terminal-sessions", `{"reason":"r"}`))
	assert.Equal(t, http.StatusBadRequest, rec3.Code)
	rec4 := httptest.NewRecorder()
	h4.ServeHTTP(rec4, acmeReq(http.MethodPost, "/api/workloads/wl-run/instances/pod-a/terminal-sessions", `{"podUid":"u","shell":"/bin/sh","reason":"debug"}`))
	require.Equal(t, http.StatusCreated, rec4.Code)
	var out struct {
		Data struct {
			SessionID  string `json:"sessionId"`
			StreamPath string `json:"streamPath"`
		} `json:"data"`
	}
	require.NoError(t, json.NewDecoder(rec4.Body).Decode(&out))
	assert.NotEmpty(t, out.Data.SessionID)
	assert.True(t, strings.HasPrefix(out.Data.StreamPath, "/api/terminal-sessions/"))
}

func TestOperationsListRequiresOps(t *testing.T) {
	h := newRuntimeHandler(&stubRepo{list: svcWL()}, nil, &fakeRuntimeReader{}, &fakeRuntimeActor{}, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodGet, "/api/workloads/wl-run/operations", ""))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestEventsRequiresReader(t *testing.T) {
	h := newRuntimeHandler(&stubRepo{list: svcWL()}, &fakeOpsMem{}, nil, nil, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodGet, "/api/workloads/wl-run/instances/pod-a/events", ""))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestCrossTenantNotFound(t *testing.T) {
	// stubRepo 只按 ID 匹配（无租户过滤）；跨租户语义由 Repository 层保证，
	// handler 测试锁定：目标不存在时统一 404 不泄漏。
	h := newRuntimeHandler(&stubRepo{list: svcWL()}, &fakeOpsMem{}, &fakeRuntimeReader{}, &fakeRuntimeActor{}, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodGet, "/api/workloads/wl-other/runtime", ""))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
