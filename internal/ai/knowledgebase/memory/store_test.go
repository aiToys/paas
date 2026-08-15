package memory

import (
	"context"
	"errors"
	"testing"

	"github.com/aitoys/paas/internal/ai/knowledgebase"
	"github.com/aitoys/paas/pkg/tenant"
)

func mustKB(name string) knowledgebase.KnowledgeBase {
	return knowledgebase.KnowledgeBase{
		Name: name, VectorStoreRef: "ds-vec-1", ObjectStoreRef: "ds-store-1",
		EmbeddingModel: "text-embedding-v4", EmbeddingDim: 1024,
	}
}

func TestKBCreateAndGet(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	kb, err := s.Create(ctx, mustKB("kb1"))
	if err != nil {
		t.Fatalf("Create 失败: %v", err)
	}
	if kb.ID == "" {
		t.Fatal("ID 应自动生成")
	}
	got, err := s.Get(ctx, kb.ID)
	if err != nil {
		t.Fatalf("Get 失败: %v", err)
	}
	if got.Name != "kb1" {
		t.Errorf("Name 应 kb1，got %q", got.Name)
	}
}

func TestKBNameUnique(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	if _, err := s.Create(ctx, mustKB("dup")); err != nil {
		t.Fatalf("首次 Create: %v", err)
	}
	_, err := s.Create(ctx, mustKB("dup"))
	if !errors.Is(err, knowledgebase.ErrKBExists) {
		t.Errorf("同名应返 ErrKBExists，got %v", err)
	}
}

func TestKBCrossTenant(t *testing.T) {
	s := NewStore()
	ctxA := tenant.WithTenant(context.Background(), "t-acme")
	ctxB := tenant.WithTenant(context.Background(), "t-globex")
	kb, _ := s.Create(ctxA, mustKB("private"))
	if _, err := s.Get(ctxB, kb.ID); !errors.Is(err, knowledgebase.ErrKBNotFound) {
		t.Errorf("跨租户应 ErrKBNotFound，got %v", err)
	}
	// List 不泄漏
	ks, _ := s.List(ctxB)
	if len(ks) != 0 {
		t.Errorf("跨租户 List 应空，got %d", len(ks))
	}
}

func TestKBDeleteCascade(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	kb, _ := s.Create(ctx, mustKB("kb"))
	doc, _ := s.CreateDocument(ctx, knowledgebase.Document{KBID: kb.ID, Name: "d.txt", MIME: "text/plain"})
	_ = s.UpsertChunks(ctx, []knowledgebase.Chunk{{KBID: kb.ID, DocID: doc.ID, Seq: 0, Content: "hi"}})
	if err := s.Delete(ctx, kb.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.GetDocument(ctx, doc.ID); !errors.Is(err, knowledgebase.ErrDocNotFound) {
		t.Errorf("删 KB 应级联清文档，got %v", err)
	}
	chs, _ := s.ListChunks(ctx, kb.ID, "")
	if len(chs) != 0 {
		t.Errorf("删 KB 应级联清 chunks，got %d", len(chs))
	}
}

func TestDocumentCreateValidatesKBOwnership(t *testing.T) {
	s := NewStore()
	ctxA := tenant.WithTenant(context.Background(), "t-acme")
	ctxB := tenant.WithTenant(context.Background(), "t-globex")
	kb, _ := s.Create(ctxA, mustKB("kb"))
	// 跨租户给他人 KB 建文档
	_, err := s.CreateDocument(ctxB, knowledgebase.Document{KBID: kb.ID, Name: "x"})
	if !errors.Is(err, knowledgebase.ErrKBNotFound) {
		t.Errorf("跨租户建文档应 ErrKBNotFound，got %v", err)
	}
}

func TestDocumentDeleteCascadeChunks(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	kb, _ := s.Create(ctx, mustKB("kb"))
	doc, _ := s.CreateDocument(ctx, knowledgebase.Document{KBID: kb.ID, Name: "d", MIME: "text/plain"})
	_ = s.UpsertChunks(ctx, []knowledgebase.Chunk{{KBID: kb.ID, DocID: doc.ID, Seq: 0, Content: "a"}, {KBID: kb.ID, DocID: doc.ID, Seq: 1, Content: "b"}})
	if err := s.DeleteDocument(ctx, doc.ID); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	chs, _ := s.ListChunks(ctx, kb.ID, doc.ID)
	if len(chs) != 0 {
		t.Errorf("删文档应级联清 chunks，got %d", len(chs))
	}
}

func TestChunkUpsertListByDoc(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	kb, _ := s.Create(ctx, mustKB("kb"))
	doc, _ := s.CreateDocument(ctx, knowledgebase.Document{KBID: kb.ID, Name: "d", MIME: "text/plain"})
	chunks := []knowledgebase.Chunk{
		{KBID: kb.ID, DocID: doc.ID, Seq: 1, Content: "second"},
		{KBID: kb.ID, DocID: doc.ID, Seq: 0, Content: "first"},
	}
	if err := s.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}
	got, _ := s.ListChunks(ctx, kb.ID, doc.ID)
	if len(got) != 2 || got[0].Seq != 0 || got[1].Seq != 1 {
		t.Errorf("List 按 Seq 升序，got %v", got)
	}
	// docID 空返 KB 全部
	all, _ := s.ListChunks(ctx, kb.ID, "")
	if len(all) != 2 {
		t.Errorf("docID 空应返 KB 全部 chunks，got %d", len(all))
	}
}

func TestUpdateDocumentStatus(t *testing.T) {
	s := NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	kb, _ := s.Create(ctx, mustKB("kb"))
	doc, _ := s.CreateDocument(ctx, knowledgebase.Document{KBID: kb.ID, Name: "d", MIME: "text/plain"})
	if err := s.UpdateDocumentStatus(ctx, doc.ID, knowledgebase.DocStatusIndexed, 5, ""); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, _ := s.GetDocument(ctx, doc.ID)
	if got.Status != knowledgebase.DocStatusIndexed || got.ChunkCount != 5 {
		t.Errorf("状态/数量未更新，got %+v", got)
	}
}

func TestKBsCount(t *testing.T) {
	s := NewStore()
	ctxA := tenant.WithTenant(context.Background(), "t-acme")
	ctxB := tenant.WithTenant(context.Background(), "t-globex")
	_, _ = s.Create(ctxA, mustKB("a1"))
	_, _ = s.Create(ctxA, mustKB("a2"))
	_, _ = s.Create(ctxB, mustKB("b1"))
	n, _ := s.KBsCount(ctxA)
	if n != 3 { // 全表（不经租户过滤，seed 判空用）
		t.Errorf("全表应 3，got %d", n)
	}
}
