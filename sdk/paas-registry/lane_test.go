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
