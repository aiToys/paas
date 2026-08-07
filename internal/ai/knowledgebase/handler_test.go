package knowledgebase_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/ai/knowledgebase"
	"github.com/aitoys/paas/internal/ai/knowledgebase/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// ---- mockBlob ----

type mockBlob struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func newMockBlob() *mockBlob { return &mockBlob{objs: map[string][]byte{}} }

func (m *mockBlob) EnsureBucket(_ context.Context, _ string) error { return nil }
func (m *mockBlob) PutObject(_ context.Context, _, key string, r io.Reader, _ string) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = b
	return nil
}
func (m *mockBlob) GetObject(_ context.Context, _, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (m *mockBlob) DeleteObject(_ context.Context, _, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, key)
	return nil
}

// ---- helpers ----

func allowAll(*http.Request, string) bool { return true }

func newTestHandler(t *testing.T) (*knowledgebase.Handler, *memory.Store, *mockVS, *mockBlob) {
	t.Helper()
	repo := memory.NewStore()
	vs := newMockVS()
	blob := newMockBlob()
	r := knowledgebase.NewRetriever(repo, vs, mockEmbedderFactory{})
	h := knowledgebase.NewHandler(repo, r, blob)
	h.Authorize = allowAll
	h.WithBaseCtx(context.Background())
	return h, repo, vs, blob
}

func mustCreateKBViaHandler(h *knowledgebase.Handler, ctx context.Context, name string) knowledgebase.KnowledgeBase {
	body, _ := json.Marshal(knowledgebase.KnowledgeBase{
		Name: name, VectorStoreRef: "ds-v", ObjectStoreRef: "ds-s",
		EmbeddingModel: "text-embedding-v4", EmbeddingDim: 3,
	})
	req := httptest.NewRequest("POST", "/api/knowledgebases", bytes.NewReader(body))
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp struct {
		Data knowledgebase.KnowledgeBase `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	return resp.Data
}

// ---- tests ----

func TestHandlerKBCreateList(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	mustCreateKBViaHandler(h, ctx, "kb1")

	req := httptest.NewRequest("GET", "/api/knowledgebases", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var resp struct {
		Data []knowledgebase.KnowledgeBase `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].Name != "kb1" {
		t.Errorf("列表应 1 个 kb1，got %+v", resp.Data)
	}
}

func TestHandlerKBGetDelete(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	kb := mustCreateKBViaHandler(h, ctx, "kb")
	// repo 加 doc 测试级联（验证删 KB 触发）
	_, _ = repo.CreateDocument(ctx, knowledgebase.Document{KBID: kb.ID, Name: "d"})

	// GET 详情
	req := httptest.NewRequest("GET", "/api/knowledgebases/"+kb.ID, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET 应 200，got %d", rec.Code)
	}

	// DELETE
	req = httptest.NewRequest("DELETE", "/api/knowledgebases/"+kb.ID, nil).WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("DELETE 应 200，got %d", rec.Code)
	}
	// 确认删了
	if _, err := repo.Get(ctx, kb.ID); err == nil {
		t.Error("KB 应已删")
	}
}

func TestHandlerDocumentUploadAndRetrieve(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	kb := mustCreateKBViaHandler(h, ctx, "kb")

	// multipart 上传
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	fw, err := mw.CreateFormFile("file", "test.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte("测试内容" + repeatStr("数据", 300)))
	mw.Close()

	req := httptest.NewRequest("POST", "/api/knowledgebases/"+kb.ID+"/documents", body).WithContext(ctx)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("上传应 201，got %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data knowledgebase.Document `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	docID := resp.Data.ID
	if docID == "" {
		t.Fatal("应返回 doc ID")
	}

	// 轮询文档状态（异步处理）
	var doc knowledgebase.Document
	for i := 0; i < 100; i++ {
		doc, _ = repo.GetDocument(ctx, docID)
		if doc.Status == knowledgebase.DocStatusIndexed || doc.Status == knowledgebase.DocStatusFailed {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if doc.Status != knowledgebase.DocStatusIndexed {
		t.Fatalf("文档应 indexed，got %s（msg=%s）", doc.Status, doc.Message)
	}

	// retrieve
	retBody, _ := json.Marshal(map[string]string{"query": "测试"})
	req = httptest.NewRequest("POST", "/api/knowledgebases/"+kb.ID+"/retrieve", bytes.NewReader(retBody)).WithContext(ctx)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("retrieve 应 200，got %d: %s", rec.Code, rec.Body.String())
	}
	var ret struct {
		Data []knowledgebase.ChunkHit `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ret)
	if len(ret.Data) == 0 {
		t.Error("应召回 chunks")
	}
}

func TestHandlerDocumentDelete(t *testing.T) {
	h, repo, _, _ := newTestHandler(t)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	kb := mustCreateKBViaHandler(h, ctx, "kb")
	doc, _ := repo.CreateDocument(ctx, knowledgebase.Document{KBID: kb.ID, Name: "d.txt", MIME: "text/plain"})
	_ = repo.UpsertChunks(ctx, []knowledgebase.Chunk{{ID: "c1", KBID: kb.ID, DocID: doc.ID, Seq: 0, Content: "x"}})

	req := httptest.NewRequest("DELETE", fmt.Sprintf("/api/knowledgebases/%s/documents/%s", kb.ID, doc.ID), nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE 文档应 200，got %d", rec.Code)
	}
	if _, err := repo.GetDocument(ctx, doc.ID); err == nil {
		t.Error("文档应已删")
	}
	chs, _ := repo.ListChunks(ctx, kb.ID, doc.ID)
	if len(chs) != 0 {
		t.Errorf("chunks 应已清，got %d", len(chs))
	}
}

func TestHandlerKBForbidden(t *testing.T) {
	h, _, _, _ := newTestHandler(t)
	h.Authorize = func(*http.Request, string) bool { return false }
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	req := httptest.NewRequest("GET", "/api/knowledgebases", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("无权限应 403，got %d", rec.Code)
	}
}

func TestHandlerRetrieveNoBackend(t *testing.T) {
	repo := memory.NewStore()
	h := knowledgebase.NewHandler(repo, nil, nil)
	h.Authorize = allowAll
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	body, _ := json.Marshal(map[string]string{"query": "x"})
	req := httptest.NewRequest("POST", "/api/knowledgebases/kb1/retrieve", bytes.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("无 retriever 应 503，got %d", rec.Code)
	}
}
