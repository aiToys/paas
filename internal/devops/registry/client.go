// Package registry 提供镜像仓库（Docker Registry v2）的 API 客户端。
//
// 设计：PaaS 一站式--镜像库复用 hub.wang.dd:5000（裸 registry:2），平台出 UI。本客户端调
// registry v2 HTTP API（_catalog / tags/list / manifests），不依赖外部 SDK。registry 无 auth
// （内网裸 registry）；若后续启 auth，扩展 Client 加 basicAuth 即可。
//
// 降级：registry 不可达返 ErrRegistryUnavailable，handler 映射 503 提示，不 panic。
//
// 删除：registry:2 默认未启用 delete API（REGISTRY_STORAGE_DELETE_ENABLED=false），
// Delete 方法在未启用时返 405，UI 据此灰显按钮。
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
)

// Sentinel 错误。
var (
	ErrRegistryUnavailable = errors.New("registry 后端不可达")
	ErrRepoNotFound        = errors.New("镜像仓库不存在")
	ErrDeleteDisabled      = errors.New("registry 未启用 delete API")
)

// Client 调 Docker Registry v2 API。baseURL 形如 http://hub.wang.dd:5000。
type Client struct {
	baseURL string
	http    *http.Client
}

// New 创建 registry 客户端。baseURL 形如 http://hub.wang.dd:5000 或 hub.wang.dd:5000（无 scheme
// 时容错补 http://，因 PAAS_REGISTRY env 同时供 builder docker push 用，那里不需 scheme）。
func New(baseURL string) *Client {
	if baseURL != "" && !strings.Contains(baseURL, "://") {
		baseURL = "http://" + baseURL
	}
	return &Client{
		baseURL: baseURL,
		http:    httputil.NewClient(15 * time.Second),
	}
}

// Repository 是 catalog 下的一个镜像仓库（如 paas/paas-core、app-cs）。
type Repository struct {
	Name string `json:"name"` // 仓库名（可能含路径前缀，如 paas/paas-core）
	Tags []Tag  `json:"tags"` // tag 列表（Tags 方法填充）
}

// Tag 是某仓库下的一个 tag + digest。
type Tag struct {
	Name   string `json:"name"`   // 仓库名
	Tag    string `json:"tag"`    // tag 名
	Digest string `json:"digest"` // sha256:...（不可变真源）
}

// Catalog 列所有仓库名（registry v2 /v2/_catalog）。仅返名字，不查 tag（避免 N+1）。
func (c *Client) Catalog(ctx context.Context) ([]string, error) {
	if c.baseURL == "" {
		return nil, ErrRegistryUnavailable
	}
	var resp struct {
		Repositories []string `json:"repositories"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v2/_catalog", nil, &resp); err != nil {
		return nil, err
	}
	return resp.Repositories, nil
}

// Tags 列某仓库的 tag + digest（先 tags/list 拿 tag 名，再 HEAD manifests 取 digest）。
// digest 是不可变真源（与 devops.Image.Digest 一致），用于精确定位镜像。
func (c *Client) Tags(ctx context.Context, name string) ([]Tag, error) {
	if c.baseURL == "" {
		return nil, ErrRegistryUnavailable
	}
	var resp struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v2/"+name+"/tags/list", nil, &resp); err != nil {
		return nil, err
	}
	out := make([]Tag, 0, len(resp.Tags))
	for _, t := range resp.Tags {
		digest, derr := c.headDigest(ctx, name, t)
		tag := Tag{Name: name, Tag: t}
		if derr == nil {
			tag.Digest = digest
		}
		out = append(out, tag)
	}
	return out, nil
}

// headDigest 取某 tag 的 digest（HEAD manifests，读 Docker-Content-Digest 响应头，不下载 body）。
func (c *Client) headDigest(ctx context.Context, name, tag string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, c.baseURL+"/v2/"+name+"/manifests/"+tag, nil)
	if err != nil {
		return "", err
	}
	// v2 manifest schema 2，registry 据此返 Docker-Content-Digest
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("manifests HEAD %s/%s 返回 %d", name, tag, resp.StatusCode)
	}
	return resp.Header.Get("Docker-Content-Digest"), nil
}

// doJSON 发请求 + JSON 编解码 + 错误分类。
func (c *Client) doJSON(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return ErrRegistryUnavailable
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrRegistryUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		if out != nil {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				return fmt.Errorf("解析响应失败: %w", err)
			}
		}
		return nil
	case http.StatusNotFound:
		return ErrRepoNotFound
	default:
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("registry API %s %s 返回 %d: %s", method, path, resp.StatusCode, string(b))
	}
}
