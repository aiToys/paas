package backup

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/pkg/tenant"
)

// fakeRepo 内存实现 backup.Repository。
type fakeRepo struct{ items map[string]Backup }

func newFakeRepo() *fakeRepo {
	return &fakeRepo{items: map[string]Backup{
		"bk-1": {ID: "bk-1", TenantID: "t-acme", ResourceID: "ds-prod", Type: TypeFull, Status: StatusCompleted},
	}}
}

func (f *fakeRepo) List(_ context.Context, tid, res string) ([]Backup, error) {
	var out []Backup
	for _, b := range f.items {
		if b.TenantID == tid && (res == "" || b.ResourceID == res) {
			out = append(out, b)
		}
	}
	return out, nil
}
func (f *fakeRepo) ListAll(_ context.Context) ([]Backup, error) {
	out := make([]Backup, 0, len(f.items))
	for _, b := range f.items {
		out = append(out, b)
	}
	return out, nil
}
func (f *fakeRepo) Get(_ context.Context, tid, id string) (Backup, error) {
	b, ok := f.items[id]
	if !ok || b.TenantID != tid {
		return Backup{}, errNotFound("备份")
	}
	return b, nil
}
func (f *fakeRepo) Create(_ context.Context, b Backup) error {
	f.items[b.ID] = b
	return nil
}
func (f *fakeRepo) Delete(_ context.Context, tid, id string) error {
	delete(f.items, id)
	return nil
}

// fakeEnv 解析 resourceID→envID 与 envID→type。
type fakeEnv struct {
	resEnv  map[string]string // resourceID → envID
	envType map[string]string // envID → prod|test
}

func (e fakeEnv) EnvType(_ context.Context, envID string) (string, error) {
	if t, ok := e.envType[envID]; ok {
		return t, nil
	}
	return "", errNotFound("环境") // fail-closed
}
func (e fakeEnv) ResourceEnv(_ context.Context, res string) (string, error) {
	if env, ok := e.resEnv[res]; ok {
		return env, nil
	}
	return "", errNotFound("资源") // fail-closed
}

type errNotFound string

func (e errNotFound) Error() string { return string(e) }

func withTenant(r *http.Request, tid string) *http.Request {
	return r.WithContext(tenant.WithTenant(r.Context(), tid))
}

// perms 模拟角色权限集：developer 仅 dataservice:write（无 prod:write），admin 两者都有。
func authWith(perms ...string) func(*http.Request, string) bool {
	set := map[string]bool{}
	for _, p := range perms {
		set[p] = true
	}
	return func(_ *http.Request, p string) bool { return set[p] }
}

// TestBackupProdWriteEnforced 验证：生产数据服务的备份操作需 prod:write，
// developer（无 prod:write）在生产环境创建/删除备份被拦（403）。
func TestBackupProdWriteEnforced(t *testing.T) {
	env := fakeEnv{
		resEnv:  map[string]string{"ds-prod": "env-prod"},
		envType: map[string]string{"env-prod": "prod"},
	}
	h := NewHandler(newFakeRepo(), WithEnvResolver(env, env))
	h.Authorize = authWith("dataservice:read", "dataservice:write") // developer，无 prod:write

	// 创建生产资源备份 → 403。
	body := strings.NewReader(`{"resourceId":"ds-prod","type":"full"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodPost, "/api/backups", body), "t-acme"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("生产备份创建无 prod:write 应 403，实得 %d", rec.Code)
	}

	// 删除已有生产资源备份 → 403。
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodDelete, "/api/backups/bk-1", nil), "t-acme"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("生产备份删除无 prod:write 应 403，实得 %d", rec.Code)
	}
}

// TestBackupTestEnvAllowed 验证：测试环境备份操作无需 prod:write，developer 可执行。
func TestBackupTestEnvAllowed(t *testing.T) {
	env := fakeEnv{
		resEnv:  map[string]string{"ds-test": "env-test"},
		envType: map[string]string{"env-test": "test"},
	}
	h := NewHandler(newFakeRepo(), WithEnvResolver(env, env))
	h.Authorize = authWith("dataservice:read", "dataservice:write")

	body := strings.NewReader(`{"resourceId":"ds-test","type":"full"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodPost, "/api/backups", body), "t-acme"))
	if rec.Code != http.StatusOK {
		t.Fatalf("测试环境备份创建应放行，实得 %d", rec.Code)
	}
}

// TestBackupProdAllowedWithPerm 验证：有 prod:write 的 admin 在生产环境可创建备份。
func TestBackupProdAllowedWithPerm(t *testing.T) {
	env := fakeEnv{
		resEnv:  map[string]string{"ds-prod": "env-prod"},
		envType: map[string]string{"env-prod": "prod"},
	}
	h := NewHandler(newFakeRepo(), WithEnvResolver(env, env))
	h.Authorize = authWith("dataservice:read", "dataservice:write", "prod:write")

	body := strings.NewReader(`{"resourceId":"ds-prod","type":"full"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodPost, "/api/backups", body), "t-acme"))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin 生产备份创建应放行，实得 %d", rec.Code)
	}
	var wrap struct {
		Data Backup `json:"data"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&wrap)
	if wrap.Data.Status != StatusCompleted {
		t.Fatalf("mock 备份状态应为 completed")
	}
}

// TestBackupFailClosedOnUnknownResource 验证：未知资源（解析失败）fail-closed 需 prod:write。
func TestBackupFailClosedOnUnknownResource(t *testing.T) {
	env := fakeEnv{resEnv: map[string]string{}, envType: map[string]string{}}
	h := NewHandler(newFakeRepo(), WithEnvResolver(env, env))
	h.Authorize = authWith("dataservice:read", "dataservice:write") // 无 prod:write

	body := strings.NewReader(`{"resourceId":"ds-ghost","type":"full"}`)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodPost, "/api/backups", body), "t-acme"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("未知资源 fail-closed 应 403，实得 %d", rec.Code)
	}
}
