package knowledgebase

import "context"

// Repository 是知识库持久化接口。全方法从 ctx 取租户强制过滤；
// 跨租户访问统一 ErrKBNotFound/ErrDocNotFound（不泄漏存在性）。
//
// 三组资源：KB / Document / Chunk。Document/Chunk 操作均校验归属同租户（防跨租户越权）。
type Repository interface {
	// ---- KB ----
	// List 列本租户 KB，按 CreatedAt 倒序。
	List(ctx context.Context) ([]KnowledgeBase, error)
	// Get 读取单条 KB（跨租户 ErrKBNotFound）。
	Get(ctx context.Context, id string) (KnowledgeBase, error)
	// Create 创建（Validate + 租户内 name 唯一）。
	Create(ctx context.Context, kb KnowledgeBase) (KnowledgeBase, error)
	// Update 更新（retrieverConfig/embeddingModel 等可改；name 唯一性复校）。
	Update(ctx context.Context, kb KnowledgeBase) (KnowledgeBase, error)
	// Delete 删除 KB（级联清 documents + chunks；向量清理由 handler 调 VectorStore）。
	Delete(ctx context.Context, id string) error
	// KBsCount 本租户 KB 数（seed 判空用）。
	KBsCount(ctx context.Context) (int, error)

	// ---- Document ----
	// ListDocuments 列 KB 下文档（校验 KB 归属同租户）。
	ListDocuments(ctx context.Context, kbID string) ([]Document, error)
	// GetDocument 读取单条文档（校验归属同租户）。
	GetDocument(ctx context.Context, docID string) (Document, error)
	// CreateDocument 创建文档记录（status 默认 parsing）。
	CreateDocument(ctx context.Context, doc Document) (Document, error)
	// UpdateDocumentStatus 更新文档状态 + chunkCount + message（异步处理完成回写）。
	UpdateDocumentStatus(ctx context.Context, docID, status string, chunkCount int, msg string) error
	// DeleteDocument 删除文档（级联清 chunks；向量+原文清理由 handler 调 VectorStore/BlobStore）。
	DeleteDocument(ctx context.Context, docID string) error

	// ---- Chunk ----
	// ListChunks 列 chunks（docID 空则 KB 全部；校验 KB 归属）。
	ListChunks(ctx context.Context, kbID, docID string) ([]Chunk, error)
	// UpsertChunks 批量写入 chunks（IndexDocument 解析后批量落库）。
	UpsertChunks(ctx context.Context, chunks []Chunk) error
	// DeleteChunksByDoc 删指定文档的 chunks（删文档/重建索引时调）。
	DeleteChunksByDoc(ctx context.Context, kbID, docID string) error
}
