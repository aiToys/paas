package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/internal/core/identity"
	idmemory "github.com/aitoys/paas/internal/core/identity/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// newTestIDB 构造带一个 acme developer key 的 identity 仓储。
func newTestIDB(t *testing.T) identity.Repository {
	t.Helper()
	idb := idmemory.NewStore()
	require.NoError(t, idb.CreateTenant(context.Background(), identity.Tenant{ID: "t-acme", Name: "Acme", CreatedAt: time.Now()}))
	require.NoError(t, idb.CreateAPIKey(context.Background(), identity.APIKey{
		ID: "k1", TenantID: "t-acme", UserID: "u1", Roles: []string{"developer"}, Key: "sk-acme-dev",
	}))
	return idb
}

func TestAPIKeyAuthInjectsTenantAndRoles(t *testing.T) {
	idb := newTestIDB(t)
	var gotTenant, gotUserID, gotRoles string
	h := APIKeyAuth(idb)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid, _ := tenant.TenantFrom(r.Context())
		gotTenant = tid
		gotUserID = UserIDFrom(r.Context())
		rs, _ := RolesFrom(r.Context())
		gotRoles = rs[0]
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer sk-acme-dev")
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "t-acme", gotTenant)
	assert.Equal(t, "u1", gotUserID)
	assert.Equal(t, "developer", gotRoles)
}

func TestAPIKeyAuthRejectsMissingOrWrong(t *testing.T) {
	idb := newTestIDB(t)
	h := APIKeyAuth(idb)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("不应放行")
	}))

	// 缺失
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	// 错误
	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	h.ServeHTTP(rec2, req)
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)
}
