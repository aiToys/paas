package lane_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/devops/pipeline"
	envmemory "github.com/aitoys/paas/internal/environment/memory"
	"github.com/aitoys/paas/internal/lane"
	lanememory "github.com/aitoys/paas/internal/lane/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// newHandler 构造集成 handler：真实 lane/env 内存仓储 + stub 鉴权。
// prodWrite=true 模拟 admin，false 模拟 developer（生产只读）。
func newHandler(prodWrite bool, opts ...lane.HandlerOpt) *lane.Handler {
	h := lane.NewHandler(lanememory.NewStore(),
		append([]lane.HandlerOpt{lane.WithEnvResolver(envmemory.NewStore())}, opts...)...)
	h.Authorize = func(r *http.Request, perm string) bool {
		if perm == lane.PermProdWrite {
			return prodWrite
		}
		return true
	}
	return h
}

func acmeCtx() context.Context { return tenant.WithTenant(context.Background(), "t-acme") }

func decodeData(t *testing.T, body []byte, v interface{}) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("解码信封失败: %v (body=%s)", err, string(body))
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatalf("解码 data 失败: %v (data=%s)", err, string(env.Data))
	}
}

func req(ctx context.Context, method, path string, body interface{}) *http.Request {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	return rq.WithContext(ctx)
}

// fakeAudit 收集审计记录（断言写操作记审计）。
type fakeAudit struct{ records []string }

func (f *fakeAudit) Record(_ context.Context, _, actor, action, _, resourceID, detail string) error {
	f.records = append(f.records, action+" "+resourceID+" "+detail)
	return nil
}

// fakeRuns 桩 RunLister（Detail 聚合 + Close 前置校验）。
type fakeRuns struct {
	summaries []pipeline.RunSummary
	err       error
}

func (f *fakeRuns) ListByBranch(_ context.Context, branch string) ([]pipeline.RunSummary, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []pipeline.RunSummary
	for _, s := range f.summaries {
		if s.Branch == branch {
			out = append(out, s)
		}
	}
	return out, nil
}

func validLane() map[string]any {
	return map[string]any{"envId": "env-acme-test", "name": "feature-x", "description": "联调"}
}

// TestHandlerCRUD 验证基础 CRUD 往返。
func TestHandlerCRUD(t *testing.T) {
	h := newHandler(true)
	// 创建
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	if w.Code != http.StatusCreated {
		t.Fatalf("创建应 201，got %d: %s", w.Code, w.Body.String())
	}
	var created lane.Lane
	decodeData(t, w.Body.Bytes(), &created)
	if created.ID == "" || created.Status != lane.StatusActive || created.Mode != lane.ModeStandard {
		t.Fatalf("创建默认值不符: %+v", created)
	}
	// 列表（按 envId 过滤）
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "GET", "/api/lanes?envId=env-acme-test", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("列表应 200，got %d", w.Code)
	}
	var list []lane.Lane
	decodeData(t, w.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Name != "feature-x" {
		t.Fatalf("列表不符: %+v", list)
	}
	// 更新（mode/description）
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "PUT", "/api/lanes/"+created.ID, map[string]any{
		"mode": lane.ModePermanent, "description": "常驻", "externalLink": "PROJ-1",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("更新应 200，got %d: %s", w.Code, w.Body.String())
	}
	var updated lane.Lane
	decodeData(t, w.Body.Bytes(), &updated)
	if updated.Mode != lane.ModePermanent || updated.ExternalLink != "PROJ-1" {
		t.Fatalf("更新不符: %+v", updated)
	}
}

// TestHandlerCreateForbidden 验证普通权限（governance:write）拒绝时 403。
func TestHandlerCreateForbidden(t *testing.T) {
	h := newHandler(true)
	h.Authorize = func(r *http.Request, perm string) bool { return false }
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	if w.Code != http.StatusForbidden {
		t.Fatalf("无权限应 403，got %d", w.Code)
	}
}

// TestHandlerCreateProdDenied 验证生产环境建泳道需 prod:write（developer 403）。
func TestHandlerCreateProdDenied(t *testing.T) {
	h := newHandler(false)
	body := validLane()
	body["envId"] = "env-acme-prod-bj"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", body))
	if w.Code != http.StatusForbidden {
		t.Fatalf("生产建泳道应 403，got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandlerCreateProdEnvUnknownFailClosed 验证环境查询失败也按生产处理（fail-closed）。
func TestHandlerCreateProdEnvUnknownFailClosed(t *testing.T) {
	h := newHandler(false)
	body := validLane()
	body["envId"] = "env-not-exist"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", body))
	if w.Code != http.StatusForbidden {
		t.Fatalf("环境查不到应 fail-closed 403，got %d", w.Code)
	}
}

// TestHandlerCreateConflict 验证同租户×环境×名唯一冲突 409。
func TestHandlerCreateConflict(t *testing.T) {
	h := newHandler(true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	if w.Code != http.StatusCreated {
		t.Fatalf("首次创建应 201，got %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	if w.Code != http.StatusConflict {
		t.Fatalf("重复创建应 409，got %d", w.Code)
	}
}

// TestHandlerCreateNameInvalid 验证非法名（非 DNS-1035）400。
func TestHandlerCreateNameInvalid(t *testing.T) {
	h := newHandler(true)
	body := validLane()
	body["name"] = "Feature_X"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", body))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法名应 400，got %d", w.Code)
	}
}

// TestHandlerCrossTenantNotFound 验证跨租户 Get 404 不泄漏。
func TestHandlerCrossTenantNotFound(t *testing.T) {
	h := newHandler(true)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	if w.Code != http.StatusCreated {
		t.Fatalf("创建应 201，got %d", w.Code)
	}
	var created lane.Lane
	decodeData(t, w.Body.Bytes(), &created)

	globexCtx := tenant.WithTenant(context.Background(), "t-globex")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(globexCtx, "GET", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨租户 Get 应 404，got %d", w.Code)
	}
}

// TestHandlerClose 验证 DELETE 语义：标记 closed + 幂等 + active run 时 409。
func TestHandlerClose(t *testing.T) {
	runs := &fakeRuns{}
	h := newHandler(true, lane.WithRunLister(runs))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	var created lane.Lane
	decodeData(t, w.Body.Bytes(), &created)

	// 有进行中 run（branch==name 且非终态）→ 409
	runs.summaries = []pipeline.RunSummary{{ID: "run-1", Branch: "feature-x"}}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "DELETE", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusConflict {
		t.Fatalf("有进行中 run 关闭应 409，got %d: %s", w.Code, w.Body.String())
	}

	// run 终态后可关
	runs.summaries = []pipeline.RunSummary{{ID: "run-1", Branch: "feature-x", FinishedAt: time.Now()}}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "DELETE", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("关闭应 200，got %d: %s", w.Code, w.Body.String())
	}
	var closed lane.Lane
	decodeData(t, w.Body.Bytes(), &closed)
	if closed.Status != lane.StatusClosed {
		t.Fatalf("关闭后状态不符: %+v", closed)
	}

	// 已 closed 再 DELETE 幂等 200
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "DELETE", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("重复关闭应幂等 200，got %d", w.Code)
	}
}

// TestHandlerDetailAggregation 验证 Detail 聚合（nil listers 返空切片）。
func TestHandlerDetailAggregation(t *testing.T) {
	h := newHandler(true, lane.WithRunLister(&fakeRuns{
		summaries: []pipeline.RunSummary{{ID: "run-1", Branch: "feature-x", Status: pipeline.RunSucceeded}},
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	var created lane.Lane
	decodeData(t, w.Body.Bytes(), &created)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "GET", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("详情应 200，got %d", w.Code)
	}
	var d lane.LaneDetail
	decodeData(t, w.Body.Bytes(), &d)
	if d.Lane.ID != created.ID {
		t.Fatalf("详情 Lane 不符: %+v", d)
	}
	if len(d.RecentRuns) != 1 || d.RecentRuns[0].ID != "run-1" {
		t.Fatalf("RecentRuns 不符: %+v", d.RecentRuns)
	}
	if d.Workloads == nil {
		t.Fatalf("Workloads 应为空切片非 nil（前端消费一致性）")
	}
}

// TestHandlerAudit 验证 Create/Update/Close 记审计。
func TestHandlerAudit(t *testing.T) {
	audit := &fakeAudit{}
	h := newHandler(true, lane.WithAudit(audit))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	var created lane.Lane
	decodeData(t, w.Body.Bytes(), &created)

	h.ServeHTTP(httptest.NewRecorder(), req(acmeCtx(), "PUT", "/api/lanes/"+created.ID, map[string]any{"description": "改"}))
	h.ServeHTTP(httptest.NewRecorder(), req(acmeCtx(), "DELETE", "/api/lanes/"+created.ID, nil))

	joined := strings.Join(audit.records, "\n")
	for _, want := range []string{"lane_create", "lane_update", "lane_close"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("审计缺失 %s，records=%s", want, joined)
		}
	}
}

// TestHandlerCloseRunListerError 验证 RunLister 查询失败 fail-closed（500，不静默关闭）。
func TestHandlerCloseRunListerError(t *testing.T) {
	h := newHandler(true, lane.WithRunLister(&fakeRuns{err: errors.New("boom")}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "POST", "/api/lanes", validLane()))
	var created lane.Lane
	decodeData(t, w.Body.Bytes(), &created)

	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(acmeCtx(), "DELETE", "/api/lanes/"+created.ID, nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("RunLister 失败应 500 fail-closed，got %d", w.Code)
	}
}
