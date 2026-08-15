// Package knowledgebase 是知识库 RAG 领域：文档->切片->embedding->向量检索。
//
// 租户私有；引用 dataservice vector（qdrant）+ storage（minio）实例，不自建基础设施。
// 向量存 qdrant（per-KB collection，collection=kb_{kbID}），chunk 文本/元数据存 PG
// （便于展示 + BM25 + 调试）；检索时 qdrant search 拿 point_id+score -> 回查 PG 取 content。
//
// MVP：text/markdown/html 纯函数解析，向量检索；hybrid/rerank/查询改写接口预留。
package knowledgebase

import (
	"errors"
	"time"
)

// 文档处理状态。
const (
	DocStatusParsing = "parsing" // 解析+切片+embedding 进行中
	DocStatusIndexed = "indexed" // 已入向量库 + PG，可检索
	DocStatusFailed  = "failed"  // 解析/embedding 失败（Message 存原因）
)

// RetrieverConfig 检索配置。MVP 仅实现向量检索；Hybrid/QueryRewrite/RerankerRef 接口预留
// （retriever 签名一次到位，后期填充分支避免返工）。
type RetrieverConfig struct {
	TopK         int    `json:"topK,omitempty"`         // 召回数，默认 5
	Hybrid       bool   `json:"hybrid,omitempty"`       // 向量+BM25 混合（预留）
	QueryRewrite bool   `json:"queryRewrite,omitempty"` // LLM 查询改写（预留）
	RerankerRef  string `json:"rerankerRef,omitempty"`  // rerank 通道 ref（预留）
}

// KnowledgeBase 知识库。引用 dataservice vector/storage 实例（不自建 Pod）。
type KnowledgeBase struct {
	ID              string          `json:"id"`
	TenantID        string          `json:"tenantId,omitempty"` // ctx 写入，请求体忽略
	AppID           string          `json:"appId,omitempty"`    // 可选（绑定应用）
	Name            string          `json:"name"`               // 租户内唯一
	VectorStoreRef  string          `json:"vectorStoreRef"`     // dataservice vector 实例 ID
	ObjectStoreRef  string          `json:"objectStoreRef"`     // dataservice storage 实例 ID
	EmbeddingModel  string          `json:"embeddingModel"`     // MaaS model id（Capabilities 含 "embedding"）
	EmbeddingDim    int             `json:"embeddingDim"`       // 向量维度（EnsureCollection 用，与模型对齐）
	RetrieverConfig RetrieverConfig `json:"retrieverConfig,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

// CollectionName 返回 KB 在 qdrant 的 collection 名（kb_{id}，全小写 + 连字符规范化）。
// 命名约定：per-KB collection，共享 qdrant 实例下多 KB 用 collection 名隔离。
func (k KnowledgeBase) CollectionName() string { return "kb_" + k.ID }

// BucketName 返回 KB 在 minio 的 bucket 名（kb-{tenant}，per-tenant 共享 bucket，per-KB 前缀 key）。
// per-tenant bucket：减少 bucket 数量；key 用 {kbID}/{docID}/{filename} 前缀隔离。
func (k KnowledgeBase) BucketName() string { return "kb-" + k.TenantID }

// Validate 校验必填：name + vectorStoreRef + objectStoreRef + embeddingModel + embeddingDim。
func (k KnowledgeBase) Validate() error {
	if k.Name == "" {
		return errInvalid("name")
	}
	if k.VectorStoreRef == "" {
		return errInvalid("vectorStoreRef")
	}
	if k.ObjectStoreRef == "" {
		return errInvalid("objectStoreRef")
	}
	if k.EmbeddingModel == "" {
		return errInvalid("embeddingModel")
	}
	if k.EmbeddingDim <= 0 {
		return errInvalid("embeddingDim")
	}
	return nil
}

// Document 知识库文档。上传后异步 parse+chunk+embed+index，状态机 parsing->indexed/failed。
type Document struct {
	ID         string            `json:"id"`
	KBID       string            `json:"kbId"`
	TenantID   string            `json:"tenantId,omitempty"`
	Name       string            `json:"name"`
	MIME       string            `json:"mime"`
	Status     string            `json:"status"`    // parsing | indexed | failed
	ObjectKey  string            `json:"objectKey"` // minio key（{kbID}/{docID}/{filename}）
	ChunkCount int               `json:"chunkCount"`
	Message    string            `json:"message,omitempty"`  // failed 时错误原因
	Metadata   map[string]string `json:"metadata,omitempty"` // {source,page,size,...}
	CreatedAt  time.Time         `json:"createdAt"`
}

// Chunk 文档切片。文本/元数据存 PG（ai_chunks），向量存 qdrant（point_id = Chunk.ID）。
type Chunk struct {
	ID        string            `json:"id"`
	KBID      string            `json:"kbId"`
	DocID     string            `json:"docId"`
	TenantID  string            `json:"tenantId,omitempty"`
	Seq       int               `json:"seq"` // 文档内序号
	Content   string            `json:"content"`
	Metadata  map[string]string `json:"metadata,omitempty"` // {page,start,...}
	CreatedAt time.Time         `json:"createdAt"`
}

// sentinel 错误（handler 映射 HTTP 状态用）。
var (
	ErrKBNotFound  = errors.New("知识库不存在")
	ErrKBExists    = errors.New("知识库名已存在")
	ErrDocNotFound = errors.New("文档不存在")
)

type fieldErr struct {
	field string
}

func (e fieldErr) Error() string { return "字段非法或缺失: " + e.field }

func errInvalid(field string) error { return fieldErr{field: field} }
