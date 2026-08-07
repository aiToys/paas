# P1 实施计划：知识库 RAG（文档->切片->embedding->向量检索）

> 2026-08-05 · spec: `docs/superpowers/specs/2026-08-05-ai-app-platform-design.md`
> 执行方式：inline TDD（Embedder 接口 -> KB 领域 -> 解析/检索 -> PG -> handler -> 桥接装配 -> 前端 -> 部署）

## 现状（调研确认）

- **Embedding 能力缺口**：`pkg/provider/provider.go:34-37` `Provider` 接口只有 `Name()`+`Chat()`，无 Embed。`OpenAICompatibleProvider` 只调 `/chat/completions`。
- **Catalog 已就绪**：`internal/maas/airouter_catalog.go:69` 已有 `text-embedding-v4`（通义千问，Caps 含 `embedding`），走 airouter 真实通道。**bge-m3 在 `DeprecatedSeedModelIDs` 不加回**。
- **向量库基础设施就绪**：dataservice vector kind（qdrant v1.12.4，per-instance StatefulSet，端口 6333，API key 鉴权，PVC 持久化），`internal/dataservice/connection.go:150` BuildConnection 返回 host+port+api_key+uri。
- **对象存储基础设施就绪**：dataservice storage kind（minio，端口 9000），BuildConnection 返回 endpoint+accessKey+secretKey。
- **Binding 模板就绪**：`internal/core/application/handler.go:25` `BindingInjector` 接口（OnBind/OnUnbind）+ `cmd/core/binding_injector.go` `dsBindingInjector` 样板。Binding.Type 是裸字符串。
- **脚手架参照**：`internal/dataservice/` 最接近（领域+Repository+memory/pg+handler+admin_handler+connection）。
- **migration 最大编号 0010**，下一个 0011。
- **go.mod 无 qdrant/minio SDK**，需引入（均 Apache 2.0）。

## 设计决策

1. **Embedder 独立接口，不污染 Provider**：`pkg/provider` 加 `Embedder` 接口（`Embed(ctx, []string) ([][]float32, error)`），`OpenAICompatibleProvider` 同时实现 Provider+Embedder（调 `/embeddings`，非流式）。MockProvider 加 Embed 返固定维度零向量。不强制所有 Provider 实现（echo 可选）。catalog 加载时 `if e, ok := p.(Embedder); ok { ... }` 探测。

2. **向量存储路径 B：KB 复用 dataservice vector，不自建**。KB spec 引用 `vectorStoreRef`（dataservice vector ID）+ `objectStoreRef`（dataservice storage ID）。KB 不部署 qdrant/minio Pod，靠 cmd/core 桥接从 dataservice.Repository 拿连接 + qdrant/minio SDK 直连。**共享 qdrant 实例 + per-KB collection 名隔离**（省资源，collection = `kb_{kbID}`）。

3. **KB 依赖倒置，定义 VectorStore/BlobStore 接口**：KB 包内定义 `VectorStore`（EnsureCollection/Upsert/Search/Delete）+ `BlobStore`（Put/Get/Delete）接口，cmd/core 桥接实现（dataservice.Repository 拿连接 + qdrant/minio SDK）。KB 不直接 import dataservice 包，测试用 mock。

4. **chunks 双存储**：chunk 文本+元数据存 PG（`ai_chunks` 表，便于 BM25 + 展示 + 调试），向量存 qdrant（point_id = chunk_id）。检索：embed query -> qdrant search 拿 point_ids+score -> PG 回查 content。BM25 走 PG `tsvector`（接口预留，MVP 先纯向量）。

5. **文档解析 MVP：纯函数 + 供应商 API 预留**。`parser.go` 按 MIME 分发：text/markdown/html 内置纯函数（`golang.org/x/net/html`，已在依赖）；PDF/Office 调供应商文档解析 API（接口预留，MVP 返 "unsupported" 友好错误，不阻塞 text/md/html 主链路）。切片器 `chunker.go` 纯函数（按 token/段落，可配 size/overlap）。

6. **检索器接口预留扩展**。`RetrieverConfig{Hybrid bool, TopK int, QueryRewrite bool, RerankerRef string}`。MVP 实现：embed query + qdrant 向量检索。查询改写（LLM）/BM25 混合/rerank 接口预留，分阶段填充（避免返工：retriever 签名一次到位）。

7. **Embedder 桥接复用 MaaS catalog**。cmd/core 从 MaaS Repository 拿 KB 配置的 embedding model（如 text-embedding-v4）-> `BuildProvider` -> `.(Embedder)` 注入 KB。KB 不自建 embedding 通路。

8. **binding 注入 KB_API_BASE/ID/KEY**。`kbBindingInjector` 参照 `dsBindingInjector`：OnBind 注入 appconfig（TypeSecret）`KB_API_BASE`（core `/v1`）+ `KB_ID` + `KB_API_KEY`（应用级 Key 归因）。OnUnbind 删 key。Type=`"knowledgebase"`。

9. **KB 创建前提**：用户先建（或租户有平台预建共享的）dataservice vector + storage 实例，再建 KB 引用。KB Validate 校验 vectorStoreRef/objectStoreRef 非空 + embeddingModel 在 catalog 存在。MVP 不自动创建 dataservice（YAGNI，避免隐式资源）。

10. **平台级 admin 总览**：`/api/admin/knowledgebases`（ListAll 跨租户只读，参照其他 admin handler L1）留后续，MVP 先租户自助。

## 任务清单

### T1: Embedder 接口 + OpenAICompatibleProvider.Embed()
**改** `pkg/provider/provider.go`：
- 加 `Embedder` 接口：`Embed(ctx context.Context, texts []string) ([][]float32, error)`
- 加 `EmbedRequest`/`EmbedResponse` 类型（如需，MVP 直接返 `[][]float32`）。

**改** `internal/maas/openai_compatible.go`：
- 加 `(p *OpenAICompatibleProvider) Embed(ctx, texts)` 方法：复用 Chat 的凭证解析（line 85-91）+ endpoint = `baseURL+"/embeddings"` + body `{"model":upstreamModel,"input":texts}` + 非流式 `json.Unmarshal` 解析 `{"data":[{"embedding":[...]}]}` + 维度校验一致。
- httpClient 复用 `httputil.NewClient`（含 CheckRedirect 防护）。

**改** `internal/maas/mock.go`：`MockProvider.Embed` 返固定维度（如 1024）零向量（演示/测试用）。

**测试**：`openai_compatible_test.go` 加 `TestEmbed`（mock httptest server 返 embedding JSON）+ `TestEmbedCredentialMissing`。

---

### T2: KB 领域 + Repository 接口 + memory
**新建** `internal/ai/knowledgebase/`：
- `model.go`：`KnowledgeBase{ID/TenantID/AppID/Name/VectorStoreRef/ObjectStoreRef/EmbeddingModel/RetrieverConfig/CreatedAt}` + `Document{ID/KBID/Name/MIME/Status/ObjectKey/ChunkCount/Metadata}` + `Chunk{ID/KBID/DocID/Seq/Content/Metadata}` + 状态常量（`DocStatusParsing/Indexed/Failed`）+ `RetrieverConfig{TopK/Hybrid/QueryRewrite/RerankerRef}`。
- `store.go`：依赖倒置接口--
  ```go
  type VectorStore interface {
      EnsureCollection(ctx, collection string, dim int) error
      UpsertVectors(ctx, collection string, points []VectorPoint) error
      Search(ctx, collection string, query []float32, topK int) ([]VectorHit, error)
      DeleteCollection(ctx, collection string) error
  }
  type BlobStore interface {
      PutObject(ctx, bucket, key string, r io.Reader, contentType string) error
      GetObject(ctx, bucket, key string) (io.ReadCloser, error)
      DeleteObject(ctx, bucket, key string) error
  }
  ```
- `repository.go`：`Repository` 接口（KB CRUD：`List/Get/Create/Update/Delete` + Document CRUD：`ListDocuments/GetDocument/CreateDocument/UpdateDocumentStatus/DeleteDocument` + `ListChunks/UpsertChunk/DeleteChunks`），全方法带 ctx + tenant 强制过滤（参照 dataservice.Repository）。
- `memory/store.go`：内存实现（map + mutex + 深拷贝）。
- `Validate`：name 非空 + vectorStoreRef/objectStoreRef/embeddingModel 非空。

**测试**：CRUD + 跨租户 not found + Validate。

---

### T3: 文档解析 + 切片
**新建** `internal/ai/knowledgebase/parser.go`：
- `Parse(mime string, r io.Reader) (content string, err error)`：按 MIME 分发--`text/plain`/`text/markdown` 直接读；`text/html` 用 `golang.org/x/net/html` 提取文本；`application/pdf` 等返 `ErrUnsupportedMIME`（接口预留，MVP 不阻塞）。
- 后续 PDF/Office 走供应商文档解析 API（接口位预留：`type DocParser interface{ Parse(ctx, mime, r) (string, error) }`）。

**新建** `internal/ai/knowledgebase/chunker.go`：
- `ChunkText(content string, size, overlap int) []string`：按 token 估算（`len/4` 近似）切，overlap 滑窗，纯函数。

**测试**：各 MIME + 切片 size/overlap 边界。

---

### T4: 检索器
**新建** `internal/ai/knowledgebase/retriever.go`：
- `type Retriever struct { vs VectorStore; embed Embedder; repo Repository }`
- `Retrieve(ctx, kbID, query string) ([]ChunkHit, error)`：embed query -> `vs.Search(collection=kb_{kbID}, query, topK)` -> 拿 chunk_ids+score -> `repo.ListChunks` 回查 content -> 返 `[]ChunkHit{Chunk, Score}`。
- `IndexDocument(ctx, kb, doc, content)`：切片 -> embed（批量）-> `vs.UpsertVectors`（point_id=chunk_id）+ `repo.UpsertChunk`（存 content+metadata）。
- `RetrieverConfig` 透传（Hybrid/QueryRewrite/RerankerRef 预留开关，MVP 不实现分支但签名含）。

**测试**：mock VectorStore + mock Embedder，验证 Index->Retrieve 链路。

---

### T5: migration + pg Store
**新建** `internal/storage/pg/migrations/0011_knowledgebase.up.sql` + `.down.sql`：
```sql
CREATE TABLE ai_knowledgebases (
  id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, app_id TEXT,
  name TEXT NOT NULL, vector_store_ref TEXT NOT NULL, object_store_ref TEXT NOT NULL,
  embedding_model TEXT NOT NULL, retriever_config JSONB,
  created_at TIMESTAMPTZ DEFAULT NOW(), UNIQUE(tenant_id, name)
);
CREATE TABLE ai_documents (
  id TEXT PRIMARY KEY, kb_id TEXT NOT NULL REFERENCES ai_knowledgebases ON DELETE CASCADE,
  tenant_id TEXT NOT NULL, name TEXT, mime TEXT, status TEXT, object_key TEXT,
  chunk_count INT DEFAULT 0, metadata JSONB, created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX ON ai_documents (tenant_id, kb_id);
CREATE TABLE ai_chunks (
  id TEXT PRIMARY KEY, kb_id TEXT NOT NULL, doc_id TEXT NOT NULL, tenant_id TEXT NOT NULL,
  seq INT, content TEXT, metadata JSONB,  -- 向量存 qdrant，point_id = id
  created_at TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX ON ai_chunks (tenant_id, kb_id, doc_id);
-- tsvector 列预留（BM25 后续）：ALTER TABLE ai_chunks ADD COLUMN tsv tsvector;
```

**新建** `internal/ai/knowledgebase/pg/store.go`：PG 实现 Repository（参照 `internal/dataservice/pg/store.go` 模式，复用 `internal/storage/pg/helpers.go`），全参数化 + tenant 过滤。

**测试**：`//go:build integration`（PAAS_TEST_PG_URL 驱动，resetSchema）。

---

### T6: handler + 路由 + OpenAPI
**新建** `internal/ai/knowledgebase/handler.go`：
- `GET/POST /api/knowledgebases`（列表/创建）
- `GET/PUT/DELETE /api/knowledgebases/{id}`
- `POST /api/knowledgebases/{id}/documents`（multipart 上传 -> 存 minio -> 异步解析+切片+embedding+入 qdrant/PG -> 状态机 parsing->indexed/failed）
- `GET /api/knowledgebases/{id}/documents` + `GET /{docId}`（状态）+ `DELETE /{docId}`（删 minio 对象 + qdrant points + PG chunks）
- `POST /v1/knowledgebases/{id}/retrieve`（query -> 检索返 chunks+score）
- 权限：新增 `kb:read/write`（admin/dev 读写，viewer 只读）+ `prod:write`（生产创建/删除 KB + 上传文档受 EnvTypeResolver，KB 不绑环境则不接 prod:write，MVP KB 不带 EnvID）。
- 文档处理异步：`CreateDocument` 后 goroutine 跑 parse+chunk+embed+index，状态写 PG（参照 devops BuildRun 异步模式 + baseCtx 进程退出 cancel）。

**改** `cmd/core/main.go`：注册路由（`mux.Handle("/api/knowledgebases", ...)` + `/v1/knowledgebases/`）+ OpenAPI `reg.Register` 登记 + handler 装配（Authorize + EnvTypeResolver 注入）。

**测试**：handler_test（memory + mock VectorStore/BlobStore/Embedder）。

---

### T7: 桥接装配（cmd/core）
**新建** `cmd/core/ai_bridges.go`：
- `qdrantVectorStore`：实现 KB `VectorStore`，用 `dataservice.Repository.Get` 拿 vector 实例 connection（host/port/api_key）+ `github.com/qdrant/go-client` 连。EnsureCollection 建 collection（dim 从 embedding 模型配置取，text-embedding-v4=1024）。
- `minioBlobStore`：实现 KB `BlobStore`，用 dataservice.Repository.Get 拿 storage connection + `github.com/minio/minio-go/v7` 连。bucket = `kb-{tenantID}`（EnsureBucket）。
- `maasEmbedder`：实现 KB `Embedder`，从 MaaS Repository 拿 KB.EmbeddingModel -> `BuildProvider` -> `.(provider.Embedder)`。

**改** `cmd/core/persistence.go`：
- `Stores` struct 加 `KnowledgeBase knowledgebase.Repository`。
- `buildAllStores` 两路径加 KB store（PG: `aipg.NewStore(db)`；memory: `aimemory.NewStore()`）。
- seed：`seedPGAllIfEmpty` 加 KB（MVP 无 demo seed，生产空目录）。

**改** `cmd/core/main.go`：装配 Retriever（repo+vs+blob+embedder）+ KB handler 注入。

**依赖**：`go get github.com/qdrant/go-client@latest` + `github.com/minio/minio-go/v7@latest`（确认 Apache 2.0）。

**测试**：桥接单元测试用 mock dataservice.Repository（不连真实 qdrant/minio，集群 e2e 才连）。

---

### T8: kbBindingInjector
**新建** `cmd/core/kb_binding_injector.go`（或合并进 `binding_injector.go`）：
- 参照 `dsBindingInjector`：`kbRepo knowledgebase.Repository` + `cfgRepo appconfig.Repository` + `idb identity.Repository`。
- `OnBind`：resolve KB by name/ID -> 注入 appconfig（TypeSecret）`KB_API_BASE`（core `/v1`）+ `KB_ID` + `KB_API_KEY`（应用级 Key 归因到应用，复用 dsBindingInjector.bindModel 的 Key 创建模式）。
- `OnUnbind`：删 appconfig 的 KB_* key。
- 装配 `cmd/core/main.go:385`：合并进 `dsBindingInjector`（加 kbRepo 字段 + OnBind 加 `case "knowledgebase"` 分支）或复合 injector 链。**推荐合并**（DRY，单一 injector 分支）。

**测试**：OnBind/OnUnbind 注入 key 正确 + 解绑清理。

---

### T9: 前端 console-user
**改** `frontend/console-user/src/router` + `views`：
- 资源中心加「知识库」路由 `/resources/knowledgebase`（KB 列表，复用 6 路由共用模式）。
- KB 列表（`fetchJSON` 对接 `/api/knowledgebases`）+ 创建弹窗（name + 选 vector/storage datasource 引用下拉 + embedding 模型下拉从 `/v1/models` Caps 含 embedding 过滤）。
- KB 详情 `/resources/knowledgebase/:id`：文档列表 + 上传（el-upload）+ 文档状态轮询（parsing->indexed）+ 检索测试面板（输入 query -> `/v1/knowledgebases/{id}/retrieve` -> 展示 chunks+score）。
- 应用详情绑定 tab 加「知识库」绑定（复用 binding UI）。

**测试**：`pnpm build`（vue-tsc + vite）。

---

### T10: 集群部署 + e2e
- `go get` 引入 qdrant/minio SDK（确认 license）。
- `./scripts/deploy-k8s.sh` 部署。
- e2e：建 dataservice vector(qdrant) + storage(minio) 实例 -> 建 KB 引用 -> 上传 text/md 文档 -> 轮询 indexed -> retrieve 验证返 chunks。
- 验证 adminGuard（`/api/admin/knowledgebases` 留后续）/ 鉴权 / 计量。

## 验证

- `go build ./...` + `go test ./internal/ai/... ./internal/maas/... ./pkg/provider/... ./cmd/core/ -race -count=1` 全绿。
- `make test-pg`（0011 migration up/down 幂等）。
- `pnpm build` 三套前端。
- k8s e2e：KB 全链路（建实例->建KB->上传->检索）。

## 留后续（spec 对齐）

- PDF/Office 文档解析（调供应商 API 或内置库）。
- 检索质量：BM25 混合（PG tsvector）+ rerank（供应商 API）+ 查询改写（LLM）--接口已预留。
- KB 绑定环境（EnvID + prod:write）--MVP KB 不绑环境。
- admin 跨租户总览（L1 详情 + L2 运维）。
- 文档增量更新 / 版本管理 / 权限分类。
- chunk 重新 embedding（embedding 模型变更后重索引）。
- 平台预建共享 dataservice vector/storage 实例（省去用户自建）。

## 不做项（YAGNI）

- 不自建 qdrant/minio Pod（复用 dataservice）。
- 不做可视化编排 / 复杂检索策略 UI。
- 不做 PDF/Office 全格式解析（MVP text/md/html + 接口预留）。
- 不做 rerank/BM25 实现（接口预留，分阶段填充）。
- 不做 KB 自动创建 dataservice 实例（显式引用，避免隐式资源）。
