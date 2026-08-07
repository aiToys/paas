package knowledgebase

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/pkg/tenant"
)

// 粗粒度权限标识（与 identity.BuiltinRoles 对齐）。
const (
	PermKBRead  = "kb:read"
	PermKBWrite = "kb:write"
)

const (
	maxUploadSize  = 32 << 20 // 文档上传上限 32MB
	processTimeout = 5 * time.Minute // 文档解析+embedding+入库 超时
)

// Handler 暴露知识库 REST API。
//
// 路由：
//
//	GET    /api/knowledgebases                列表
//	POST   /api/knowledgebases                创建
//	GET    /api/knowledgebases/{id}           详情
//	PUT    /api/knowledgebases/{id}           更新
//	DELETE /api/knowledgebases/{id}           删除（级联清文档+chunks+向量）
//	GET    /api/knowledgebases/{id}/documents 文档列表
//	POST   /api/knowledgebases/{id}/documents 上传文档（multipart，异步解析+索引）
//	GET    /api/knowledgebases/{id}/documents/{docId}  文档状态
//	DELETE /api/knowledgebases/{id}/documents/{docId}  删文档（清 chunks+向量+原文）
//	POST   /api/knowledgebases/{id}/retrieve  检索（query -> chunks+score）
type Handler struct {
	repo      Repository
	retriever *Retriever
	blob      BlobStore
	baseCtx   context.Context // 异步处理 goroutine 派生之，进程退出 cancel
	Authorize func(r *http.Request, perm string) bool
}

// NewHandler 创建 KB handler。retriever/blob 必传（文档上传+检索用）；baseCtx 经 WithBaseCtx 注入。
func NewHandler(repo Repository, retriever *Retriever, blob BlobStore) *Handler {
	return &Handler{repo: repo, retriever: retriever, blob: blob}
}

// WithBaseCtx 注入进程级 ctx，异步文档处理 goroutine 派生之（进程退出 cancel 构建，防残留）。
func (h *Handler) WithBaseCtx(ctx context.Context) *Handler { h.baseCtx = ctx; return h }

func (h *Handler) allow(w http.ResponseWriter, r *http.Request, perm string) bool {
	if h.Authorize == nil || h.Authorize(r, perm) {
		return true
	}
	httputil.WriteError(w, http.StatusForbidden, "forbidden: missing "+perm)
	return false
}

// ServeHTTP 按路径分发。
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == "/api/knowledgebases":
		h.serveCollection(w, r)
	case strings.HasPrefix(path, "/api/knowledgebases/"):
		h.serveItem(w, r)
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermKBRead) {
			return
		}
		list, err := h.repo.List(r.Context())
		if err != nil {
			httputil.WriteInternalError(w, err)
			return
		}
		httputil.WriteData(w, list)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermKBWrite) {
			return
		}
		var kb KnowledgeBase
		if err := json.NewDecoder(r.Body).Decode(&kb); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		saved, err := h.repo.Create(r.Context(), kb)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteDataCreated(w, saved)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) serveItem(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimRight(r.URL.Path, "/")
	rest := strings.TrimPrefix(path, "/api/knowledgebases/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		httputil.WriteError(w, http.StatusNotFound, "not found")
		return
	}
	id := parts[0]
	switch {
	case len(parts) == 1:
		h.serveKB(w, r, id)
	case len(parts) == 2 && parts[1] == "documents":
		h.serveDocuments(w, r, id)
	case len(parts) == 2 && parts[1] == "retrieve":
		h.serveRetrieve(w, r, id)
	case len(parts) == 3 && parts[1] == "documents":
		h.serveDocument(w, r, id, parts[2])
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

func (h *Handler) serveKB(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermKBRead) {
			return
		}
		kb, err := h.repo.Get(r.Context(), id)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, kb)
	case http.MethodPut:
		if !h.allow(w, r, PermKBWrite) {
			return
		}
		var kb KnowledgeBase
		if err := json.NewDecoder(r.Body).Decode(&kb); err != nil {
			httputil.WriteError(w, http.StatusBadRequest, "invalid body")
			return
		}
		kb.ID = id
		saved, err := h.repo.Update(r.Context(), kb)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, saved)
	case http.MethodDelete:
		if !h.allow(w, r, PermKBWrite) {
			return
		}
		// 删 KB 前：清向量 collection（需先取 KB 拿 CollectionName）。
		kb, err := h.repo.Get(r.Context(), id)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		if h.retriever != nil {
			_ = h.retriever.vs.DeleteCollection(r.Context(), kb.CollectionName())
		}
		if err := h.repo.Delete(r.Context(), id); err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, map[string]string{"deleted": id})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) serveDocuments(w http.ResponseWriter, r *http.Request, kbID string) {
	if r.Method == http.MethodGet {
		if !h.allow(w, r, PermKBRead) {
			return
		}
		docs, err := h.repo.ListDocuments(r.Context(), kbID)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, docs)
		return
	}
	if r.Method == http.MethodPost {
		if !h.allow(w, r, PermKBWrite) {
			return
		}
		h.serveDocumentUpload(w, r, kbID)
		return
	}
	httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
}

// serveDocumentUpload 处理 multipart 文档上传：存 minio -> 建 doc 记录(parsing) -> 异步解析+索引。
func (h *Handler) serveDocumentUpload(w http.ResponseWriter, r *http.Request, kbID string) {
	if h.blob == nil || h.retriever == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "知识库后端未配置（向量库/对象存储/embedding 未注入）")
		return
	}
	kb, err := h.repo.Get(r.Context(), kbID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "解析上传失败（上限 32MB）: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "missing file")
		return
	}
	defer file.Close()

	mime := header.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/octet-stream"
	}
	// 先建 doc 记录拿 ID（ObjectKey 用 docID）
	doc := Document{KBID: kbID, Name: header.Filename, MIME: mime, Status: DocStatusParsing}
	doc, err = h.repo.CreateDocument(r.Context(), doc)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	doc.ObjectKey = fmt.Sprintf("%s/%s/%s", kbID, doc.ID, header.Filename)
	// 回写 ObjectKey（CreateDocument 时 doc.ID 未生成，object_key 为空；不回写则删文档时 minio 原文残留泄漏）。
	_ = h.repo.UpdateDocumentObjectKey(r.Context(), doc.ID, doc.ObjectKey)
	// 存原文到 minio
	bucket := kb.BucketName()
	if err := h.blob.EnsureBucket(r.Context(), bucket); err != nil {
		_ = h.repo.UpdateDocumentStatus(r.Context(), doc.ID, DocStatusFailed, 0, "bucket 初始化失败")
		httputil.WriteInternalError(w, err)
		return
	}
	if err := h.blob.PutObject(r.Context(), bucket, doc.ObjectKey, file, mime); err != nil {
		_ = h.repo.UpdateDocumentStatus(r.Context(), doc.ID, DocStatusFailed, 0, "原文存储失败")
		httputil.WriteInternalError(w, err)
		return
	}
	// 异步解析+索引（goroutine 派生 baseCtx，进程退出 cancel）
	go h.processDocument(kb, doc)
	httputil.WriteDataCreated(w, doc)
}

// processDocument 异步：读原文 -> 解析 -> 切片 -> embedding -> 入向量库+PG。
func (h *Handler) processDocument(kb KnowledgeBase, doc Document) {
	parent := h.baseCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, processTimeout)
	defer cancel()
	// 异步 ctx 注入租户（baseCtx 无 tenant，repo 操作需租户过滤）
	ctx = tenant.WithTenant(ctx, kb.TenantID)

	bucket := kb.BucketName()
	rc, err := h.blob.GetObject(ctx, bucket, doc.ObjectKey)
	if err != nil {
		_ = h.repo.UpdateDocumentStatus(context.WithoutCancel(ctx), doc.ID, DocStatusFailed, 0, "读取原文失败: "+err.Error())
		return
	}
	defer rc.Close()
	content, err := Parse(doc.MIME, rc)
	if err != nil {
		_ = h.repo.UpdateDocumentStatus(context.WithoutCancel(ctx), doc.ID, DocStatusFailed, 0, "解析失败: "+err.Error())
		return
	}
	_ = h.retriever.IndexDocument(ctx, kb, doc, content) // 内部更新状态 indexed/failed（最终状态用 WithoutCancel）
}

func (h *Handler) serveDocument(w http.ResponseWriter, r *http.Request, kbID, docID string) {
	switch r.Method {
	case http.MethodGet:
		if !h.allow(w, r, PermKBRead) {
			return
		}
		doc, err := h.repo.GetDocument(r.Context(), docID)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		httputil.WriteData(w, doc)
	case http.MethodDelete:
		if !h.allow(w, r, PermKBWrite) {
			return
		}
		kb, err := h.repo.Get(r.Context(), kbID)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		doc, err := h.repo.GetDocument(r.Context(), docID)
		if err != nil {
			h.writeErr(w, err)
			return
		}
		// 校验 doc 归属路径 KB（防本租户内跨 KB 删他人文档，统一 not found 不泄漏）。
		if doc.KBID != kbID {
			httputil.WriteError(w, http.StatusNotFound, "文档不存在")
			return
		}
		// 清向量 points（需先取 chunks 拿 point_ids）
		if h.retriever != nil {
			_ = h.retriever.DeleteDocumentVectors(r.Context(), kb, docID)
		}
		// 清 PG chunks（FK CASCADE 删 doc 时本会清，但先清更稳妥 + 与向量一致）
		_ = h.repo.DeleteChunksByDoc(r.Context(), kbID, docID)
		// 删 doc 记录（CASCADE 清 chunks）
		if err := h.repo.DeleteDocument(r.Context(), docID); err != nil {
			h.writeErr(w, err)
			return
		}
		// 删 minio 原文
		if h.blob != nil {
			_ = h.blob.DeleteObject(r.Context(), kb.BucketName(), doc.ObjectKey)
		}
		httputil.WriteData(w, map[string]string{"deleted": docID})
	default:
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (h *Handler) serveRetrieve(w http.ResponseWriter, r *http.Request, kbID string) {
	if r.Method != http.MethodPost {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !h.allow(w, r, PermKBRead) {
		return
	}
	if h.retriever == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "检索后端未配置")
		return
	}
	var req struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Query == "" {
		httputil.WriteError(w, http.StatusBadRequest, "query 不能为空")
		return
	}
	// TopK 由 KB.RetrieverConfig.TopK 决定（默认 5）；运行时覆盖留后续。
	hits, err := h.retriever.Retrieve(r.Context(), kbID, req.Query)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	httputil.WriteData(w, hits)
}

// writeErr 映射领域 sentinel 到 HTTP 状态（与 dataservice/maas 同款）。
func (h *Handler) writeErr(w http.ResponseWriter, err error) {
	switch {
	case err == ErrKBNotFound, err == ErrDocNotFound:
		httputil.WriteError(w, http.StatusNotFound, err.Error())
	case err == ErrKBExists:
		httputil.WriteError(w, http.StatusConflict, err.Error())
	case isFieldErr(err):
		httputil.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		httputil.WriteServiceError(w, http.StatusInternalServerError, err)
	}
}

func isFieldErr(err error) bool {
	_, ok := err.(fieldErr)
	return ok
}
