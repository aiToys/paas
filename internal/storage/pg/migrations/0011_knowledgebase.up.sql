-- 知识库 RAG（P1）：文档->切片->embedding->向量检索
-- 复用 dataservice vector(qdrant)+storage(minio) 实例，向量存 qdrant per-KB collection，
-- chunk 文本/元数据存 PG（便于展示+BM25+调试）。全表带 tenant_id 多租户隔离。
-- spec: docs/superpowers/specs/2026-08-05-ai-app-platform-design.md

CREATE TABLE ai_knowledgebases (
  id TEXT PRIMARY KEY,
  tenant_id TEXT NOT NULL,
  app_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL,
  vector_store_ref TEXT NOT NULL,   -- dataservice vector 实例 ID
  object_store_ref TEXT NOT NULL,   -- dataservice storage 实例 ID
  embedding_model TEXT NOT NULL,    -- MaaS model id（Capabilities 含 "embedding"）
  embedding_dim INT NOT NULL,
  retriever_config JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE(tenant_id, name)
);

CREATE TABLE ai_documents (
  id TEXT PRIMARY KEY,
  kb_id TEXT NOT NULL REFERENCES ai_knowledgebases(id) ON DELETE CASCADE,
  tenant_id TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  mime TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'parsing',   -- parsing | indexed | failed
  object_key TEXT NOT NULL DEFAULT '',       -- minio key（{kbID}/{docID}/{filename}）
  chunk_count INT NOT NULL DEFAULT 0,
  message TEXT NOT NULL DEFAULT '',          -- failed 时错误原因
  metadata JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_ai_documents_tenant_kb ON ai_documents (tenant_id, kb_id);

-- ai_chunks: 文本/元数据存 PG，向量存 qdrant（point_id = chunk.id）。
-- doc_id FK CASCADE：删文档级联清 chunks；删 KB 经 ai_documents CASCADE 间接清 chunks。
CREATE TABLE ai_chunks (
  id TEXT PRIMARY KEY,
  kb_id TEXT NOT NULL,
  doc_id TEXT NOT NULL REFERENCES ai_documents(id) ON DELETE CASCADE,
  tenant_id TEXT NOT NULL,
  seq INT NOT NULL DEFAULT 0,
  content TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_ai_chunks_tenant_kb_doc ON ai_chunks (tenant_id, kb_id, doc_id);
-- tsvector 列预留（BM25 后续）：ALTER TABLE ai_chunks ADD COLUMN tsv tsvector;
