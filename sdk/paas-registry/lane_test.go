package paasregistry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLaneCtxRoundTrip(t *testing.T) {
	ctx := context.Background()
	if got := LaneFromCtx(ctx); got != "" {
		t.Fatalf("空 ctx 应返空 lane，得 %q", got)
	}
	ctx = WithLane(ctx, "feature-x")
	if got := LaneFromCtx(ctx); got != "feature-x" {
		t.Fatalf("期望 feature-x，得 %q", got)
	}
}

func TestLaneMiddlewareExtractsHeader(t *testing.T) {
	got := ""
	mw := LaneMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = LaneFromCtx(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	// 带 header 提取 lane 入 ctx
	req := httptest.NewRequest(http.MethodGet, "/foo", nil)
	req.Header.Set(LaneHeader, "feature-x")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if got != "feature-x" {
		t.Fatalf("middleware 应从 header 提取 lane 入 ctx，得 %q", got)
	}

	// 缺 header 时下游 ctx 无 lane（默认基线）
	got = ""
	req2 := httptest.NewRequest(http.MethodGet, "/foo", nil)
	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, req2)
	if got != "" {
		t.Fatalf("无 header 时 ctx 不应含 lane，得 %q", got)
	}
}

func TestApplyLaneHeaderFromCtx(t *testing.T) {
	// ctx 含 lane → 注入 header
	req := httptest.NewRequest(http.MethodGet, "/foo", nil)
	ApplyLaneHeader(WithLane(context.Background(), "feature-x"), req)
	if got := req.Header.Get(LaneHeader); got != "feature-x" {
		t.Fatalf("期望注入 x-paas-lane=feature-x，得 %q", got)
	}

	// ctx 无 lane → 不注入
	req2 := httptest.NewRequest(http.MethodGet, "/foo", nil)
	ApplyLaneHeader(context.Background(), req2)
	if got := req2.Header.Get(LaneHeader); got != "" {
		t.Fatalf("无 lane 不应注入 header，得 %q", got)
	}

	// lane 空字符串 → 不注入（防御）
	req3 := httptest.NewRequest(http.MethodGet, "/foo", nil)
	ApplyLaneHeader(WithLane(context.Background(), ""), req3)
	if got := req3.Header.Get(LaneHeader); got != "" {
		t.Fatalf("空 lane 不应注入 header，得 %q", got)
	}
}

func TestSelfLaneFromEnv(t *testing.T) {
	t.Setenv("PAAS_LANE_ID", "feature-x")
	resetSelfLaneForTest(t)
	if got := SelfLane(); got != "feature-x" {
		t.Fatalf("SelfLane = %q, want feature-x", got)
	}
}

func TestSelfLaneDefaultEmpty(t *testing.T) {
	t.Setenv("PAAS_LANE_ID", "default")
	resetSelfLaneForTest(t)
	if got := SelfLane(); got != "" {
		t.Fatalf("default 应返空串, got %q", got)
	}
}

func TestResolveLanePriority(t *testing.T) {
	t.Setenv("PAAS_LANE_ID", "feature-env")
	resetSelfLaneForTest(t)
	// ctx 显式 > env
	ctx := WithLane(context.Background(), "feature-ctx")
	if got := resolveLane(ctx); got != "feature-ctx" {
		t.Fatalf("ctx 优先: got %q", got)
	}
	// ctx 无 lane -> env 回落
	if got := resolveLane(context.Background()); got != "feature-env" {
		t.Fatalf("env 回落: got %q", got)
	}
}

func TestApplyLaneHeaderEnvFallback(t *testing.T) {
	t.Setenv("PAAS_LANE_ID", "feature-env")
	resetSelfLaneForTest(t)
	req, _ := http.NewRequest("GET", "http://x", nil)
	ApplyLaneHeader(context.Background(), req)
	if got := req.Header.Get(LaneHeader); got != "feature-env" {
		t.Fatalf("env 回落注入: got %q", got)
	}
}

// resetSelfLaneForTest 重置 SelfLane 缓存（t.Setenv 后重新读 env）。
func resetSelfLaneForTest(t *testing.T) {
	selfLane.Store("")
	t.Cleanup(func() { selfLane.Store("") })
}

// TestExplicitDefaultOverridesSelfLane 显式 default 覆盖容器自身泳道（spec 2026-10-17 §5.1）：
// 容器部署在 feature-x（SelfLane）时，请求显式 x-paas-lane: default 必须解析回基线——
// 此前字面 "default" 归一后回落 SelfLane，显式基线请求被吞。
func TestExplicitDefaultOverridesSelfLane(t *testing.T) {
	t.Setenv("PAAS_LANE_ID", "feature-x")
	selfLane.Store("") // 重置进程级缓存（强制重读 env）
	t.Cleanup(func() { selfLane.Store("") }) // t.Setenv 只恢复 env 不清缓存，防污染后续测试
	ctx := WithLane(context.Background(), "default")
	if got := LaneFromCtx(ctx); got != "default" {
		t.Fatalf("LaneFromCtx 对外应读出 default，got %q", got)
	}
	if got := resolveLane(ctx); got != "" {
		t.Fatalf("显式 default 应解析为基线（覆盖 SelfLane），got %q", got)
	}
	// 未指定（ctx 无 lane）仍回落自身泳道（零改动染色语义不变）。
	if got := resolveLane(context.Background()); got != "feature-x" {
		t.Fatalf("未指定应回落 SelfLane=feature-x，got %q", got)
	}
	// 显式 feature 覆盖 SelfLane（既有语义回归锁）。
	if got := resolveLane(WithLane(context.Background(), "feature-y")); got != "feature-y" {
		t.Fatalf("显式 feature-y 应覆盖 SelfLane，got %q", got)
	}
}
