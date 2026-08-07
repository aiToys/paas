//go:build integration

// 集成测试：需真实 PostgreSQL，由环境变量 PAAS_TEST_PG_URL 指定 DSN。
// 默认 `go test ./...` 不编译本文件（构建标签门控）；用 `make test-pg` 运行。
// 每测重建 schema（DROP KB 表 + schema_migrations -> RunMigrations up），保证隔离。

package pg

import (
	"context"
	"os"
	"testing"

	"github.com/aitoys/paas/internal/ai/knowledgebase"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/tenant"
)

func newTestDB(t *testing.T) *storagepg.DB {
	t.Helper()
	dsn := os.Getenv("PAAS_TEST_PG_URL")
	if dsn == "" {
		t.Skip("PAAS_TEST_PG_URL 未设置，跳过 PG 集成测试")
	}
	ctx := context.Background()
	db, err := storagepg.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("打开测试 DB 失败: %v", err)
	}
	t.Cleanup(db.Close)
	if err := storagepg.RunMigrations(ctx, db); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { resetSchema(t, db) })
	return db
}

func resetSchema(t *testing.T, db *storagepg.DB) {
	t.Helper()
	// 清空 public schema 全部表 + schema_migrations（含其他模块表），下个测试全新迁移。
	// 用 DROP SCHEMA CASCADE 比 DROP 各表更简洁，避免遗漏表导致下测「表已存在」dirty。
	_, err := db.Pool().Exec(context.Background(),
		`DROP SCHEMA IF EXISTS public CASCADE; CREATE SCHEMA public;`)
	if err != nil {
		t.Fatalf("重置 schema 失败: %v", err)
	}
}

func mustKB(name string) knowledgebase.KnowledgeBase {
	return knowledgebase.KnowledgeBase{
		Name: name, VectorStoreRef: "ds-v", ObjectStoreRef: "ds-s",
		EmbeddingModel: "text-embedding-v4", EmbeddingDim: 1024,
	}
}

func TestPGKBCreateAndGet(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	kb, err := s.Create(ctx, mustKB("kb1"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if kb.ID == "" {
		t.Fatal("ID 应生成")
	}
	got, err := s.Get(ctx, kb.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "kb1" || got.EmbeddingDim != 1024 {
		t.Errorf("字段不符: %+v", got)
	}
}

func TestPGKBNameUnique(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	if _, err := s.Create(ctx, mustKB("dup")); err != nil {
		t.Fatalf("首次: %v", err)
	}
	_, err := s.Create(ctx, mustKB("dup"))
	if err != knowledgebase.ErrKBExists {
		t.Errorf("应 ErrKBExists，got %v", err)
	}
}

func TestPGKBCrossTenant(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctxA := tenant.WithTenant(context.Background(), "t-acme")
	ctxB := tenant.WithTenant(context.Background(), "t-globex")
	kb, _ := s.Create(ctxA, mustKB("private"))
	if _, err := s.Get(ctxB, kb.ID); err != knowledgebase.ErrKBNotFound {
		t.Errorf("跨租户应 ErrKBNotFound，got %v", err)
	}
}

func TestPGDocumentCRUD(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	kb, _ := s.Create(ctx, mustKB("kb"))

	doc, err := s.CreateDocument(ctx, knowledgebase.Document{KBID: kb.ID, Name: "d.txt", MIME: "text/plain"})
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}
	if doc.Status != knowledgebase.DocStatusParsing {
		t.Errorf("默认状态应 parsing，got %s", doc.Status)
	}
	if err := s.UpdateDocumentStatus(ctx, doc.ID, knowledgebase.DocStatusIndexed, 3, ""); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	got, _ := s.GetDocument(ctx, doc.ID)
	if got.Status != knowledgebase.DocStatusIndexed || got.ChunkCount != 3 {
		t.Errorf("状态/数量未更新: %+v", got)
	}
	docs, _ := s.ListDocuments(ctx, kb.ID)
	if len(docs) != 1 {
		t.Errorf("应 1 文档，got %d", len(docs))
	}
}

func TestPGChunkUpsertAndList(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	kb, _ := s.Create(ctx, mustKB("kb"))
	doc, _ := s.CreateDocument(ctx, knowledgebase.Document{KBID: kb.ID, Name: "d", MIME: "text/plain"})
	chunks := []knowledgebase.Chunk{
		{ID: "c1", KBID: kb.ID, DocID: doc.ID, Seq: 1, Content: "second"},
		{ID: "c2", KBID: kb.ID, DocID: doc.ID, Seq: 0, Content: "first"},
	}
	if err := s.UpsertChunks(ctx, chunks); err != nil {
		t.Fatalf("UpsertChunks: %v", err)
	}
	got, _ := s.ListChunks(ctx, kb.ID, doc.ID)
	if len(got) != 2 || got[0].Seq != 0 {
		t.Errorf("应 2 片按 Seq 升序，got %v", got)
	}
	// upsert 覆盖同 ID
	_ = s.UpsertChunks(ctx, []knowledgebase.Chunk{{ID: "c1", KBID: kb.ID, DocID: doc.ID, Seq: 1, Content: "updated"}})
	got2, _ := s.ListChunks(ctx, kb.ID, doc.ID)
	if len(got2) != 2 {
		t.Errorf("覆盖后仍应 2 片，got %d", len(got2))
	}
	for _, c := range got2 {
		if c.ID == "c1" && c.Content != "updated" {
			t.Errorf("覆盖未生效: %s", c.Content)
		}
	}
}

func TestPGKBCascadeDelete(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	kb, _ := s.Create(ctx, mustKB("kb"))
	doc, _ := s.CreateDocument(ctx, knowledgebase.Document{KBID: kb.ID, Name: "d", MIME: "text/plain"})
	_ = s.UpsertChunks(ctx, []knowledgebase.Chunk{{ID: "c1", KBID: kb.ID, DocID: doc.ID, Seq: 0, Content: "x"}})
	if err := s.Delete(ctx, kb.ID); err != nil {
		t.Fatalf("Delete KB: %v", err)
	}
	// 级联清 docs + chunks
	if _, err := s.GetDocument(ctx, doc.ID); err != knowledgebase.ErrDocNotFound {
		t.Errorf("删 KB 应级联清文档，got %v", err)
	}
	chs, _ := s.ListChunks(ctx, kb.ID, "")
	if len(chs) != 0 {
		t.Errorf("删 KB 应级联清 chunks，got %d", len(chs))
	}
}

func TestPGDocumentCascadeDeleteChunks(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	kb, _ := s.Create(ctx, mustKB("kb"))
	doc, _ := s.CreateDocument(ctx, knowledgebase.Document{KBID: kb.ID, Name: "d", MIME: "text/plain"})
	_ = s.UpsertChunks(ctx, []knowledgebase.Chunk{{ID: "c1", KBID: kb.ID, DocID: doc.ID, Seq: 0, Content: "x"}})
	if err := s.DeleteDocument(ctx, doc.ID); err != nil {
		t.Fatalf("DeleteDocument: %v", err)
	}
	chs, _ := s.ListChunks(ctx, kb.ID, doc.ID)
	if len(chs) != 0 {
		t.Errorf("删文档应级联清 chunks，got %d", len(chs))
	}
}

func TestPGKBsCount(t *testing.T) {
	db := newTestDB(t)
	s := NewStore(db)
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	s.Create(ctx, mustKB("a"))
	s.Create(ctx, mustKB("b"))
	n, _ := s.KBsCount(ctx)
	// KBsCount 全表（不经租户过滤，seed 判空用）；本测 resetSchema 后只创建 2 条
	if n != 2 {
		t.Errorf("应 2，got %d", n)
	}
}
