package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/ai/agent"
	"github.com/aitoys/paas/internal/ai/guardrail"
	"github.com/aitoys/paas/internal/ai/knowledgebase"
	"github.com/aitoys/paas/internal/ai/tool"
	"github.com/aitoys/paas/internal/ai/tool/mcp"
	"github.com/aitoys/paas/internal/core/gateway"
	"github.com/aitoys/paas/internal/httputil"
	"github.com/aitoys/paas/internal/maas"
	"github.com/aitoys/paas/pkg/provider"

	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ============================================================
// qdrantVectorStore：qdrant REST API（net/http，不引 SDK，减少依赖）
// ============================================================
//
// point id 用 uint64（FNV-1a hash of chunkID），payload 存 chunkId 便于回查。
// qdrant REST: /collections/{name} + /points + /points/search + /points/delete。

type qdrantVectorStore struct {
	baseURL string // http://host:6333
	apiKey  string
	client  *http.Client
}

func newQdrantVectorStore(url, apiKey string) *qdrantVectorStore {
	return &qdrantVectorStore{
		baseURL: url, apiKey: apiKey,
		// httputil.NewClient 内置 CheckRedirect=ErrUseLastResponse（防 SSRF，与平台出站 client 一致）。
		client: httputil.NewClient(30 * time.Second),
	}
}

func (s *qdrantVectorStore) do(ctx context.Context, method, path string, body any) (io.ReadCloser, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.apiKey != "" {
		// qdrant REST 鉴权 header 是 `api-key`（非 x-api-key），见 qdrant security 文档。
		req.Header.Set("api-key", s.apiKey)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: qdrant 请求失败: %v", provider.ErrUpstreamUnavailable, err)
	}
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("qdrant %d: %s", resp.StatusCode, string(b))
	}
	return resp.Body, nil
}

func fnv64(s string) uint64 {
	const offset64, prime64 = 14695981039346656037, 1099511628211
	h := uint64(offset64)
	for _, b := range []byte(s) {
		h ^= uint64(b)
		h *= prime64
	}
	return h
}

func (s *qdrantVectorStore) EnsureCollection(ctx context.Context, collection string, dim int) error {
	// 先 GET 判存在（幂等，避免重复 PUT 报 409）
	if rc, err := s.do(ctx, "GET", "/collections/"+collection, nil); err == nil {
		_ = rc.Close()
		return nil
	}
	body := map[string]any{"vectors": map[string]any{"size": dim, "distance": "Cosine"}}
	rc, err := s.do(ctx, "PUT", "/collections/"+collection, body)
	if err != nil {
		return err
	}
	_ = rc.Close()
	return nil
}

type qdrantPoint struct {
	ID      uint64            `json:"id"`
	Vector  []float32         `json:"vector"`
	Payload map[string]string `json:"payload,omitempty"`
}

func (s *qdrantVectorStore) UpsertVectors(ctx context.Context, collection string, points []knowledgebase.VectorPoint) error {
	qp := make([]qdrantPoint, len(points))
	for i, p := range points {
		qp[i] = qdrantPoint{ID: fnv64(p.ID), Vector: p.Vector, Payload: map[string]string{"chunkId": p.ID}}
	}
	rc, err := s.do(ctx, "PUT", "/collections/"+collection+"/points?wait=true", map[string]any{"points": qp})
	if err != nil {
		return err
	}
	_ = rc.Close()
	return nil
}

type qdrantSearchResult struct {
	Result []struct {
		ID      uint64  `json:"id"`
		Score   float32 `json:"score"`
		Payload struct {
			ChunkID string `json:"chunkId"`
		} `json:"payload"`
	} `json:"result"`
}

func (s *qdrantVectorStore) Search(ctx context.Context, collection string, query []float32, topK int) ([]knowledgebase.VectorHit, error) {
	body := map[string]any{"vector": query, "limit": topK, "with_payload": true}
	rc, err := s.do(ctx, "POST", "/collections/"+collection+"/points/search", body)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	var res qdrantSearchResult
	if err := json.NewDecoder(rc).Decode(&res); err != nil {
		return nil, fmt.Errorf("解析 qdrant 响应失败: %w", err)
	}
	out := make([]knowledgebase.VectorHit, 0, len(res.Result))
	for _, r := range res.Result {
		out = append(out, knowledgebase.VectorHit{ID: r.Payload.ChunkID, Score: r.Score})
	}
	return out, nil
}

func (s *qdrantVectorStore) DeletePoints(ctx context.Context, collection string, ids []string) error {
	pts := make([]uint64, len(ids))
	for i, id := range ids {
		pts[i] = fnv64(id)
	}
	rc, err := s.do(ctx, "POST", "/collections/"+collection+"/points/delete?wait=true", map[string]any{"points": pts})
	if err != nil {
		return err
	}
	_ = rc.Close()
	return nil
}

func (s *qdrantVectorStore) DeleteCollection(ctx context.Context, collection string) error {
	rc, err := s.do(ctx, "DELETE", "/collections/"+collection, nil)
	if err != nil {
		return err
	}
	_ = rc.Close()
	return nil
}

// ============================================================
// minioBlobStore：minio-go v7 SDK（S3 兼容，签名复杂用 SDK）
// ============================================================

type minioBlobStore struct {
	client   *minio.Client
	endpoint string
}

func newMinioBlobStore(endpoint, accessKey, secretKey string) (*minioBlobStore, error) {
	// endpoint 形如 host:9000（minio SDK 自动判 https，内网 http 传 Secure:false）
	cli, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: false,
	})
	if err != nil {
		return nil, err
	}
	return &minioBlobStore{client: cli, endpoint: endpoint}, nil
}

func (s *minioBlobStore) EnsureBucket(ctx context.Context, bucket string) error {
	exists, err := s.client.BucketExists(ctx, bucket)
	if err != nil {
		return fmt.Errorf("%w: minio 检查 bucket 失败: %v", provider.ErrUpstreamUnavailable, err)
	}
	if exists {
		return nil
	}
	return s.client.MakeBucket(ctx, bucket, minio.MakeBucketOptions{})
}

func (s *minioBlobStore) PutObject(ctx context.Context, bucket, key string, r io.Reader, contentType string) error {
	_, err := s.client.PutObject(ctx, bucket, key, r, -1, minio.PutObjectOptions{ContentType: contentType})
	return err
}

func (s *minioBlobStore) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func (s *minioBlobStore) DeleteObject(ctx context.Context, bucket, key string) error {
	return s.client.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
}

// ============================================================
// maasEmbedderFactory：按 model ID 从 MaaS catalog 解析 Embedder（缓存）
// ============================================================

type maasEmbedderFactory struct {
	repo     maas.Repository
	resolver provider.CredentialResolver
	cache    sync.Map // modelID -> provider.Embedder
}

func newMaasEmbedderFactory(repo maas.Repository, resolver provider.CredentialResolver) *maasEmbedderFactory {
	return &maasEmbedderFactory{repo: repo, resolver: resolver}
}

func (f *maasEmbedderFactory) EmbedderFor(ctx context.Context, modelID string) (provider.Embedder, error) {
	if v, ok := f.cache.Load(modelID); ok {
		return v.(provider.Embedder), nil
	}
	m, err := f.repo.GetModel(ctx, modelID)
	if err != nil {
		return nil, fmt.Errorf("embedding 模型不存在: %s", modelID)
	}
	if len(m.Channels) == 0 {
		return nil, fmt.Errorf("模型 %s 无通道", modelID)
	}
	ch := m.Channels[0]
	p := maas.BuildProvider(ch, f.resolver)
	e, ok := p.(provider.Embedder)
	if !ok {
		return nil, fmt.Errorf("模型 %s 不支持 embedding", modelID)
	}
	f.cache.Store(modelID, e)
	return e, nil
}

// agentDispatcherAdapter 把 agent.Runtime 适配为 gateway.AgentDispatcher。
//
// gateway 包不 import agent（避免循环依赖），由 cmd/core 注入此适配器。
// model 形如 "agent:<id>"——剥前缀得 agentID，调 runtime.ServeSSE 以 OpenAI 兼容 SSE 输出。
type agentDispatcherAdapter struct{ rt *agent.Runtime }

const agentModelPrefix = "agent:"

func (a agentDispatcherAdapter) Match(model string) bool {
	return len(model) > len(agentModelPrefix) && strings.HasPrefix(model, agentModelPrefix)
}

func (a agentDispatcherAdapter) ServeSSE(w http.ResponseWriter, r *http.Request, model string, msgs []provider.Message) error {
	agentID := strings.TrimPrefix(model, agentModelPrefix)
	// 预检：agent 存在 + 启用 + 输入护栏（SSE 写头前，让 gateway 能返干净 4xx 而非空 SSE 流）。
	// Run 内部会再检一次（覆盖直接调 runtime 的路径），双重保险。
	// 映射 agent/guardrail 错误到 gateway sentinel（避免 gateway import agent 破解耦）。
	if err := a.rt.CheckInput(r.Context(), agentID, msgs); err != nil {
		if errors.Is(err, guardrail.ErrBlocked) {
			return gateway.ErrAgentBlocked
		}
		if errors.Is(err, agent.ErrAgentNotFound) {
			return gateway.ErrAgentNotFound
		}
		return err
	}
	return a.rt.ServeSSE(w, r.Context(), agentID, msgs)
}

// ServeSSEConv 带会话 ID（多轮记忆）的执行。
func (a agentDispatcherAdapter) ServeSSEConv(w http.ResponseWriter, r *http.Request, model, conversationID string, msgs []provider.Message) error {
	agentID := strings.TrimPrefix(model, agentModelPrefix)
	if err := a.rt.CheckInput(r.Context(), agentID, msgs); err != nil {
		if errors.Is(err, guardrail.ErrBlocked) {
			return gateway.ErrAgentBlocked
		}
		if errors.Is(err, agent.ErrAgentNotFound) {
			return gateway.ErrAgentNotFound
		}
		return err
	}
	return a.rt.ServeSSEConv(w, r.Context(), agentID, conversationID, msgs)
}

// —— 工作流引擎桥接（workflow 依赖倒置接口 -> agent.Runtime / mcp client）——

// workflowAgentRunner 把 agent.Runtime 适配为 workflow.AgentRunner：
// 收集全部 content chunk 拼接为最终文本（工作流节点消费完整输出，非流式透传）。
// 计量经 Runtime.MeterFn 统一覆盖（agent run / eval / workflow 三路径同源，防绕过 billing 配额）。
type workflowAgentRunner struct{ rt *agent.Runtime }

func (w workflowAgentRunner) RunNode(ctx context.Context, agentID, prompt string) (string, error) {
	var sb strings.Builder
	err := w.rt.Run(ctx, agentID, []provider.Message{{Role: "user", Content: prompt}}, func(c provider.Chunk) {
		sb.WriteString(c.Content)
	})
	return sb.String(), err
}

// workflowToolRunner 把 tool.Repository + mcp client 适配为 workflow.ToolRunner。
// 仅支持 MCP 工具（与 tool handler invoke 同款语义）；工具不存在/类型不符返错。
type workflowToolRunner struct {
	repo tool.Repository
}

func (w workflowToolRunner) Invoke(ctx context.Context, toolID, toolName string, args map[string]string) (string, error) {
	t, err := w.repo.Get(ctx, toolID)
	if err != nil {
		return "", err
	}
	if t.Type != tool.TypeMCP {
		return "", fmt.Errorf("工作流工具节点暂仅支持 mcp 类型（%s 是 %s）", toolID, t.Type)
	}
	margs := make(map[string]any, len(args))
	for k, v := range args {
		margs[k] = v
	}
	cli := mcp.GetClient(t.Config[tool.CfgMCPServerURL], t.Config[tool.CfgMCPAPIKey])
	res, err := cli.Invoke(ctx, toolName, margs)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(res)
	return string(b), err
}
