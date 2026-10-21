package configcenter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/configcenter"
	"github.com/aitoys/paas/internal/configcenter/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// admin handler 跨租户语义锁（D3 spec §8.6）：聚合列表 / 详情掩码与键 diff / 引用影响面。
// 用 memory store（双实现同接口，行为契约一致）。
func TestAdminHandlerCrossTenant(t *testing.T) {
	s := memory.NewStore()
	ctxA := tenant.WithTenant(t.Context(), "t-acme")

	// acme：app ns 两个 item + 发布一次后删一个 key（制造 removed diff）+ 再加一个新 key（added）。
	ns, err := s.EnsureByApp(ctxA, "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertItem(ctxA, configcenter.ConfigItem{NamespaceID: ns.ID, Key: "k1", Value: "secret-1", Type: "text"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertItem(ctxA, configcenter.ConfigItem{NamespaceID: ns.ID, Key: "k2", Value: "secret-2", Type: "text"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePublish(ctxA, ns.ID); err != nil {
		t.Fatal(err)
	}
	items, err := s.ListItems(ctxA, ns.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.Key == "k2" {
			if err := s.DeleteItem(ctxA, it.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.UpsertItem(ctxA, configcenter.ConfigItem{NamespaceID: ns.ID, Key: "k3", Value: "secret-3", Type: "text"}); err != nil {
		t.Fatal(err)
	}

	h := configcenter.NewAdminHandler(s)

	// 列表：跨租户聚合（draftItems=2，activeVersion=1，refCount=0）。
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/configcenter/namespaces", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("列表应 200，got %d body=%s", rec.Code, rec.Body.String())
	}
	var listResp struct {
		Data []configcenter.AdminNamespaceRow `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if len(listResp.Data) != 1 || listResp.Data[0].DraftItems != 2 || listResp.Data[0].ActiveVersion != 1 {
		t.Fatalf("聚合行不符: %+v", listResp.Data)
	}

	// 详情：值全掩码、Snapshot 只列 keys、diff added=[k3] removed=[k2]。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/configcenter/namespaces/"+ns.ID, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("详情应 200，got %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "secret-1") || strings.Contains(body, "secret-2") || strings.Contains(body, "secret-3") {
		t.Fatalf("详情泄漏明文: %s", body)
	}
	if !strings.Contains(body, "••••••") {
		t.Fatalf("详情应有掩码值")
	}
	var detail struct {
		Data configcenter.AdminNSDetail `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Data.Active == nil || len(detail.Data.Active.Keys) != 2 {
		t.Fatalf("active keys 应为 [k1 k2]: %+v", detail.Data.Active)
	}
	if len(detail.Data.Diff.Added) != 1 || detail.Data.Diff.Added[0] != "k3" {
		t.Fatalf("diff.added 应 [k3]: %+v", detail.Data.Diff)
	}
	if len(detail.Data.Diff.Removed) != 1 || detail.Data.Diff.Removed[0] != "k2" {
		t.Fatalf("diff.removed 应 [k2]: %+v", detail.Data.Diff)
	}
	if len(detail.Data.Publishes) != 1 {
		t.Fatalf("发布记录应 1 条: %+v", detail.Data.Publishes)
	}

	// 不存在的 ns → 404。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/configcenter/namespaces/ns-404", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在应 404，got %d", rec.Code)
	}
}
