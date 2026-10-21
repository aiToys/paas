package maas

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aitoys/paas/pkg/provider"
)

func doReq(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr
}

func TestHandlerModelCRUD(t *testing.T) {
	repo := NewMemoryStore()
	gw := &fakeRegistrar{registered: map[string]*provider.Model{}}
	h := NewHandler(repo, gw, nil)

	// Create
	rr := doReq(t, h, "POST", "/api/admin/models", map[string]any{
		"id": "m1", "name": "M1", "vendor": "OpenAI", "contextWindow": 8192, "capabilities": []string{"chat"},
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create want 201, got %d body=%s", rr.Code, rr.Body.String())
	}
	if _, ok := gw.registered["m1"]; !ok {
		t.Fatal("create 后 gateway 未刷新")
	}

	// 重复
	rr = doReq(t, h, "POST", "/api/admin/models", map[string]any{"id": "m1", "name": "M1"})
	if rr.Code != http.StatusConflict {
		t.Fatalf("重复 want 409, got %d", rr.Code)
	}
	// 空 id
	rr = doReq(t, h, "POST", "/api/admin/models", map[string]any{"name": "x"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("空 id want 400, got %d", rr.Code)
	}

	// List
	rr = doReq(t, h, "GET", "/api/admin/models", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list want 200, got %d", rr.Code)
	}
	var listResp struct {
		Data []*provider.Model `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &listResp)
	if len(listResp.Data) != 1 {
		t.Fatalf("list want 1, got %d", len(listResp.Data))
	}

	// Get
	rr = doReq(t, h, "GET", "/api/admin/models/m1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get want 200, got %d", rr.Code)
	}
	// Get not found
	rr = doReq(t, h, "GET", "/api/admin/models/nope", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("get not found want 404, got %d", rr.Code)
	}

	// Update
	rr = doReq(t, h, "PUT", "/api/admin/models/m1", map[string]any{"name": "改名"})
	if rr.Code != http.StatusOK {
		t.Fatalf("update want 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	// Delete
	rr = doReq(t, h, "DELETE", "/api/admin/models/m1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete want 200, got %d", rr.Code)
	}
	if _, ok := gw.registered["m1"]; ok {
		t.Fatal("delete 后 gateway 未注销")
	}
	// Delete not found
	rr = doReq(t, h, "DELETE", "/api/admin/models/m1", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("delete not found want 404, got %d", rr.Code)
	}
}

func TestHandlerChannelCRUD(t *testing.T) {
	repo := NewMemoryStore()
	gw := &fakeRegistrar{registered: map[string]*provider.Model{}}
	h := NewHandler(repo, gw, nil)
	doReq(t, h, "POST", "/api/admin/models", map[string]any{"id": "m1", "name": "M1", "vendor": "v"})

	// Create channel（status 空补 healthy）
	rr := doReq(t, h, "POST", "/api/admin/models/m1/channels", map[string]any{
		"id": "c1", "type": ProviderEcho, "priority": 0,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create channel want 201, got %d body=%s", rr.Code, rr.Body.String())
	}
	m := gw.registered["m1"]
	if m == nil || len(m.Channels) != 1 || m.Channels[0].Impl() == nil {
		t.Fatalf("channel 创建后 gateway 未正确刷新: %+v", m)
	}

	// List channels
	rr = doReq(t, h, "GET", "/api/admin/models/m1/channels", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list channels want 200, got %d", rr.Code)
	}

	// Update channel + gateway 刷新
	rr = doReq(t, h, "PUT", "/api/admin/models/m1/channels/c1", map[string]any{"priority": 5, "status": provider.StatusOffline})
	if rr.Code != http.StatusOK {
		t.Fatalf("update channel want 200, got %d", rr.Code)
	}
	m = gw.registered["m1"]
	if m.Channels[0].Priority != 5 || m.Channels[0].Status != provider.StatusOffline {
		t.Fatalf("channel update 未刷新 gateway: %+v", m.Channels[0])
	}

	// Delete channel + gateway 刷新（通道数减 0）
	rr = doReq(t, h, "DELETE", "/api/admin/models/m1/channels/c1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete channel want 200, got %d", rr.Code)
	}
	m = gw.registered["m1"]
	if len(m.Channels) != 0 {
		t.Fatal("delete channel 后 gateway 未刷新（应无通道）")
	}

	// channel 操作 model 不存在
	rr = doReq(t, h, "POST", "/api/admin/models/none/channels", map[string]any{"id": "x", "type": ProviderEcho})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("channel model not found want 404, got %d", rr.Code)
	}
}

// statusReaderRegistrar 实现 GatewayRegistrar + provider.ChannelStatusReader，
// 模拟 gateway 请求级 failover 打标后的运行态投影（R3 审计 H-1 回归锁）。
type statusReaderRegistrar struct {
	fakeRegistrar
	status map[string]string // "modelID/channelID" -> status
}

func (f *statusReaderRegistrar) ChannelStatus(modelID, channelID string) string {
	return f.status[modelID+"/"+channelID]
}

func TestHandlerChannelStatusProjection(t *testing.T) {
	repo := NewMemoryStore()
	gw := &statusReaderRegistrar{
		fakeRegistrar: fakeRegistrar{registered: map[string]*provider.Model{}},
		status:        map[string]string{},
	}
	h := NewHandler(repo, gw, nil)

	// 建模型 + 通道（store 值 healthy）
	_ = repo.CreateModel(t.Context(), &provider.Model{ID: "m1", Name: "M1"})
	_ = repo.CreateChannel(t.Context(), "m1", &provider.Channel{ID: "ch1", Status: provider.StatusHealthy})

	// 无运行态打标：保持 store 值
	rr := doReq(t, h, "GET", "/api/admin/models/m1/channels", nil)
	var listResp struct {
		Data []*provider.Channel `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &listResp)
	if listResp.Data[0].Status != provider.StatusHealthy {
		t.Fatalf("未打标应保持 store 值 healthy，got %s", listResp.Data[0].Status)
	}

	// 请求级 failover 打标 offline（只写 gateway 内存，不回写 store）
	gw.status["m1/ch1"] = provider.StatusOffline
	rr = doReq(t, h, "GET", "/api/admin/models/m1/channels", nil)
	_ = json.Unmarshal(rr.Body.Bytes(), &listResp)
	if listResp.Data[0].Status != provider.StatusOffline {
		t.Fatalf("打标后投影应显示 offline，got %s", listResp.Data[0].Status)
	}
	// store 值未被回写污染
	sc, _ := repo.ListChannels(t.Context(), "m1")
	if sc[0].Status != provider.StatusHealthy {
		t.Fatalf("store 值不应被投影污染，got %s", sc[0].Status)
	}

	// 模型详情同投影
	rr = doReq(t, h, "GET", "/api/admin/models/m1", nil)
	var modelResp struct {
		Data provider.Model `json:"data"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &modelResp)
	if len(modelResp.Data.Channels) > 0 && modelResp.Data.Channels[0].Status != provider.StatusOffline {
		t.Fatalf("模型详情投影应显示 offline，got %s", modelResp.Data.Channels[0].Status)
	}
}
