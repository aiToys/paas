// Package pg 提供 knowledgebase.Repository 的 PostgreSQL 实现。
// 显式 WHERE tenant_id=$1 多租户过滤（与内存 1:1）；Create 以 ctx 租户为准、忽略请求体 TenantID；
// retriever_config/metadata 用 JSONB 列；FK CASCADE 级联删（删 KB 清 docs+chunks，删 doc 清 chunks）。
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/ai/knowledgebase"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Store 是 knowledgebase.Repository 的 PostgreSQL 实现。
type Store struct {
	db  *storagepg.DB
	seq atomic.Int64
}

func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

// 列顺序与 scan helper 对齐（必须一致，列错位是最易踩坑）。
const (
	kbCols    = `id, tenant_id, app_id, name, vector_store_ref, object_store_ref, embedding_model, embedding_dim, retriever_config, created_at, updated_at`
	docCols   = `id, kb_id, tenant_id, name, mime, status, object_key, chunk_count, message, metadata, created_at`
	chunkCols = `id, kb_id, doc_id, tenant_id, seq, content, metadata, created_at`
)

// marshalStrMap nil -> '{}'（JSONB NOT NULL DEFAULT '{}' 一致）。
func marshalStrMap(m map[string]string) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

func unmarshalStrMap(raw []byte) map[string]string {
	m := map[string]string{}
	if len(raw) == 0 || string(raw) == "null" {
		return m
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]string{}
	}
	return m
}

func scanKB(r storagepg.RowScanner, k *knowledgebase.KnowledgeBase) error {
	var rcRaw []byte
	if err := r.Scan(&k.ID, &k.TenantID, &k.AppID, &k.Name, &k.VectorStoreRef, &k.ObjectStoreRef, &k.EmbeddingModel, &k.EmbeddingDim, &rcRaw, &k.CreatedAt, &k.UpdatedAt); err != nil {
		return err
	}
	if len(rcRaw) > 0 && string(rcRaw) != "null" {
		_ = json.Unmarshal(rcRaw, &k.RetrieverConfig)
	}
	return nil
}

func scanDoc(r storagepg.RowScanner, d *knowledgebase.Document) error {
	var metaRaw []byte
	if err := r.Scan(&d.ID, &d.KBID, &d.TenantID, &d.Name, &d.MIME, &d.Status, &d.ObjectKey, &d.ChunkCount, &d.Message, &metaRaw, &d.CreatedAt); err != nil {
		return err
	}
	d.Metadata = unmarshalStrMap(metaRaw)
	return nil
}

func scanChunk(r storagepg.RowScanner, c *knowledgebase.Chunk) error {
	var metaRaw []byte
	if err := r.Scan(&c.ID, &c.KBID, &c.DocID, &c.TenantID, &c.Seq, &c.Content, &metaRaw, &c.CreatedAt); err != nil {
		return err
	}
	c.Metadata = unmarshalStrMap(metaRaw)
	return nil
}

func (s *Store) newID(prefix string) string {
	s.seq.Add(1)
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), s.seq.Load())
}

// kbExists 校验 KB 归属当前租户（ListDocuments/ListChunks 前置，跨租户不泄漏）。
func (s *Store) kbExists(ctx context.Context, tid, kbID string) error {
	var exists int
	err := s.db.Pool().QueryRow(ctx,
		`SELECT 1 FROM ai_knowledgebases WHERE id=$1 AND tenant_id=$2`, kbID, tid).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return knowledgebase.ErrKBNotFound
	}
	return err
}

// ---- KB ----

func (s *Store) List(ctx context.Context) ([]knowledgebase.KnowledgeBase, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+kbCols+` FROM ai_knowledgebases WHERE tenant_id=$1 ORDER BY created_at DESC`, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]knowledgebase.KnowledgeBase, 0)
	for rows.Next() {
		var k knowledgebase.KnowledgeBase
		if err = scanKB(rows, &k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (knowledgebase.KnowledgeBase, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return knowledgebase.KnowledgeBase{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+kbCols+` FROM ai_knowledgebases WHERE id=$1 AND tenant_id=$2`, id, tid)
	var k knowledgebase.KnowledgeBase
	if err = scanKB(row, &k); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return knowledgebase.KnowledgeBase{}, knowledgebase.ErrKBNotFound
		}
		return knowledgebase.KnowledgeBase{}, err
	}
	return k, nil
}

func (s *Store) Create(ctx context.Context, kb knowledgebase.KnowledgeBase) (knowledgebase.KnowledgeBase, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return knowledgebase.KnowledgeBase{}, err
	}
	if err := kb.Validate(); err != nil {
		return knowledgebase.KnowledgeBase{}, err
	}
	if kb.ID == "" {
		kb.ID = s.newID("kb")
	}
	kb.TenantID = tid
	if kb.CreatedAt.IsZero() {
		kb.CreatedAt = time.Now()
		kb.UpdatedAt = kb.CreatedAt
	}
	rcBytes, _ := json.Marshal(kb.RetrieverConfig)
	row := s.db.Pool().QueryRow(ctx, `
INSERT INTO ai_knowledgebases (id, tenant_id, app_id, name, vector_store_ref, object_store_ref, embedding_model, embedding_dim, retriever_config, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
RETURNING `+kbCols,
		kb.ID, kb.TenantID, kb.AppID, kb.Name, kb.VectorStoreRef, kb.ObjectStoreRef, kb.EmbeddingModel, kb.EmbeddingDim, rcBytes, kb.CreatedAt, kb.UpdatedAt,
	)
	var saved knowledgebase.KnowledgeBase
	if err = scanKB(row, &saved); err != nil {
		if storagepg.IsUniqueViolation(err) {
			return knowledgebase.KnowledgeBase{}, knowledgebase.ErrKBExists
		}
		return knowledgebase.KnowledgeBase{}, err
	}
	return saved, nil
}

func (s *Store) Update(ctx context.Context, kb knowledgebase.KnowledgeBase) (knowledgebase.KnowledgeBase, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return knowledgebase.KnowledgeBase{}, err
	}
	if kb.Name == "" {
		return knowledgebase.KnowledgeBase{}, fmt.Errorf("name 不能为空")
	}
	rcBytes, _ := json.Marshal(kb.RetrieverConfig)
	// 空字段保留原值：embedding_model 空走 COALESCE 保留；embedding_dim<=0 保留。
	row := s.db.Pool().QueryRow(ctx, `
UPDATE ai_knowledgebases SET name=$1, embedding_model=COALESCE(NULLIF($2,''), embedding_model),
  embedding_dim=CASE WHEN $3>0 THEN $3 ELSE embedding_dim END,
  retriever_config=$4, updated_at=NOW()
WHERE id=$5 AND tenant_id=$6 RETURNING `+kbCols,
		kb.Name, kb.EmbeddingModel, kb.EmbeddingDim, rcBytes, kb.ID, tid,
	)
	var saved knowledgebase.KnowledgeBase
	if err = scanKB(row, &saved); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return knowledgebase.KnowledgeBase{}, knowledgebase.ErrKBNotFound
		}
		if storagepg.IsUniqueViolation(err) {
			return knowledgebase.KnowledgeBase{}, knowledgebase.ErrKBExists
		}
		return knowledgebase.KnowledgeBase{}, err
	}
	return saved, nil
}

// Delete 删 KB（FK CASCADE 级联清 ai_documents + ai_chunks；向量/原文清理由 handler 调 VectorStore/BlobStore）。
func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM ai_knowledgebases WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return knowledgebase.ErrKBNotFound
	}
	return nil
}

func (s *Store) KBsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM ai_knowledgebases`).Scan(&n)
	return n, err
}

// ---- Document ----

func (s *Store) ListDocuments(ctx context.Context, kbID string) ([]knowledgebase.Document, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.kbExists(ctx, tid, kbID); err != nil {
		return nil, err
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+docCols+` FROM ai_documents WHERE kb_id=$1 AND tenant_id=$2 ORDER BY created_at DESC`, kbID, tid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]knowledgebase.Document, 0)
	for rows.Next() {
		var d knowledgebase.Document
		if err = scanDoc(rows, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDocument(ctx context.Context, docID string) (knowledgebase.Document, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return knowledgebase.Document{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+docCols+` FROM ai_documents WHERE id=$1 AND tenant_id=$2`, docID, tid)
	var d knowledgebase.Document
	if err = scanDoc(row, &d); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return knowledgebase.Document{}, knowledgebase.ErrDocNotFound
		}
		return knowledgebase.Document{}, err
	}
	return d, nil
}

func (s *Store) CreateDocument(ctx context.Context, doc knowledgebase.Document) (knowledgebase.Document, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return knowledgebase.Document{}, err
	}
	if err := s.kbExists(ctx, tid, doc.KBID); err != nil {
		return knowledgebase.Document{}, err
	}
	if doc.ID == "" {
		doc.ID = s.newID("doc")
	}
	doc.TenantID = tid
	if doc.Status == "" {
		doc.Status = knowledgebase.DocStatusParsing
	}
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = time.Now()
	}
	metaBytes, _ := marshalStrMap(doc.Metadata)
	row := s.db.Pool().QueryRow(ctx, `
INSERT INTO ai_documents (id, kb_id, tenant_id, name, mime, status, object_key, chunk_count, message, metadata, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
RETURNING `+docCols,
		doc.ID, doc.KBID, doc.TenantID, doc.Name, doc.MIME, doc.Status, doc.ObjectKey, doc.ChunkCount, doc.Message, metaBytes, doc.CreatedAt,
	)
	var saved knowledgebase.Document
	if err = scanDoc(row, &saved); err != nil {
		return knowledgebase.Document{}, err
	}
	return saved, nil
}

func (s *Store) UpdateDocumentStatus(ctx context.Context, docID, status string, chunkCount int, msg string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE ai_documents SET status=$1, chunk_count=$2, message=$3 WHERE id=$4 AND tenant_id=$5`,
		status, chunkCount, msg, docID, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return knowledgebase.ErrDocNotFound
	}
	return nil
}

// DeleteDocument 删文档（FK CASCADE 清 ai_chunks；向量/原文清理由 handler 调 VectorStore/BlobStore）。
func (s *Store) DeleteDocument(ctx context.Context, docID string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM ai_documents WHERE id=$1 AND tenant_id=$2`, docID, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return knowledgebase.ErrDocNotFound
	}
	return nil
}

// ---- Chunk ----

func (s *Store) ListChunks(ctx context.Context, kbID, docID string) ([]knowledgebase.Chunk, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.kbExists(ctx, tid, kbID); err != nil {
		return nil, err
	}
	q := `SELECT ` + chunkCols + ` FROM ai_chunks WHERE kb_id=$1 AND tenant_id=$2`
	args := []any{kbID, tid}
	if docID != "" {
		q += ` AND doc_id=$3`
		args = append(args, docID)
	}
	q += ` ORDER BY seq ASC`
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]knowledgebase.Chunk, 0)
	for rows.Next() {
		var c knowledgebase.Chunk
		if err = scanChunk(rows, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpsertChunks 批量 upsert（ON CONFLICT (id) DO UPDATE，重索引覆盖语义，与内存一致）。
func (s *Store) UpsertChunks(ctx context.Context, chunks []knowledgebase.Chunk) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return nil
	}
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	for _, c := range chunks {
		if c.TenantID == "" {
			c.TenantID = tid
		}
		if c.TenantID != tid {
			return fmt.Errorf("chunk tenant 不匹配")
		}
		if c.ID == "" {
			c.ID = s.newID("chk")
		}
		if c.CreatedAt.IsZero() {
			c.CreatedAt = time.Now()
		}
		metaBytes, _ := marshalStrMap(c.Metadata)
		if _, err := tx.Exec(ctx, `
INSERT INTO ai_chunks (id, kb_id, doc_id, tenant_id, seq, content, metadata, created_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (id) DO UPDATE SET seq=$5, content=$6, metadata=$7`,
			c.ID, c.KBID, c.DocID, c.TenantID, c.Seq, c.Content, metaBytes, c.CreatedAt,
		); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) DeleteChunksByDoc(ctx context.Context, kbID, docID string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	if err := s.kbExists(ctx, tid, kbID); err != nil {
		return err
	}
	_, err = s.db.Pool().Exec(ctx,
		`DELETE FROM ai_chunks WHERE kb_id=$1 AND doc_id=$2 AND tenant_id=$3`, kbID, docID, tid)
	return err
}
