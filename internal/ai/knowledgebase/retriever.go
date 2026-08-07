package knowledgebase

import (
	"context"
	"fmt"
)

// 默认切片参数（MVP 硬编码；后续可下沉到 RetrieverConfig）。
const (
	defaultChunkSize    = 500
	defaultChunkOverlap = 100
)

// Retriever 封装文档索引 + 向量检索链路。
//
// 依赖（依赖倒置，cmd/core 注入）：
//   - repo：KB/Document/Chunk 持久化
//   - vs：向量存储（qdrant 桥接）
//   - blob：对象存储（minio 桥接，读文档原文解析用）
//   - embed：EmbedderFactory（按 KB.EmbeddingModel 动态选 Embedder，支持多 embedding 模型）
type Retriever struct {
	repo  Repository
	vs    VectorStore
	embed EmbedderFactory
}

// NewRetriever 构造 Retriever。
func NewRetriever(repo Repository, vs VectorStore, embed EmbedderFactory) *Retriever {
	return &Retriever{repo: repo, vs: vs, embed: embed}
}

// IndexDocument 解析后切片 + embedding + 入向量库 + 入 PG。
// 异步调用方应在 goroutine 中调用（含网络 I/O，耗时）。任何失败 UpdateDocumentStatus(failed)
// 后返回错误；调用方忽略错误（状态已落库）。重索引时同 chunkID 覆盖（UpsertChunks + UpsertVectors 幂等）。
func (r *Retriever) IndexDocument(ctx context.Context, kb KnowledgeBase, doc Document, content string) error {
	// 确保向量 collection 存在（幂等，dim 与 KB 配置对齐）
	if err := r.vs.EnsureCollection(ctx, kb.CollectionName(), kb.EmbeddingDim); err != nil {
		r.markFailed(ctx, doc.ID, fmt.Sprintf("向量库初始化失败: %v", err))
		return err
	}
	// 旧 chunks 清理（重索引场景：先删旧再加新，避免残留）
	_ = r.repo.DeleteChunksByDoc(ctx, kb.ID, doc.ID)

	chunks := ChunkText(content, defaultChunkSize, defaultChunkOverlap)
	if len(chunks) == 0 {
		// 空文档直接 indexed（0 chunks）；同样用脱离请求 ctx，防超时卡 parsing。
		return r.repo.UpdateDocumentStatus(context.WithoutCancel(ctx), doc.ID, DocStatusIndexed, 0, "")
	}

	// 按 KB.EmbeddingModel 选 Embedder
	emb, err := r.embed.EmbedderFor(ctx, kb.EmbeddingModel)
	if err != nil {
		r.markFailed(ctx, doc.ID, fmt.Sprintf("embedding 模型不可用: %v", err))
		return err
	}
	vecs, err := emb.Embed(ctx, chunks)
	if err != nil {
		r.markFailed(ctx, doc.ID, fmt.Sprintf("embedding 失败: %v", err))
		return err
	}
	if len(vecs) != len(chunks) {
		err := fmt.Errorf("embedding 数量 %d 与切片 %d 不匹配", len(vecs), len(chunks))
		r.markFailed(ctx, doc.ID, err.Error())
		return err
	}

	// 构造 points + chunk 记录（chunkID = kb_doc_seq，确定性便于覆盖）
	points := make([]VectorPoint, len(chunks))
	chunkRecs := make([]Chunk, len(chunks))
	for i, c := range chunks {
		cid := fmt.Sprintf("%s_%s_%d", kb.ID, doc.ID, i)
		points[i] = VectorPoint{
			ID:     cid,
			Vector: vecs[i],
			Payload: map[string]string{
				"docId": doc.ID,
				"seq":   fmt.Sprintf("%d", i),
			},
		}
		chunkRecs[i] = Chunk{
			ID: cid, KBID: kb.ID, DocID: doc.ID, TenantID: kb.TenantID,
			Seq: i, Content: c,
			Metadata: map[string]string{"seq": fmt.Sprintf("%d", i)},
		}
	}

	if err := r.vs.UpsertVectors(ctx, kb.CollectionName(), points); err != nil {
		r.markFailed(ctx, doc.ID, fmt.Sprintf("向量入库失败: %v", err))
		return err
	}
	if err := r.repo.UpsertChunks(ctx, chunkRecs); err != nil {
		r.markFailed(ctx, doc.ID, fmt.Sprintf("切片入库失败: %v", err))
		return err
	}
	// 最终状态（indexed）写入用脱离请求生命周期的 ctx：中间操作（embedding/向量入库）
	// 已完成，状态落库不应因 processTimeout 超时或进程退出 cancel 而失败，否则文档卡 parsing。
	return r.repo.UpdateDocumentStatus(context.WithoutCancel(ctx), doc.ID, DocStatusIndexed, len(chunks), "")
}

func (r *Retriever) markFailed(ctx context.Context, docID, msg string) {
	// 最终状态（failed）写入用脱离请求生命周期的 ctx：失败原因已知，状态落库不应因
	// processTimeout 超时或进程退出 cancel 而失败，否则文档永远卡 parsing 无法重试。
	_ = r.repo.UpdateDocumentStatus(context.WithoutCancel(ctx), docID, DocStatusFailed, 0, msg)
}

// Retrieve 检索：embed query -> 向量检索 topK -> 回查 PG 取 chunk content。
// 返 ChunkHit 按 score 降序（向量库保证顺序）。未命中返 nil。
func (r *Retriever) Retrieve(ctx context.Context, kbID, query string) ([]ChunkHit, error) {
	kb, err := r.repo.Get(ctx, kbID)
	if err != nil {
		return nil, err
	}
	topK := kb.RetrieverConfig.TopK
	if topK <= 0 {
		topK = 5
	}

	emb, err := r.embed.EmbedderFor(ctx, kb.EmbeddingModel)
	if err != nil {
		return nil, fmt.Errorf("embedding 模型不可用: %w", err)
	}
	qvecs, err := emb.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("查询 embedding 失败: %w", err)
	}
	hits, err := r.vs.Search(ctx, kb.CollectionName(), qvecs[0], topK)
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, nil
	}

	// 回查 chunk content（ListChunks 取 KB 全部 + 按 ID 过滤；MVP 简化，
	// KB 内 chunks 通常 < 万级可接受。后续优化可加 GetChunksByIDs 接口）。
	allChunks, err := r.repo.ListChunks(ctx, kbID, "")
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Chunk, len(allChunks))
	for _, c := range allChunks {
		byID[c.ID] = c
	}
	out := make([]ChunkHit, 0, len(hits))
	for _, h := range hits {
		if c, ok := byID[h.ID]; ok {
			out = append(out, ChunkHit{Chunk: c, Score: h.Score})
		}
	}
	return out, nil
}

// DeleteDocumentVectors 删除文档的向量点（删文档时 handler 调，清 qdrant points）。
// chunkIDs 从 repo.ListChunks(docID) 取。
func (r *Retriever) DeleteDocumentVectors(ctx context.Context, kb KnowledgeBase, docID string) error {
	chunks, err := r.repo.ListChunks(ctx, kb.ID, docID)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return nil
	}
	ids := make([]string, len(chunks))
	for i, c := range chunks {
		ids[i] = c.ID
	}
	return r.vs.DeletePoints(ctx, kb.CollectionName(), ids)
}
