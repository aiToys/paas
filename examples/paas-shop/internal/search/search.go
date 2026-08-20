// Package search 提供 paas-shop 的 Meilisearch 全文检索能力（平台 search 数据服务绑定注入 MEILI_URL/MEILI_MASTER_KEY）。
//
// 纯 net/http 调 Meilisearch REST：
//   - EnsureIndex：建 products 索引（幂等）
//   - UpsertProduct / 同步索引（写路径调用）
//   - Search：全文搜索（中文分词 + typo 容错，SQL ILIKE 做不到）
//
// 降级链约定：MEILI_URL 未配 -> degraded stub（Search 返空），product 搜索回落 SQL ILIKE。
package search

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

const indexUID = "products"

// Client 封装 Meilisearch；degraded=true 时全部 no-op。
type Client struct {
	url      string
	key      string
	http     *http.Client
	degraded bool
}

// New 从 env 构造：MEILI_URL/MEILI_MASTER_KEY（search 绑定注入）。缺失 -> degraded。
func New() *Client {
	c := &Client{
		url:  os.Getenv("MEILI_URL"),
		key:  os.Getenv("MEILI_MASTER_KEY"),
		http: &http.Client{Timeout: 10 * time.Second},
	}
	if c.url == "" {
		c.degraded = true
		slog.Warn("MEILI_URL 未设置，全文搜索降级关闭（回落 SQL ILIKE）")
	}
	return c
}

// Available 是否可用（degraded 返 false，调用方走降级链）。
func (c *Client) Available() bool { return c != nil && !c.degraded }

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url+path, rd)
	if err != nil {
		return nil, 0, err
	}
	// Meilisearch 鉴权：Authorization: Bearer <master_key>（与平台 connection.go 约定一致）。
	req.Header.Set("Authorization", "Bearer "+c.key)
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

// EnsureIndex 建 products 索引（主键 id，幂等：已存在忽略 400 index_already_exists）。
func (c *Client) EnsureIndex(ctx context.Context) error {
	if c.degraded {
		return nil
	}
	_, status, err := c.do(ctx, http.MethodPost, "/indexes", map[string]any{
		"uid": indexUID, "primaryKey": "id",
	})
	if err != nil {
		return err
	}
	if status >= 300 && status != 400 && status != 409 { // 400/409=已存在
		return fmt.Errorf("建索引 %d", status)
	}
	// 可搜索属性限 name/description/category（防 id/price 等数值字段干扰相关性）。
	_, _, err = c.do(ctx, http.MethodPut, "/indexes/"+indexUID+"/settings/searchable-attributes",
		[]string{"name", "description", "category"})
	if err != nil {
		return fmt.Errorf("设 searchable 属性: %w", err)
	}
	slog.Info("meilisearch 索引就绪", "index", indexUID)
	return nil
}

// Doc 索引文档（与 product.Product 字段对齐，search 包不依赖 product 防循环）。
type Doc struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
}

// UpsertProduct 同步单条商品到索引（meili upsert 语义按主键覆盖）。
func (c *Client) UpsertProduct(ctx context.Context, d Doc) error {
	if c.degraded {
		return nil
	}
	_, status, err := c.do(ctx, http.MethodPost, "/indexes/"+indexUID+"/documents", []Doc{d})
	if err != nil {
		return err
	}
	if status >= 300 && status != 202 { // 202=已接受（meili 异步建索引标准响应）
		return fmt.Errorf("upsert %d", status)
	}
	return nil
}

// Hit 命中结果（meili 返回含原文文档，这里只取 ID 供 PG 回表）。
type Hit struct {
	ID int `json:"id"`
}

// Search 全文搜索（q 空返空；limit 上限 50）。
func (c *Client) Search(ctx context.Context, q string, limit int) ([]Hit, error) {
	if c.degraded || q == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	b, status, err := c.do(ctx, http.MethodPost, "/indexes/"+indexUID+"/search",
		map[string]any{"q": q, "limit": limit})
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("search %d", status)
	}
	var out struct {
		Hits []map[string]any `json:"hits"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	hits := make([]Hit, 0, len(out.Hits))
	for _, h := range out.Hits {
		if id, ok := h["id"].(float64); ok {
			hits = append(hits, Hit{ID: int(id)})
		}
	}
	return hits, nil
}
