package configcenter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/configcenter"
	ccmemory "github.com/aitoys/paas/internal/configcenter/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

func TestLaneBatchRequiresPreviewAndRejectsStaleValues(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	repo := ccmemory.NewStore()
	h := configcenter.NewAppHandler(repo)
	h.Authorize = func(*http.Request, string) bool { return true }
	request := func(method, suffix, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/api/applications/app-r/dynamic-configs/lane-overrides"+suffix+"envId=e&lane=l", strings.NewReader(body)).WithContext(ctx))
		return w
	}
	if w := request("POST", "/batch?", `{"ops":[{"action":"upsert","key":"k","value":"A"}]}`); w.Code != 400 {
		t.Fatalf("缺版本不应提交: %d", w.Code)
	}
	preview := request("GET", "?preview=1&", "")
	revision := preview.Header().Get("X-Config-Revision")
	if preview.Code != 200 || revision == "" {
		t.Fatal(preview.Code, preview.Body.String())
	}
	row, err := repo.UpsertLaneOverride(ctx, configcenter.LaneOverride{AppID: "app-r", EnvID: "e", LaneID: "l", Key: "k", Value: "A"})
	if err != nil {
		t.Fatal(err)
	}
	preview = request("GET", "?preview=1&", "")
	revision = preview.Header().Get("X-Config-Revision")
	row.Value = "B"
	if _, err = repo.UpsertLaneOverride(ctx, row); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(revision)
	w := request("POST", "/batch?", fmt.Sprintf(`{"expectedRevision":%s,"ops":[{"action":"upsert","key":"k","value":"C"}]}`, encoded))
	if w.Code != 409 {
		t.Fatalf("过期预览未拒绝: %d %s", w.Code, w.Body.String())
	}
}
