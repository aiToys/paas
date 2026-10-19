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

// UpdateProgress / ListActive 补齐 Repository 新面（真实快照推进用，handler 测试不触及）。
func (f *fakeRepo) UpdateProgress(_ context.Context, tid, id string, up ProgressUpdate) error {
	b, ok := f.items[id]
	if !ok || b.TenantID != tid {
		return errNotFound("备份")
	}
	if up.Status != nil {
		b.Status = *up.Status
	}
	if up.ArtifactRef != nil {
		b.ArtifactRef = *up.ArtifactRef
	}
	if up.Checksum != nil {
		b.Checksum = *up.Checksum
	}
	if up.SizeBytes != nil {
		b.SizeBytes = *up.SizeBytes
	}
	if up.Error != nil {
		b.Error = *up.Error
	}
	if up.VerifiedAt != nil {
		b.VerifiedAt = *up.VerifiedAt
	}
	if up.FinishedAt != nil {
		b.FinishedAt = *up.FinishedAt
	}
	f.items[id] = b
	return nil
}
func (f *fakeRepo) ListActive(_ context.Context) ([]Backup, error) {
	var out []Backup
	for _, b := range f.items {
		if !b.Terminal() {
			out = append(out, b)
		}
	}
	return out, nil
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
	if rec.Code != http.StatusCreated {
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
	if rec.Code != http.StatusCreated {
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

// TestBackupCreateDefaultTypeID 锁住 R8-B1：body 无 type 时默认值必须先补齐再生成 ID
// （原顺序生成尾部悬挂 '-' 的 ID 且与实际 type 不一致）。
func TestBackupCreateDefaultTypeID(t *testing.T) {
	env := fakeEnv{
		resEnv:  map[string]string{"ds-test": "env-test"},
		envType: map[string]string{"env-test": "test"},
	}
	h := NewHandler(newFakeRepo(), WithEnvResolver(env, env))
	h.Authorize = authWith("dataservice:read", "dataservice:write")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodPost, "/api/backups",
		strings.NewReader(`{"resourceId":"ds-test"}`)), "t-acme"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("无 type 创建应放行，实得 %d: %s", rec.Code, rec.Body.String())
	}
	var wrap struct {
		Data Backup `json:"data"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&wrap)
	if wrap.Data.Type != TypeFull {
		t.Fatalf("type 应默认 full，got %q", wrap.Data.Type)
	}
	// R8-B1 本意：默认值补齐后生成无缺陷 ID（无悬挂 '-'）。ID 已改时间基（2026-10-19：
	// 旧确定性 ID「bk-<rid>-<type>」致同资源第二次创建 409 只能删旧建新）。
	if !strings.HasPrefix(wrap.Data.ID, "bk-") || strings.HasSuffix(wrap.Data.ID, "-") {
		t.Fatalf("ID 应为 bk- 前缀且无悬挂连字符，got %q", wrap.Data.ID)
	}
	if wrap.Data.Mode != ModeRecord || wrap.Data.Status != StatusCompleted {
		t.Fatalf("缺省 mode 应为记录模式且直接 completed，got mode=%q status=%q", wrap.Data.Mode, wrap.Data.Status)
	}
	if wrap.Data.SizeMB != 0 {
		t.Fatalf("记录模式不应生成模拟大小（spec 专项05 阶段A），got %d", wrap.Data.SizeMB)
	}
}

// fakeTargetsForHandler / fakeRestorer / noopExecutor：snapshot/restore handler 用轻桩。
type fakeRestorer struct{ newID string }

func (f fakeRestorer) CreateRestored(_ context.Context, _, _, _, _ string) (string, error) {
	return f.newID, nil
}

// TestSnapshotWithoutExecutor503 无执行面（内存模式）snapshot 明确拒绝，不假受理。
func TestSnapshotWithoutExecutor503(t *testing.T) {
	h := NewHandler(newFakeRepo())
	h.Authorize = authWith("dataservice:read", "dataservice:write")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodPost, "/api/backups",
		strings.NewReader(`{"resourceId":"ds-test","mode":"snapshot"}`)), "t-acme"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("无执行面 snapshot 应 503，got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSnapshotAccepted 有执行面 snapshot 受理 accepted（推进归 Pacer）。
func TestSnapshotAccepted(t *testing.T) {
	repo := newFakeRepo()
	h := NewHandler(repo, WithExecutor(
		newFakeExecutor(ProbeResult{Phase: "pending"}),
		fakeTargetsForHandler{}, fakeRestorer{newID: "ds-new"}))
	h.Authorize = authWith("dataservice:read", "dataservice:write")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodPost, "/api/backups",
		strings.NewReader(`{"resourceId":"ds-1","mode":"snapshot"}`)), "t-acme"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("snapshot 应受理 201，got %d: %s", rec.Code, rec.Body.String())
	}
	var wrap struct {
		Data Backup `json:"data"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&wrap)
	if wrap.Data.Status != StatusAccepted || wrap.Data.Mode != ModeSnapshot || wrap.Data.Engine != "postgres" {
		t.Fatalf("受理应为 accepted/postgres，got %+v", wrap.Data)
	}
}

// TestRestoreRejectsRecordMode 记录模式无恢复证明，恢复 409（spec 阶段A：不冒充可恢复）。
func TestRestoreRejectsRecordMode(t *testing.T) {
	repo := newFakeRepo()
	repo.items["bk-rec"] = Backup{ID: "bk-rec", TenantID: "t-acme", ResourceID: "ds-1",
		Mode: ModeRecord, Status: StatusCompleted}
	h := NewHandler(repo, WithExecutor(
		newFakeExecutor(ProbeResult{}), fakeTargetsForHandler{}, fakeRestorer{newID: "ds-new"}))
	h.Authorize = authWith("dataservice:read", "dataservice:write")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, withTenant(httptest.NewRequest(http.MethodPost, "/api/backups/bk-rec/restore",
		strings.NewReader(`{}`)), "t-acme"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("记录模式恢复应 409，got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestRestoreFromSnapshot 已完成快照恢复：创建新实例 + restore 作业记录。
func TestRestoreFromSnapshot(t *testing.T) {
	repo := newFakeRepo()
	repo.items["bk-snap"] = Backup{ID: "bk-snap", TenantID: "t-acme", ResourceID: "ds-1",
		Mode: ModeSnapshot, Status: StatusCompleted, ArtifactRef: "file://paas-backups/bk-snap.dump",
		Engine: "postgres", Checksum: strings.Repeat("a", 64)}
	h := NewHandler(repo, WithExecutor(
		newFakeExecutor(ProbeResult{}), fakeTargetsForHandler{}, fakeRestorer{newID: "ds-new"}))
	h.Authorize = authWith("dataservice:read", "dataservice:write")
	rec := httptest.NewRecorder()
	req := withTenant(httptest.NewRequest(http.MethodPost, "/api/backups/bk-snap/restore", strings.NewReader(`{}`)), "t-acme")
	req.Header.Set("Idempotency-Key", "test-restore-request")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("快照恢复应 201，got %d: %s", rec.Code, rec.Body.String())
	}
	var wrap struct {
		Data Backup `json:"data"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&wrap)
	if wrap.Data.Mode != ModeRestore || wrap.Data.SourceBackupID != "bk-snap" ||
		wrap.Data.RestoredResourceID != RestoreTargetID(wrap.Data.ID) || wrap.Data.Status != StatusAccepted {
		t.Fatalf("恢复记录字段不符： %+v", wrap.Data)
	}
}

type fakeTargetsForHandler struct{}

func (fakeTargetsForHandler) ResolveTarget(_ context.Context, _, _ string) (Target, error) {
	return Target{Namespace: "ns", ServiceName: "ds-1", SecretName: "ds-1-secret", Engine: "postgres"}, nil
}
