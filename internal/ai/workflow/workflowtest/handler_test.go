package workflowtest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/internal/ai/workflow"
	"github.com/aitoys/paas/internal/ai/workflow/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// 包内 handler 测试：CRUD + 校验 400 + 触发 + 权限。引擎全链路见 workflowtest。
func newTestHandler(t *testing.T) *workflow.Handler {
	repo := memory.NewStore()
	eng := workflow.NewEngine(repo, nil, nil)
	return workflow.NewHandler(repo, eng)
}

func req(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, path, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	return r.WithContext(tenant.WithTenant(context.Background(), "t-acme"))
}

func ticketBody() map[string]any {
	return map[string]any{
		"name": "ticket-flow", "enabled": true,
		"nodes": []map[string]any{
			{"id": "s", "type": "start", "nextId": "cls"},
			{"id": "cls", "type": "llm", "nextId": "e",
				"config": map[string]any{"agentId": "a-1", "inputTemplate": "分析：{{inputs.q}}"}},
			{"id": "e", "type": "end"},
		},
	}
}

func TestHandlerCRUD(t *testing.T) {
	h := newTestHandler(t)
	// 创建
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "POST", "/api/workflows", ticketBody()))
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", w.Code, w.Body.String())
	}
	var created struct{ Data workflow.WorkflowDef }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析 create 响应: %v", err)
	}
	// 列表
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "GET", "/api/workflows", nil))
	var list struct{ Data []workflow.WorkflowDef }
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("解析 list 响应: %v", err)
	}
	if len(list.Data) != 1 {
		t.Fatalf("list = %d", len(list.Data))
	}
	// 详情
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "GET", "/api/workflows/"+created.Data.ID, nil))
	if w.Code != 200 {
		t.Fatalf("get = %d", w.Code)
	}
	// 更新
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "PUT", "/api/workflows/"+created.Data.ID, ticketBody()))
	if w.Code != 200 {
		t.Fatalf("put = %d: %s", w.Code, w.Body.String())
	}
	// 删除
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "DELETE", "/api/workflows/"+created.Data.ID, nil))
	if w.Code != 200 {
		t.Fatalf("delete = %d", w.Code)
	}
	// 删后 404
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "GET", "/api/workflows/"+created.Data.ID, nil))
	if w.Code != 404 {
		t.Fatalf("get after delete = %d", w.Code)
	}
}

func TestHandlerRejectsInvalidDef(t *testing.T) {
	h := newTestHandler(t)
	// 缺 end
	body := ticketBody()
	body["nodes"] = body["nodes"].([]map[string]any)[:2]
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "POST", "/api/workflows", body))
	if w.Code != 400 {
		t.Fatalf("invalid def = %d: %s", w.Code, w.Body.String())
	}
	// 重名 409
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "POST", "/api/workflows", ticketBody()))
	if w.Code != 201 {
		t.Fatal("first create failed")
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "POST", "/api/workflows", ticketBody()))
	if w.Code != 409 {
		t.Fatalf("dup name = %d", w.Code)
	}
}

func TestHandlerRunLifecycle(t *testing.T) {
	h := newTestHandler(t)
	// 建一个纯 llm 工作流（引擎无 runner → llm 失败 → run failed，验证链路通畅）
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "POST", "/api/workflows", ticketBody()))
	var created struct{ Data workflow.WorkflowDef }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析 create 响应: %v", err)
	}
	// 触发
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "POST", fmt.Sprintf("/api/workflows/%s/runs", created.Data.ID), map[string]string{"q": "x"}))
	if w.Code != 201 {
		t.Fatalf("trigger = %d: %s", w.Code, w.Body.String())
	}
	var run struct{ Data workflow.WorkflowRun }
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatalf("解析 run 响应: %v", err)
	}
	// 详情
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "GET", "/api/workflows/runs/"+run.Data.ID, nil))
	if w.Code != 200 {
		t.Fatalf("run detail = %d", w.Code)
	}
	// 历史
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "GET", fmt.Sprintf("/api/workflows/%s/runs", created.Data.ID), nil))
	if w.Code != 200 {
		t.Fatalf("run list = %d", w.Code)
	}
}

func TestHandlerPermissionDenied(t *testing.T) {
	h := newTestHandler(t)
	h.Authorize = func(r *http.Request, perm string) bool { return false }
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "GET", "/api/workflows", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("forbidden = %d", w.Code)
	}
}

func TestHandlerDisabledWorkflowRejectsRun(t *testing.T) {
	h := newTestHandler(t)
	body := ticketBody()
	body["enabled"] = false
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "POST", "/api/workflows", body))
	var created struct{ Data workflow.WorkflowDef }
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("解析 create 响应: %v", err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req(t, "POST", fmt.Sprintf("/api/workflows/%s/runs", created.Data.ID), map[string]string{}))
	if w.Code != 409 {
		t.Fatalf("disabled run = %d: %s", w.Code, w.Body.String())
	}
}
