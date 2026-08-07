package knowledgebase

import (
	"context"
	"io"

	"github.com/aitoys/paas/pkg/provider"
)

// VectorStore 向量存储抽象（依赖倒置）。cmd/core 桥接实现用 dataservice.Repository
// 拿 qdrant 连接 + github.com/qdrant/go-client 连。KB 不直接 import dataservice 包，测试用 mock。
type VectorStore interface {
	// EnsureCollection 创建 collection（若不存在）+ 设置向量维度 + 距离度量（cosine）。
	EnsureCollection(ctx context.Context, collection string, dim int) error
	// UpsertVectors 批量写入/更新向量点（point_id = VectorPoint.ID）。
	UpsertVectors(ctx context.Context, collection string, points []VectorPoint) error
	// Search 向量检索，返 topK 个最近邻（按 score 降序）。
	Search(ctx context.Context, collection string, query []float32, topK int) ([]VectorHit, error)
	// DeletePoints 删除指定 point（删文档时清该文档的向量）。
	DeletePoints(ctx context.Context, collection string, ids []string) error
	// DeleteCollection 删除整个 collection（删 KB 时清向量）。
	DeleteCollection(ctx context.Context, collection string) error
}

// VectorPoint 是一个向量点。Payload 存关联元数据（doc_id/chunk_seq 等，便于检索过滤）。
type VectorPoint struct {
	ID      string            `json:"id"`
	Vector  []float32         `json:"vector"`
	Payload map[string]string `json:"payload,omitempty"`
}

// VectorHit 是检索命中。ID = point_id（= chunk_id），Score 越大越相似（cosine）。
type VectorHit struct {
	ID    string  `json:"id"`
	Score float32 `json:"score"`
}

// BlobStore 对象存储抽象（依赖倒置）。cmd/core 桥接用 dataservice.Repository
// 拿 minio 连接 + github.com/minio/minio-go/v7 连。
type BlobStore interface {
	// EnsureBucket 创建 bucket（若不存在）。
	EnsureBucket(ctx context.Context, bucket string) error
	// PutObject 上传对象（文档原文）。
	PutObject(ctx context.Context, bucket, key string, r io.Reader, contentType string) error
	// GetObject 下载对象（解析时读原文）。
	GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	// DeleteObject 删除对象（删文档时清原文）。
	DeleteObject(ctx context.Context, bucket, key string) error
}

// ChunkHit 是检索结果（chunk + 相似度），handler 返给客户端。
type ChunkHit struct {
	Chunk Chunk   `json:"chunk"`
	Score float32 `json:"score"`
}

// EmbedderFactory 按 model ID 解析 Embedder（依赖倒置，cmd/core 桥接 MaaS catalog）。
// Retriever 按 KB.EmbeddingModel 动态选 Embedder，支持多 embedding 模型（不同 KB 可用不同模型）。
// 缓存由实现负责（避免每次调用 BuildProvider 开销）。
type EmbedderFactory interface {
	EmbedderFor(ctx context.Context, modelID string) (provider.Embedder, error)
}
