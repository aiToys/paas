// Package memory 提供 knowledgebase.Repository 的内存实现。
// 三组资源（KB/Document/Chunk）各一 map + mutex + 深拷贝 + tenant 强制过滤。
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/ai/knowledgebase"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store 实现 knowledgebase.Repository。
type Store struct {
	mu     sync.RWMutex
	kbs    map[string]knowledgebase.KnowledgeBase
	docs   map[string]knowledgebase.Document
	chunks map[string]knowledgebase.Chunk
	seq    int
}

func NewStore() *Store {
	return &Store{
		kbs:    map[string]knowledgebase.KnowledgeBase{},
		docs:   map[string]knowledgebase.Document{},
		chunks: map[string]knowledgebase.Chunk{},
	}
}

func tenantOrErr(ctx context.Context) (string, error) {
	tid, ok := tenant.TenantFrom(ctx)
	if !ok {
		// 无 tenant 统一 not found 不泄漏（与其他 AI 模块 memory tenantOrErr 一致）。
		return "", knowledgebase.ErrKBNotFound
	}
	return tid, nil
}

func cloneStrMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}

// cloneDoc 深拷 Document.Metadata。
func cloneDoc(d knowledgebase.Document) knowledgebase.Document {
	d.Metadata = cloneStrMap(d.Metadata)
	return d
}

// cloneChunk 深拷 Chunk.Metadata。
func cloneChunk(c knowledgebase.Chunk) knowledgebase.Chunk {
	c.Metadata = cloneStrMap(c.Metadata)
	return c
}

func (s *Store) newID(prefix string) string {
	s.seq++
	return fmt.Sprintf("%s-%d-%d", prefix, time.Now().UnixNano(), s.seq)
}

// ---- KB ----

func (s *Store) List(ctx context.Context) ([]knowledgebase.KnowledgeBase, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]knowledgebase.KnowledgeBase, 0)
	for _, k := range s.kbs {
		if k.TenantID != tid {
			continue
		}
		out = append(out, k) // KB 无引用字段，值拷即可
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// ListAll admin 跨租户列表（带 TenantID）。
func (s *Store) ListAll(ctx context.Context) ([]knowledgebase.KnowledgeBase, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]knowledgebase.KnowledgeBase, 0, len(s.kbs))
	for _, k := range s.kbs {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) Get(ctx context.Context, id string) (knowledgebase.KnowledgeBase, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return knowledgebase.KnowledgeBase{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	k, ok := s.kbs[id]
	if !ok || k.TenantID != tid {
		return knowledgebase.KnowledgeBase{}, knowledgebase.ErrKBNotFound
	}
	return k, nil
}

func (s *Store) Create(ctx context.Context, kb knowledgebase.KnowledgeBase) (knowledgebase.KnowledgeBase, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return knowledgebase.KnowledgeBase{}, err
	}
	if err := kb.Validate(); err != nil {
		return knowledgebase.KnowledgeBase{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ex := range s.kbs {
		if ex.TenantID == tid && ex.Name == kb.Name {
			return knowledgebase.KnowledgeBase{}, knowledgebase.ErrKBExists
		}
	}
	if kb.ID == "" {
		kb.ID = s.newID("kb")
	}
	kb.TenantID = tid
	now := time.Now()
	kb.CreatedAt = now
	kb.UpdatedAt = now
	s.kbs[kb.ID] = kb
	return kb, nil
}

func (s *Store) Update(ctx context.Context, kb knowledgebase.KnowledgeBase) (knowledgebase.KnowledgeBase, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return knowledgebase.KnowledgeBase{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ex, ok := s.kbs[kb.ID]
	if !ok || ex.TenantID != tid {
		return knowledgebase.KnowledgeBase{}, knowledgebase.ErrKBNotFound
	}
	// name 唯一性复校（排除自身）
	for id, other := range s.kbs {
		if id != kb.ID && other.TenantID == tid && other.Name == kb.Name {
			return knowledgebase.KnowledgeBase{}, knowledgebase.ErrKBExists
		}
	}
	// 仅允许改 name/embeddingModel/embeddingDim/retrieverConfig；refs 不变（迁移属重建）。
	ex.Name = kb.Name
	if kb.EmbeddingModel != "" {
		ex.EmbeddingModel = kb.EmbeddingModel
	}
	if kb.EmbeddingDim > 0 {
		ex.EmbeddingDim = kb.EmbeddingDim
	}
	ex.RetrieverConfig = kb.RetrieverConfig
	ex.UpdatedAt = time.Now()
	if err := ex.Validate(); err != nil {
		return knowledgebase.KnowledgeBase{}, err
	}
	s.kbs[kb.ID] = ex
	return ex, nil
}

// Delete 级联清 KB 的 documents + chunks（向量/原文清理由 handler 调 VectorStore/BlobStore）。
func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k, ok := s.kbs[id]
	if !ok || k.TenantID != tid {
		return knowledgebase.ErrKBNotFound
	}
	// 级联清 documents
	for docID, d := range s.docs {
		if d.KBID == id {
			delete(s.docs, docID)
		}
	}
	// 级联清 chunks
	for chunkID, c := range s.chunks {
		if c.KBID == id {
			delete(s.chunks, chunkID)
		}
	}
	delete(s.kbs, id)
	return nil
}

// KBsCount 返全表 KB 数（不经租户过滤，供启动期 seed 判空用；与 pg 全表 COUNT 语义一致）。
func (s *Store) KBsCount(ctx context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.kbs), nil
}

// ---- Document ----

func (s *Store) ListDocuments(ctx context.Context, kbID string) ([]knowledgebase.Document, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// 校验 KB 归属（跨租户 KB 不泄漏其文档列表）
	k, ok := s.kbs[kbID]
	if !ok || k.TenantID != tid {
		return nil, knowledgebase.ErrKBNotFound
	}
	out := make([]knowledgebase.Document, 0)
	for _, d := range s.docs {
		if d.KBID == kbID && d.TenantID == tid {
			out = append(out, cloneDoc(d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (s *Store) GetDocument(ctx context.Context, docID string) (knowledgebase.Document, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return knowledgebase.Document{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.docs[docID]
	if !ok || d.TenantID != tid {
		return knowledgebase.Document{}, knowledgebase.ErrDocNotFound
	}
	return cloneDoc(d), nil
}

func (s *Store) CreateDocument(ctx context.Context, doc knowledgebase.Document) (knowledgebase.Document, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return knowledgebase.Document{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 校验 KB 归属
	k, ok := s.kbs[doc.KBID]
	if !ok || k.TenantID != tid {
		return knowledgebase.Document{}, knowledgebase.ErrKBNotFound
	}
	if doc.ID == "" {
		doc.ID = s.newID("doc")
	}
	doc.TenantID = tid
	if doc.Status == "" {
		doc.Status = knowledgebase.DocStatusParsing
	}
	doc.Metadata = cloneStrMap(doc.Metadata)
	doc.CreatedAt = time.Now()
	s.docs[doc.ID] = doc
	return cloneDoc(doc), nil
}

func (s *Store) UpdateDocumentStatus(ctx context.Context, docID, status string, chunkCount int, msg string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[docID]
	if !ok || d.TenantID != tid {
		return knowledgebase.ErrDocNotFound
	}
	d.Status = status
	d.ChunkCount = chunkCount
	d.Message = msg
	s.docs[docID] = d
	return nil
}

// UpdateDocumentObjectKey 更新文档原文对象 key（memory 路径同步）。
func (s *Store) UpdateDocumentObjectKey(ctx context.Context, docID, objectKey string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[docID]
	if !ok || d.TenantID != tid {
		return knowledgebase.ErrDocNotFound
	}
	d.ObjectKey = objectKey
	s.docs[docID] = d
	return nil
}

func (s *Store) DeleteDocument(ctx context.Context, docID string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[docID]
	if !ok || d.TenantID != tid {
		return knowledgebase.ErrDocNotFound
	}
	// 级联清 chunks
	for chunkID, c := range s.chunks {
		if c.DocID == docID {
			delete(s.chunks, chunkID)
		}
	}
	delete(s.docs, docID)
	return nil
}

// ---- Chunk ----

func (s *Store) ListChunks(ctx context.Context, kbID, docID string) ([]knowledgebase.Chunk, error) {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	// 校验 KB 归属
	k, ok := s.kbs[kbID]
	if !ok || k.TenantID != tid {
		return nil, knowledgebase.ErrKBNotFound
	}
	out := make([]knowledgebase.Chunk, 0)
	for _, c := range s.chunks {
		if c.KBID != kbID || c.TenantID != tid {
			continue
		}
		if docID != "" && c.DocID != docID {
			continue
		}
		out = append(out, cloneChunk(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

func (s *Store) UpsertChunks(ctx context.Context, chunks []knowledgebase.Chunk) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
		c.Metadata = cloneStrMap(c.Metadata)
		s.chunks[c.ID] = c
	}
	return nil
}

func (s *Store) DeleteChunksByDoc(ctx context.Context, kbID, docID string) error {
	tid, err := tenantOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 校验 KB 归属
	k, ok := s.kbs[kbID]
	if !ok || k.TenantID != tid {
		return knowledgebase.ErrKBNotFound
	}
	for chunkID, c := range s.chunks {
		if c.KBID == kbID && c.DocID == docID {
			delete(s.chunks, chunkID)
		}
	}
	return nil
}
