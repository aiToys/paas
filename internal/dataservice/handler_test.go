package dataservice_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/dataservice"
	dsmemory "github.com/aitoys/paas/internal/dataservice/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// stubResolver 按预设 envID -> 类型 映射。
type stubResolver struct {
	types map[string]string
}

func (s stubResolver) EnvType(_ context.Context, envID string) (string, error) {
	if t, ok := s.types[envID]; ok {
		return t, nil
	}
	return "", nil
}

func newReq(method, target, body, tid string) *http.Request {
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	ctx := tenant.WithTenant(r.Context(), tid)
	return r.WithContext(ctx)
}

func allowAll(*http.Request, string) bool { return true }

// createDS 经 POST /api/dataservices 自建数据服务（去 mock seed 后测试自建真源），
// 返回创建结果（含 store 生成的 ID 与掩码后的 Connection）。
func createDS(t *testing.T, h *dataservice.Handler, tid, kind, name, env, specJSON string) dataservice.DataService {
	t.Helper()
	body := fmt.Sprintf(`{"kind":%q,"name":%q,"spec":%s,"envId":%q}`, kind, name, specJSON, env)
	r := newReq(http.MethodPost, "/api/dataservices", body, tid)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("创建 %s 应 201，got %d body=%s", name, w.Code, w.Body.String())
	}
	var d dataservice.DataService
	decodeData(t, w, &d)
	return d
}

func decode(t *testing.T, w *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("解码失败: %v (body=%s)", err, w.Body.String())
	}
}

// decodeData 解包 {data:T} 信封后反序列化到 v（单资源响应，handler 统一 WriteData 契约）。
func decodeData(t *testing.T, w *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("解码信封失败: %v (body=%s)", err, w.Body.String())
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatalf("解码 data 失败: %v (data=%s)", err, string(env.Data))
	}
}

// TestMeta 验证 KindMeta 端点返回 6 个 kind。
func TestMeta(t *testing.T) {
	h := dataservice.NewHandler(dsmemory.NewStore())
	r := newReq(http.MethodGet, "/api/dataservices/meta", "", "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("meta 应 200，got %d", w.Code)
	}
	var resp struct {
		Data []dataservice.KindMeta `json:"data"`
	}
	decode(t, w, &resp)
	if len(resp.Data) != 6 {
		t.Fatalf("应 6 个 kind，got %d", len(resp.Data))
	}
}

// TestListByKind 验证列表 + kind 过滤。
func TestListByKind(t *testing.T) {
	h := dataservice.NewHandler(dsmemory.NewStore())
	h.Authorize = allowAll
	createDS(t, h, "t-acme", dataservice.KindDB, "acme-orders-db", "env-acme-test",
		`{"engine":"postgres","version":"15","size_gb":"100"}`)
	r := newReq(http.MethodGet, "/api/dataservices?kind=db", "", "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var resp struct {
		Data []dataservice.DataService `json:"data"`
	}
	decode(t, w, &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("acme db 应 1 个，got %d", len(resp.Data))
	}
}

// TestCreateAndDelete 验证创建/删除闭环 + 默认 running。
func TestCreateAndDelete(t *testing.T) {
	h := dataservice.NewHandler(dsmemory.NewStore(), dataservice.WithEnvResolver(stubResolver{
		types: map[string]string{"env-acme-test": "test"},
	}))
	h.Authorize = allowAll
	body := `{"kind":"db","name":"new-db","spec":{"engine":"postgres","version":"15","size_gb":"30"},"envId":"env-acme-test"}`
	r := newReq(http.MethodPost, "/api/dataservices", body, "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("创建应 201，got %d body=%s", w.Code, w.Body.String())
	}
	var d dataservice.DataService
	decodeData(t, w, &d)
	if d.Status != dataservice.StatusRunning {
		t.Fatalf("应默认 running，got %s", d.Status)
	}

	// 删除
	r2 := newReq(http.MethodDelete, "/api/dataservices/"+d.ID, "", "t-acme")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusOK {
		t.Fatalf("删除应 200，got %d", w2.Code)
	}
}

// TestProdCreateBlocked 验证 developer 在生产环境创建被 prod:write 拦截。
func TestProdCreateBlocked(t *testing.T) {
	h := dataservice.NewHandler(dsmemory.NewStore(), dataservice.WithEnvResolver(stubResolver{
		types: map[string]string{"env-acme-prod-bj": "prod"},
	}))
	// 模拟 developer：有 write 但无 prod:write
	h.Authorize = func(_ *http.Request, perm string) bool { return perm != "prod:write" }
	body := `{"kind":"db","name":"prod-db","spec":{"engine":"postgres","version":"15","size_gb":"30"},"envId":"env-acme-prod-bj"}`
	r := newReq(http.MethodPost, "/api/dataservices", body, "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("生产创建应 403，got %d", w.Code)
	}
}

// TestProdDeleteBlocked 验证跨租户删除生产资源被拒（先 not found 或 prod 拦截）。
func TestProdDeleteAsAdmin(t *testing.T) {
	h := dataservice.NewHandler(dsmemory.NewStore(), dataservice.WithEnvResolver(stubResolver{
		types: map[string]string{"env-acme-prod-bj": "prod"},
	}))
	h.Authorize = allowAll // admin 有 prod:write
	// 自建生产环境 mq（env-acme-prod-bj）
	mq := createDS(t, h, "t-acme", dataservice.KindMQ, "acme-events-mq", "env-acme-prod-bj",
		`{"engine":"nats","partitions":"6"}`)
	r := newReq(http.MethodDelete, "/api/dataservices/"+mq.ID, "", "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("admin 删除生产应 200，got %d", w.Code)
	}
}

// TestTenantIsolation 验证跨租户不可见。
func TestTenantIsolation(t *testing.T) {
	h := dataservice.NewHandler(dsmemory.NewStore())
	h.Authorize = allowAll
	r := newReq(http.MethodGet, "/api/dataservices", "", "t-globex")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var resp struct {
		Data []dataservice.DataService `json:"data"`
	}
	decode(t, w, &resp)
	for _, d := range resp.Data {
		if d.TenantID == "t-acme" {
			t.Fatal("globex 不应见到 acme 资源")
		}
	}
}

// TestListMasksSecret 验证 list 返回的 password/secretKey 掩码，不泄漏明文。
func TestListMasksSecret(t *testing.T) {
	h := dataservice.NewHandler(dsmemory.NewStore())
	h.Authorize = allowAll
	createDS(t, h, "t-acme", dataservice.KindDB, "acme-orders-db", "env-acme-test",
		`{"engine":"postgres","version":"15","size_gb":"100"}`)
	r := newReq(http.MethodGet, "/api/dataservices?kind=db", "", "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var resp struct {
		Data []dataservice.DataService `json:"data"`
	}
	decode(t, w, &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("acme db 应 1 个，got %d", len(resp.Data))
	}
	conn := resp.Data[0].Connection
	if conn["password"] != dataservice.SecretMask {
		t.Fatalf("list password 应掩码，got %q", conn["password"])
	}
	if conn["uri"] != dataservice.SecretMask {
		t.Fatalf("list uri 应掩码（含明文密码），got %q", conn["uri"])
	}
	if conn["host"] == "" || conn["host"] == dataservice.SecretMask {
		t.Fatalf("host 不应为空或掩码: %q", conn["host"])
	}
}

// TestDetailMasksSecret 验证 Create/Detail 均返回掩码凭证（明文仅内部绑定注入用，
// 防日志/proxy/MITM 捕获；与 security 模块 Create 也返 Masked 策略一致）。
func TestDetailMasksSecret(t *testing.T) {
	h := dataservice.NewHandler(dsmemory.NewStore(), dataservice.WithEnvResolver(stubResolver{
		types: map[string]string{"env-acme-test": "test"},
	}))
	h.Authorize = allowAll
	body := `{"kind":"cache","name":"detail-redis","spec":{"engine":"redis"},"envId":"env-acme-test"}`
	r := newReq(http.MethodPost, "/api/dataservices", body, "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var created dataservice.DataService
	decodeData(t, w, &created)
	// POST 返掩码（write 权限者也只见掩码，明文仅内部绑定注入用）
	if created.Connection["password"] != dataservice.SecretMask {
		t.Fatalf("创建返回 password 应掩码，got %q", created.Connection["password"])
	}

	// GET 详情应掩码（read 权限者含 viewer 不可见明文）
	r2 := newReq(http.MethodGet, "/api/dataservices/"+created.ID, "", "t-acme")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	var detail dataservice.DataService
	decodeData(t, w2, &detail)
	if detail.Connection["password"] != dataservice.SecretMask {
		t.Fatalf("详情 password 应掩码，got %q", detail.Connection["password"])
	}
	if detail.Connection["uri"] != dataservice.SecretMask {
		t.Fatalf("详情 uri 应掩码（含明文密码），got %q", detail.Connection["uri"])
	}
	if detail.Connection["host"] == "" || detail.Connection["host"] == dataservice.SecretMask {
		t.Fatalf("host 不应为空或掩码: %q", detail.Connection["host"])
	}
}

// fakePodReader 是可控的 PodReader（注入 PodInfo 供 servePods 测试）。
type fakePodReader struct {
	pods []dataservice.PodInfo
	err  error
}

func (f fakePodReader) Pods(ctx context.Context, ns, id string) ([]dataservice.PodInfo, error) {
	return f.pods, f.err
}

// TestServePodsReturnsInstances 验证 pods 端点返 reader 的 Pod 列表。
func TestServePodsReturnsInstances(t *testing.T) {
	repo := dsmemory.NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	d, _ := repo.Create(ctx, dataservice.DataService{Kind: "db", Name: "pg1", Spec: map[string]string{"engine": "postgres"}, EnvID: "env1"})
	h := dataservice.NewHandler(repo, dataservice.WithPodReader(fakePodReader{pods: []dataservice.PodInfo{{Name: "pg1-0", Status: "Running", Ready: "1/1"}}}))
	r := newReq(http.MethodGet, "/api/dataservices/"+d.ID+"/pods", "", "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var pods []dataservice.PodInfo
	decodeData(t, w, &pods)
	if len(pods) != 1 || pods[0].Name != "pg1-0" || pods[0].Ready != "1/1" {
		t.Fatalf("期望 pg1-0 ready=1/1，实际 %+v", pods)
	}
}

// TestServePodsNilReaderReturnsEmpty 验证集群外（nil reader）降级返空切片 200。
func TestServePodsNilReaderReturnsEmpty(t *testing.T) {
	repo := dsmemory.NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	d, _ := repo.Create(ctx, dataservice.DataService{Kind: "db", Name: "pg1", Spec: map[string]string{"engine": "postgres"}, EnvID: "env1"})
	h := dataservice.NewHandler(repo) // 无 PodReader
	r := newReq(http.MethodGet, "/api/dataservices/"+d.ID+"/pods", "", "t-acme")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("nil reader 应降级 200，code=%d", w.Code)
	}
	var pods []dataservice.PodInfo
	decodeData(t, w, &pods)
	if len(pods) != 0 {
		t.Fatalf("nil reader 应返空切片，got %d", len(pods))
	}
}

// TestServePodsCrossTenantNotFound 验证越权校验：跨租户访问他人数据服务 pods 统一 NotFound 不泄漏。
func TestServePodsCrossTenantNotFound(t *testing.T) {
	repo := dsmemory.NewStore()
	acme := tenant.WithTenant(context.Background(), "t-acme")
	d, _ := repo.Create(acme, dataservice.DataService{Kind: "db", Name: "pg1", Spec: map[string]string{"engine": "postgres"}, EnvID: "env1"})
	h := dataservice.NewHandler(repo, dataservice.WithPodReader(fakePodReader{pods: []dataservice.PodInfo{{Name: "pg1-0"}}}))
	// globex 访问 acme 的数据服务。
	r := newReq(http.MethodGet, "/api/dataservices/"+d.ID+"/pods", "", "t-globex")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("跨租户应 404 不泄漏，got %d", w.Code)
	}
}
