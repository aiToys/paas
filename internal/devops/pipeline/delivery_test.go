// delivery_test.go webhook 投递记录与幂等重放（D5-5）的行为锁。
//
// 锁定语义：
//   - 每次通过鉴权的投递都留记录（accepted/duplicate/skipped/rejected/failed + 原因）；
//   - 同 delivery ID 重复到达不新建 run（幂等），记录标 duplicate 且保留首次结果；
//   - token 校验失败不落记录（无租户身份，落库等于开放无界写入面）；
//   - 重放已有 run 的记录是 no-op；可重放记录新建 run 且 trigger=replay；
//   - 记录仓储按租户隔离。
package pipeline

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/pkg/tenant"
)

// newDeliveryTestHandler 建 memory store + 一个 webhook pipeline（glob=main）。
// 返回 handler / pid / token。
func newDeliveryTestHandler(t *testing.T, store *memoryStore) (*Handler, string, string) {
	t.Helper()
	ctx := acmeCtxEngine()
	if _, err := store.CreateTemplate(ctx, PipelineTemplate{
		ID: "tpl-wh-d", Name: "WH投递测试", Kind: KindCI,
		Stages: []StageDef{{Name: "构建", Type: StageBuild}},
	}); err != nil {
		t.Fatalf("创建模板失败: %v", err)
	}
	h := NewHandler(store, store, store, nil, WithDeliveries(store))
	h.Authorize = allowAll
	req := acmeReq(http.MethodPost, "/api/applications/app-whd/pipelines",
		`{"name":"p-whd","kind":"ci","templateId":"tpl-wh-d","trigger":{"type":"webhook","branch":"main"}}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建 pipeline 期望 201，got %d body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data Pipeline `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Data.Trigger.Token == "" {
		t.Fatal("webhook pipeline 应自动生成 token")
	}
	return h, resp.Data.ID, resp.Data.Trigger.Token
}

// postWebhook 发一次 webhook 投递（无租户 ctx，模拟外部源），返回状态码。
func postWebhook(t *testing.T, h *Handler, pid, token, deliveryID, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/pipeline/"+pid+"?token="+token, strings.NewReader(body))
	if deliveryID != "" {
		req.Header.Set("X-Gitea-Delivery", deliveryID)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestWebhookDeliveryIdempotent(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	h, pid, token := newDeliveryTestHandler(t, s)
	push := `{"ref":"refs/heads/main","after":"abc123"}`

	if code := postWebhook(t, h, pid, token, "dlv-1", push); code != http.StatusOK {
		t.Fatalf("首次投递期望 200，got %d", code)
	}
	runs, _ := s.ListRuns(ctx, "", pid, "")
	if len(runs) != 1 {
		t.Fatalf("首次投递应生成 1 个 run，got %d", len(runs))
	}
	runID := runs[0].ID

	// 同 delivery ID 重发（webhook 源重试）：不新建 run，记录标 duplicate 且保留 run 关联。
	if code := postWebhook(t, h, pid, token, "dlv-1", push); code != http.StatusOK {
		t.Fatalf("重复投递期望 200（静默），got %d", code)
	}
	runs, _ = s.ListRuns(ctx, "", pid, "")
	if len(runs) != 1 {
		t.Fatalf("重复投递不应新建 run，got %d", len(runs))
	}
	rec, err := s.GetDelivery(ctx, pid, "dlv-1")
	if err != nil {
		t.Fatalf("投递记录应存在: %v", err)
	}
	if rec.Status != DeliveryDuplicate {
		t.Errorf("重复投递记录 status 期望 duplicate，got %s", rec.Status)
	}
	if rec.RunID != runID {
		t.Errorf("重复投递记录应保留首次 run，期望 %s got %s", runID, rec.RunID)
	}
	if !strings.Contains(rec.Reason, "首次") {
		t.Errorf("duplicate 原因应保留首次结果，got %q", rec.Reason)
	}
}

func TestWebhookDeliverySkippedReasons(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	h, pid, token := newDeliveryTestHandler(t, s)

	cases := []struct {
		name     string
		body     string
		wantSub  string
		wantCode int
	}{
		// glob 不匹配：静默 200（防重试风暴），但记录原因（用户可查「push 了为何不触发」）。
		{"分支不匹配 glob", `{"ref":"refs/heads/feature-x","after":"c1"}`, "glob", http.StatusOK},
		{"非分支引用", `{"ref":"refs/tags/v1.0.0","after":"c2"}`, "非分支引用", http.StatusOK},
		{"分支删除", `{"ref":"refs/heads/main","after":"0000000000000000000000000000000000000000"}`, "分支删除", http.StatusOK},
		{"非法请求体", `not-json`, "push event", http.StatusBadRequest},
	}
	for i, c := range cases {
		did := "dlv-skip-" + string(rune('a'+i))
		if code := postWebhook(t, h, pid, token, did, c.body); code != c.wantCode {
			t.Fatalf("%s 期望 %d，got %d", c.name, c.wantCode, code)
		}
		rec, err := s.GetDelivery(ctx, pid, did)
		if err != nil {
			t.Fatalf("%s 应落投递记录: %v", c.name, err)
		}
		if !strings.Contains(rec.Reason, c.wantSub) {
			t.Errorf("%s 记录原因应包含 %q，got %q", c.name, c.wantSub, rec.Reason)
		}
		if rec.Status == DeliveryAccepted {
			t.Errorf("%s 不应标 accepted", c.name)
		}
	}
	runs, _ := s.ListRuns(ctx, "", pid, "")
	if len(runs) != 0 {
		t.Fatalf("跳过的投递不应生成 run，got %d", len(runs))
	}
}

func TestWebhookDeliveryTokenFailureNotRecorded(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	h, pid, _ := newDeliveryTestHandler(t, s)

	// 错误 token → 401；无租户身份可归属，落库等于开放无界写入面，故不记记录。
	if code := postWebhook(t, h, pid, "wrong-token", "dlv-bad", `{"ref":"refs/heads/main","after":"c1"}`); code != http.StatusUnauthorized {
		t.Fatalf("错误 token 期望 401，got %d", code)
	}
	list, err := s.ListDeliveries(ctx, pid, 10)
	if err != nil {
		t.Fatalf("列表失败: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("token 校验失败不应落投递记录，got %d", len(list))
	}
}

func TestReplayDeliveryIdempotentForExistingRun(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	h, pid, token := newDeliveryTestHandler(t, s)

	if code := postWebhook(t, h, pid, token, "dlv-r1", `{"ref":"refs/heads/main","after":"abc123"}`); code != http.StatusOK {
		t.Fatalf("webhook 触发期望 200，got %d", code)
	}
	runs, _ := s.ListRuns(ctx, "", pid, "")
	if len(runs) != 1 {
		t.Fatalf("期望 1 个 run，got %d", len(runs))
	}
	// 重放已生成 run 的记录：no-op，返回原 run，不新建、不增 replays。
	req := acmeReq(http.MethodPost, "/api/applications/app-whd/pipelines/"+pid+"/deliveries/dlv-r1/replay", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("重放期望 200，got %d body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Replayed bool        `json:"replayed"`
			Reason   string      `json:"reason"`
			Run      PipelineRun `json:"run"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Data.Replayed {
		t.Error("已有 run 的重放应为 no-op（replayed=false）")
	}
	if resp.Data.Run.ID != runs[0].ID {
		t.Errorf("应返回原 run %s，got %s", runs[0].ID, resp.Data.Run.ID)
	}
	runs, _ = s.ListRuns(ctx, "", pid, "")
	if len(runs) != 1 {
		t.Fatalf("no-op 重放不应新建 run，got %d", len(runs))
	}
	d, _ := s.GetDelivery(ctx, pid, "dlv-r1")
	if d.Replays != 0 {
		t.Errorf("no-op 重放不应计入 replays，got %d", d.Replays)
	}
}

func TestReplayDeliveryCreatesRun(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	h, pid, _ := newDeliveryTestHandler(t, s)

	// 构造一条跳过的历史投递（如当时被 glob 拦下），人工重放应新建 run。
	if _, err := s.CreateDelivery(ctx, WebhookDelivery{
		ID: "dlv-replay", PipelineID: pid, AppID: "app-whd", Event: "push",
		Branch: "main", Commit: "deadbeef", Status: DeliverySkipped, Reason: "分支 main 不匹配触发 glob",
	}); err != nil {
		t.Fatalf("构造投递记录失败: %v", err)
	}
	req := acmeReq(http.MethodPost, "/api/applications/app-whd/pipelines/"+pid+"/deliveries/dlv-replay/replay", "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("重放期望 201，got %d body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Replayed bool            `json:"replayed"`
			Run      PipelineRun     `json:"run"`
			Delivery WebhookDelivery `json:"delivery"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Data.Replayed {
		t.Error("重放应新建 run（replayed=true）")
	}
	if resp.Data.Run.Trigger != TriggerReplay {
		t.Errorf("run trigger 期望 replay，got %s", resp.Data.Run.Trigger)
	}
	if resp.Data.Run.Branch != "main" || resp.Data.Run.Commit != "deadbeef" {
		t.Errorf("重放应复用历史 branch/commit，got %s/%s", resp.Data.Run.Branch, resp.Data.Run.Commit)
	}
	d, _ := s.GetDelivery(ctx, pid, "dlv-replay")
	if d.Status != DeliveryAccepted || d.RunID != resp.Data.Run.ID || d.Replays != 1 {
		t.Errorf("记录应更新为 accepted/run 关联/replays=1，got %+v", d)
	}
	if d.LastReplayAt == nil {
		t.Error("重放应记录 lastReplayAt")
	}
	if !strings.Contains(d.Reason, "人工重放") {
		t.Errorf("记录原因应标注人工重放，got %q", d.Reason)
	}
}

func TestDeliveryEndpointsPermissions(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	h, pid, _ := newDeliveryTestHandler(t, s)
	if _, err := s.CreateDelivery(ctx, WebhookDelivery{
		ID: "dlv-perm", PipelineID: pid, AppID: "app-whd", Event: "push",
		Branch: "main", Commit: "c1", Status: DeliverySkipped, Reason: "测试",
	}); err != nil {
		t.Fatalf("构造投递记录失败: %v", err)
	}
	base := "/api/applications/app-whd/pipelines/" + pid + "/deliveries"

	// 只读权限：列表 200，重放 403（重放会触发部署，必须 pipeline:write）。
	h.Authorize = func(_ *http.Request, perm string) bool { return perm == PermPipelineRead }
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodGet, base, ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("只读权限列表期望 200，got %d body %s", rec.Code, rec.Body.String())
	}
	var listResp struct {
		Data []WebhookDelivery `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listResp)
	if len(listResp.Data) != 1 || listResp.Data[0].ID != "dlv-perm" {
		t.Errorf("列表示例不符: %+v", listResp.Data)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, base+"/dlv-perm/replay", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("只读权限重放期望 403，got %d", rec.Code)
	}

	// 路径 appID 与实际归属不符统一 404（防同租户跨应用移花接木）。
	h.Authorize = allowAll
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, "/api/applications/app-other/pipelines/"+pid+"/deliveries/dlv-perm/replay", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("跨应用重放期望 404，got %d", rec.Code)
	}
	// 不存在的投递 → 404。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, acmeReq(http.MethodPost, base+"/nope/replay", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在投递重放期望 404，got %d", rec.Code)
	}
}

func TestDeliveryTenantIsolation(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtxEngine()
	if _, err := s.CreateDelivery(ctx, WebhookDelivery{
		ID: "dlv-iso", PipelineID: "p-iso", AppID: "app-iso", Event: "push",
		Branch: "main", Commit: "c1", Status: DeliveryAccepted,
	}); err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	// 跨租户 Get/List 一律不可见（不泄漏存在性）。
	other := tenant.WithTenant(context.Background(), "t-globex")
	if _, err := s.GetDelivery(other, "p-iso", "dlv-iso"); err != ErrDeliveryNotFound {
		t.Errorf("跨租户 Get 期望 ErrDeliveryNotFound，got %v", err)
	}
	if list, err := s.ListDeliveries(other, "p-iso", 10); err != nil || len(list) != 0 {
		t.Errorf("跨租户 List 应为空，got %d err %v", len(list), err)
	}
	// 本租户可见，且归属以 ctx 为准。
	got, err := s.GetDelivery(ctx, "p-iso", "dlv-iso")
	if err != nil || got.TenantID != "t-acme" {
		t.Errorf("本租户应可见且归属 t-acme，got %+v err %v", got, err)
	}
}
