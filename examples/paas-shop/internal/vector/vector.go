// Package vector 提供 paas-shop 的 Qdrant 语义检索能力（平台 vector 数据服务绑定注入 QDRANT_URL/QDRANT_API_KEY）。
//
// 纯 net/http 调 Qdrant REST（无 SDK 依赖，与项目零重依赖风格一致）：
//   - Embed：经平台 gateway /v1/embeddings（airouter embedding 模型）把文本转向量
//   - EnsureCollection：建 products collection（cosine + 向量维度自动适配）
//   - UpsertProduct：商品向量同步（写路径调用）
//   - Search：语义搜索（"便宜好用的手机"也能命中，SQL ILIKE 做不到）
//
// 降级链约定：QDRANT_URL 或 PAAS_LLM_* 未配 -> degraded stub（Search 返空），
// product 搜索回落 meilisearch -> SQL ILIKE（见 product 搜索 handler）。
package vector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

const collection = "products"

// Client 封装 Qdrant + embedding gateway；degraded=true 时全部 no-op。
type Client struct {
	qdrantURL  string
	apiKey     string
	embedURL   string // 平台 gateway /v1/embeddings
	embedKey   string
	embedModel string
	dim        int // 向量维度（首次 embed 后锁定）
	http       *http.Client
	degraded   bool
}

// New 从 env 构造：QDRANT_URL/QDRANT_API_KEY（vector 绑定注入）+ PAAS_LLM_BASE_URL/PAAS_LLM_API_KEY（模型绑定注入）。
// 任一缺失 -> degraded（示例最小部署不崩，与 natspub 同款约定）。
func New() *Client {
	c := &Client{
		qdrantURL:  os.Getenv("QDRANT_URL"),
		apiKey:     os.Getenv("QDRANT_API_KEY"),
		embedModel: envOr("PAAS_EMBED_MODEL", "text-embedding-v4"),
		http:       &http.Client{Timeout: 10 * time.Second},
	}
	base := os.Getenv("PAAS_LLM_BASE_URL")
	if base != "" {
		c.embedURL = base + "/embeddings"
		c.embedKey = os.Getenv("PAAS_LLM_API_KEY")
	}
	if c.qdrantURL == "" || c.embedURL == "" {
		c.degraded = true
		slog.Warn("QDRANT_URL 或 PAAS_LLM_* 未设置，语义搜索降级关闭（回落 meili/SQL）")
	}
	return c
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Available 是否可用（degraded 返 false，调用方走降级链）。
func (c *Client) Available() bool { return c != nil && !c.degraded }

// Embed 文本转向量（平台 gateway OpenAI 兼容 /v1/embeddings）。
func (c *Client) Embed(ctx context.Context, text string) ([]float64, error) {
	body, _ := json.Marshal(map[string]any{"model": c.embedModel, "input": text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.embedURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.embedKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("embeddings %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("embeddings 返回空")
	}
	if c.dim == 0 {
		c.dim = len(out.Data[0].Embedding)
	}
	return out.Data[0].Embedding, nil
}

// qdrant 调用封装（x-api-key 鉴权，与平台 connection.go 约定一致）。
func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.qdrantURL+path, rd)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("api-key", c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return b, resp.StatusCode, nil
}

// EnsureCollection 建 products collection（幂等：已存在忽略 409）。
func (c *Client) EnsureCollection(ctx context.Context) error {
	if c.degraded {
		return nil
	}
	// 用一句占位文本 embed 拿真实维度（airouter 各 embedding 模型维度不同，不硬编码）。
	if _, err := c.Embed(ctx, "dimension probe"); err != nil {
		return fmt.Errorf("维度探测失败: %w", err)
	}
	_, status, err := c.do(ctx, http.MethodPut, "/collections/"+collection, map[string]any{
		"vectors": map[string]any{"size": c.dim, "distance": "Cosine"},
	})
	if err != nil {
		return err
	}
	if status >= 300 && status != http.StatusConflict { // 409=已存在
		return fmt.Errorf("建 collection %d", status)
	}
	slog.Info("qdrant collection 就绪", "collection", collection, "dim", c.dim)
	return nil
}

// UpsertProduct 商品向量入库（id=商品 ID，payload 存原文供排查）。
func (c *Client) UpsertProduct(ctx context.Context, id int, name, description, category string) error {
	if c.degraded {
		return nil
	}
	vec, err := c.Embed(ctx, name+" "+description+" "+category)
	if err != nil {
		return err
	}
	body := map[string]any{
		"points": []map[string]any{{
			"id":      id,
			"vector":  vec,
			"payload": map[string]string{"name": name, "description": description, "category": category},
		}},
	}
	_, status, err := c.do(ctx, http.MethodPut, "/collections/"+collection+"/points", body)
	if err != nil {
		return err
	}
	if status >= 300 {
		return fmt.Errorf("upsert %d", status)
	}
	return nil
}

// Scored 命中结果（ID + 相似度）。
type Scored struct {
	ID    int     `json:"id"`
	Score float64 `json:"score"`
}

// Search 语义搜索：query 向量化后 nearest 查询。
func (c *Client) Search(ctx context.Context, query string, limit int) ([]Scored, error) {
	if c.degraded {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	vec, err := c.Embed(ctx, query)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"vector": vec, "limit": limit, "with_payload": false, "with_vector": false,
	}
	b, status, err := c.do(ctx, http.MethodPost, "/collections/"+collection+"/points/search", body)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("search %d", status)
	}
	var out struct {
		Result []struct {
			ID    int     `json:"id"`
			Score float64 `json:"score"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	res := make([]Scored, 0, len(out.Result))
	for _, r := range out.Result {
		res = append(res, Scored{ID: r.ID, Score: r.Score})
	}
	return res, nil
}
