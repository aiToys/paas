package tool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeRepo 仅实现权限路径用到的 Get。
type fakeRepo struct{}

func (fakeRepo) Get(context.Context, string) (Tool, error) { return Tool{}, nil }
func (fakeRepo) List(context.Context) ([]Tool, error)      { return nil, nil }
func (fakeRepo) ListAll(context.Context) ([]Tool, error)   { return nil, nil }
func (fakeRepo) Create(_ context.Context, t Tool) (Tool, error) {
	return t, nil
}
func (fakeRepo) Update(_ context.Context, t Tool) (Tool, error) {
	return t, nil
}
func (fakeRepo) Delete(context.Context, string) error    { return nil }
func (fakeRepo) ToolsCount(context.Context) (int, error) { return 0, nil }

// TestInvokeRequiresWritePerm 锁住 R11-I1：invoke 是真实执行（MCP 工具可有写副作用），
// 必须要求 tool:write；仅持 tool:read（如 viewer）应被 403 拒绝。
func TestInvokeRequiresWritePerm(t *testing.T) {
	h := NewHandler(fakeRepo{})
	h.Authorize = func(_ *http.Request, p string) bool { return p == PermToolRead }

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost,
		"/api/tools/tl-1/invoke", strings.NewReader(`{"name":"x"}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("仅 tool:read 调 invoke 应 403，实得 %d", rec.Code)
	}
}
