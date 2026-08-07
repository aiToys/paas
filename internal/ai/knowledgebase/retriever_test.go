package knowledgebase_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/aitoys/paas/internal/ai/knowledgebase"
	"github.com/aitoys/paas/internal/ai/knowledgebase/memory"
	"github.com/aitoys/paas/pkg/provider"
	"github.com/aitoys/paas/pkg/tenant"
)

// ---- mocks ----

type mockVS struct {
	mu     sync.Mutex
	colls  map[string]int
	points map[string][]knowledgebase.VectorPoint
}

func newMockVS() *mockVS {
	return &mockVS{colls: map[string]int{}, points: map[string][]knowledgebase.VectorPoint{}}
}

func (m *mockVS) EnsureCollection(_ context.Context, c string, dim int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.colls[c] = dim
	return nil
}

func (m *mockVS) UpsertVectors(_ context.Context, c string, pts []knowledgebase.VectorPoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range pts {
		found := false
		for i, ex := range m.points[c] {
			if ex.ID == p.ID {
				m.points[c][i] = p
				found = true
				break
			}
		}
		if !found {
			m.points[c] = append(m.points[c], p)
		}
	}
	return nil
}

func (m *mockVS) Search(_ context.Context, c string, _ []float32, topK int) ([]knowledgebase.VectorHit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pts := m.points[c]
	out := make([]knowledgebase.VectorHit, 0, len(pts))
	for _, p := range pts {
		out = append(out, knowledgebase.VectorHit{ID: p.ID, Score: 1.0})
	}
	if len(out) > topK {
		out = out[:topK]
	}
	return out, nil
}

func (m *mockVS) DeletePoints(_ context.Context, c string, ids []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	filtered := m.points[c][:0]
	for _, p := range m.points[c] {
		drop := false
		for _, id := range ids {
			if p.ID == id {
				drop = true
				break
			}
		}
		if !drop {
			filtered = append(filtered, p)
		}
	}
	m.points[c] = filtered
	return nil
}

func (m *mockVS) DeleteCollection(_ context.Context, c string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.points, c)
	delete(m.colls, c)
	return nil
}

type mockEmbedder struct{}

func (mockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}

type mockEmbedderFactory struct{ fail bool }

func (m mockEmbedderFactory) EmbedderFor(_ context.Context, _ string) (provider.Embedder, error) {
	if m.fail {
		return nil, fmt.Errorf("embed error")
	}
	return mockEmbedder{}, nil
}

// ---- helpers ----

func newTestRetriever(t *testing.T, embedFail bool) (*knowledgebase.Retriever, *memory.Store, *mockVS) {
	t.Helper()
	repo := memory.NewStore()
	vs := newMockVS()
	r := knowledgebase.NewRetriever(repo, vs, mockEmbedderFactory{fail: embedFail})
	return r, repo, vs
}

func mustCreateKB(ctx context.Context, repo *memory.Store, name string) (knowledgebase.KnowledgeBase, error) {
	return repo.Create(ctx, knowledgebase.KnowledgeBase{
		Name: name, VectorStoreRef: "ds-v", ObjectStoreRef: "ds-s",
		EmbeddingModel: "text-embedding-v4", EmbeddingDim: 3,
	})
}

func repeatStr(s string, n int) string {
	out := ""
	for i := 0; i < n; i++ {
		out += s
	}
	return out
}

// ---- tests ----

func TestIndexDocumentOK(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	r, repo, vs := newTestRetriever(t, false)
	kb, _ := mustCreateKB(ctx, repo, "kb")
	doc, _ := repo.CreateDocument(ctx, knowledgebase.Document{KBID: kb.ID, Name: "d.txt", MIME: "text/plain"})

	content := "测试文本" + repeatStr("内容", 300)
	if err := r.IndexDocument(ctx, kb, doc, content); err != nil {
		t.Fatalf("IndexDocument: %v", err)
	}
	got, _ := repo.GetDocument(ctx, doc.ID)
	if got.Status != knowledgebase.DocStatusIndexed {
		t.Errorf("应 indexed，got %s（msg=%s）", got.Status, got.Message)
	}
	if got.ChunkCount == 0 {
		t.Error("ChunkCount 应 > 0")
	}
	chs, _ := repo.ListChunks(ctx, kb.ID, "")
	if len(chs) != got.ChunkCount {
		t.Errorf("chunks 数 %d != ChunkCount %d", len(chs), got.ChunkCount)
	}
	if len(vs.points[kb.CollectionName()]) != got.ChunkCount {
		t.Errorf("向量数不匹配")
	}
}

func TestIndexDocumentEmpty(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	r, repo, _ := newTestRetriever(t, false)
	kb, _ := mustCreateKB(ctx, repo, "kb")
	doc, _ := repo.CreateDocument(ctx, knowledgebase.Document{KBID: kb.ID, Name: "empty", MIME: "text/plain"})
	if err := r.IndexDocument(ctx, kb, doc, ""); err != nil {
		t.Fatalf("IndexDocument 空: %v", err)
	}
	got, _ := repo.GetDocument(ctx, doc.ID)
	if got.Status != knowledgebase.DocStatusIndexed || got.ChunkCount != 0 {
		t.Errorf("空文档应 indexed/0，got %s/%d", got.Status, got.ChunkCount)
	}
}

func TestIndexDocumentEmbedFail(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	r, repo, _ := newTestRetriever(t, true)
	kb, _ := mustCreateKB(ctx, repo, "kb")
	doc, _ := repo.CreateDocument(ctx, knowledgebase.Document{KBID: kb.ID, Name: "d", MIME: "text/plain"})
	_ = r.IndexDocument(ctx, kb, doc, "some content here")
	got, _ := repo.GetDocument(ctx, doc.ID)
	if got.Status != knowledgebase.DocStatusFailed {
		t.Errorf("embed 失败应 failed，got %s", got.Status)
	}
	if got.Message == "" {
		t.Error("failed 应有 Message")
	}
}

func TestRetrieveOK(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	r, repo, _ := newTestRetriever(t, false)
	kb, _ := mustCreateKB(ctx, repo, "kb")
	doc, _ := repo.CreateDocument(ctx, knowledgebase.Document{KBID: kb.ID, Name: "d", MIME: "text/plain"})
	_ = r.IndexDocument(ctx, kb, doc, "知识库内容"+repeatStr("数据", 300))

	hits, err := r.Retrieve(ctx, kb.ID, "查询")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("应召回 chunks")
	}
	if hits[0].Chunk.Content == "" {
		t.Error("chunk content 不应为空")
	}
}

func TestRetrieveNoHits(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	r, repo, _ := newTestRetriever(t, false)
	kb, _ := mustCreateKB(ctx, repo, "kb")
	hits, err := r.Retrieve(ctx, kb.ID, "查询")
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if hits != nil {
		t.Errorf("空向量库应 nil，got %d hits", len(hits))
	}
}

func TestDeleteDocumentVectors(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	r, repo, vs := newTestRetriever(t, false)
	kb, _ := mustCreateKB(ctx, repo, "kb")
	doc, _ := repo.CreateDocument(ctx, knowledgebase.Document{KBID: kb.ID, Name: "d", MIME: "text/plain"})
	_ = r.IndexDocument(ctx, kb, doc, "内容"+repeatStr("x", 600))
	if len(vs.points[kb.CollectionName()]) == 0 {
		t.Fatal("应已入向量")
	}
	if err := r.DeleteDocumentVectors(ctx, kb, doc.ID); err != nil {
		t.Fatalf("DeleteDocumentVectors: %v", err)
	}
	if len(vs.points[kb.CollectionName()]) != 0 {
		t.Errorf("删文档向量后应为空，got %d", len(vs.points[kb.CollectionName()]))
	}
}
