// Package gitea 提供无头 Gitea 后端的 API 客户端。
//
// 设计：PaaS 一站式--Gitea 作为无头代码托管后端（ClusterIP，不暴露 Web UI），所有仓库管理
// 通过本客户端调 Gitea REST API 完成，UI 在 console-user。鉴权用 paas-bot 平台账号 basic auth
// （initContainer 幂等创建），避免 token 生成的复杂性。租户隔离在 PaaS 层（CodeRepo.tenant_id），
// Gitea 单一 paas-bot 用户下建所有 repo，repo 名加租户前缀。
//
// 降级：Gitea 不可达时返 sentinel ErrGiteaUnavailable，handler 层映射 503 提示，不 panic。
package gitea

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/httputil"
)

// Sentinel 错误（参考 maas provider 模式，驱动 handler HTTP 状态映射）。
var (
	ErrGiteaUnavailable = errors.New("gitea 后端不可达")
	ErrRepoNotFound     = errors.New("仓库不存在")
	ErrRepoExists       = errors.New("仓库已存在")
	ErrUnauthorized     = errors.New("gitea 鉴权失败")
)

// Client 调 Gitea REST API（/api/v1）。baseURL 形如 http://gitea.paas.svc.cluster.local:3000。
type Client struct {
	baseURL  string
	username string // paas-bot
	password string
	http     *http.Client
}

// New 创建 Gitea 客户端。baseURL 形如 http://gitea.paas.svc.cluster.local:3000 或
// gitea.paas.svc.cluster.local:3000（无 scheme 时容错补 http://，与 registry.New 一致）。
func New(baseURL, username, password string) *Client {
	if baseURL != "" && !strings.Contains(baseURL, "://") {
		baseURL = "http://" + baseURL
	}
	return &Client{
		baseURL:  baseURL,
		username: username,
		password: password,
		http:     httputil.NewClient(15 * time.Second),
	}
}

// Username 返回平台账号（paas-bot），handler 建仓时填 CodeRepo.GiteaOwner。
func (c *Client) Username() string { return c.username }

// Repo 是 Gitea 仓库的最小子集（CreateRepo 入参 + 返回）。
type Repo struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

// CreateRepoInput 创建仓库入参。
type CreateRepoInput struct {
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch,omitempty"`
	Private       bool   `json:"private"`
	AutoInit      bool   `json:"auto_init"`
}

// Commit 是 Gitea 提交历史项的最小子集。
type Commit struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

// TreeNode 是 git tree 节点（recursive 展平）。
type TreeNode struct {
	Path string `json:"path"` // 相对仓库根的完整路径
	Type string `json:"type"` // "blob"（文件）| "tree"（目录）
	Mode string `json:"mode"`
	Size int64  `json:"size"`
}

// FileContent 是单文件内容（contents API）。
type FileContent struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Content string `json:"content"` // base64 编码
	Encoding string `json:"encoding"`
	Size    int64  `json:"size"`
}

// CreateRepo 在 paas-bot（owner = username）名下创建仓库。已存在返 ErrRepoExists。
func (c *Client) CreateRepo(ctx context.Context, in CreateRepoInput) (Repo, error) {
	if in.DefaultBranch == "" {
		in.DefaultBranch = "main"
	}
	var out Repo
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/user/repos", in, &out); err != nil {
		return Repo{}, err
	}
	return out, nil
}

// GetRepo 查询仓库。不存在返 ErrRepoNotFound。
func (c *Client) GetRepo(ctx context.Context, owner, name string) (Repo, error) {
	var out Repo
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/repos/"+pathEscape(owner)+"/"+pathEscape(name), nil, &out); err != nil {
		return Repo{}, err
	}
	return out, nil
}

// CloneURLWithAuth 返回含 basic auth 的 git clone URL（供 builder Job 内网 clone）。
// 形如 http://paas-bot:<password>@gitea.paas.svc.cluster.local:3000/paas-bot/<repo>.git。
// 内置 Gitea 是 http（无 TLS），builder 的 injectToken 对含 @ 的 URL 跳过，直接用此 URL。
func (c *Client) CloneURLWithAuth(owner, name string) string {
	return fmt.Sprintf("http://%s:%s@%s/%s/%s.git",
		c.username, c.password, strings.TrimPrefix(c.baseURL, "http://"), owner, name)
}

// ListCommits 最近 N 条提交（Gitea commits API，倒序）。
func (c *Client) ListCommits(ctx context.Context, owner, name string, limit int) ([]Commit, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	p := fmt.Sprintf("/api/v1/repos/%s/%s/commits?limit=%d", pathEscape(owner), pathEscape(name), limit)
	var raw []struct {
		SHA     string `json:"sha"`
		Commit  struct {
			Message string `json:"message"`
			Author  struct {
				Name string `json:"name"`
				Date string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}
	if err := c.doJSON(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return nil, err
	}
	out := make([]Commit, 0, len(raw))
	for _, r := range raw {
		// message 多行取首行作为 summary
		msg := r.Commit.Message
		if i := bytes.IndexByte([]byte(msg), '\n'); i >= 0 {
			msg = msg[:i]
		}
		out = append(out, Commit{
			SHA:     r.SHA,
			Message: msg,
			Author:  r.Commit.Author.Name,
			Date:    r.Commit.Author.Date,
		})
	}
	return out, nil
}

// GetTree 取仓库文件树（recursive，一次拉全树）。ref 为分支/tag/commit，空用默认分支。
func (c *Client) GetTree(ctx context.Context, owner, name, ref string) ([]TreeNode, error) {
	if ref == "" {
		ref = "main"
	}
	p := fmt.Sprintf("/api/v1/repos/%s/%s/git/trees/%s?recursive=1", pathEscape(owner), pathEscape(name), pathEscape(ref))
	var resp struct {
		Tree []TreeNode `json:"tree"`
		Truncated bool   `json:"truncated"`
	}
	if err := c.doJSON(ctx, http.MethodGet, p, nil, &resp); err != nil {
		return nil, err
	}
	return resp.Tree, nil
}

// GetFileContent 取单文件内容（base64 解码为字符串）。
func (c *Client) GetFileContent(ctx context.Context, owner, name, path, ref string) (string, FileContent, error) {
	if ref == "" {
		ref = "main"
	}
	p := fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s?ref=%s", pathEscape(owner), pathEscape(name), pathEscape(path), pathEscape(ref))
	var fc FileContent
	if err := c.doJSON(ctx, http.MethodGet, p, nil, &fc); err != nil {
		return "", FileContent{}, err
	}
	if fc.Encoding == "base64" {
		decoded, err := base64.StdEncoding.DecodeString(fc.Content)
		if err != nil {
			return "", fc, fmt.Errorf("解码文件内容失败: %w", err)
		}
		return string(decoded), fc, nil
	}
	return fc.Content, fc, nil
}

// doJSON 发请求 + 鉴权 + JSON 编解码 + 错误分类。
func (c *Client) doJSON(ctx context.Context, method, path string, body any, out any) error {
	if c.baseURL == "" {
		return ErrGiteaUnavailable
	}
	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("编码请求体失败: %w", err)
		}
		bodyReader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return ErrGiteaUnavailable
	}
	req.SetBasicAuth(c.username, c.password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// 网络不可达/超时 -> 降级标记
		return fmt.Errorf("%w: %v", ErrGiteaUnavailable, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		if out != nil {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				return fmt.Errorf("解析响应失败: %w", err)
			}
		}
		return nil
	case http.StatusNotFound:
		return ErrRepoNotFound
	case http.StatusConflict, http.StatusBadRequest:
		// 409 仓库已存在；400 可能是 name 重复等
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return ErrRepoExists
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	default:
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gitea API %s %s 返回 %d: %s", method, path, resp.StatusCode, string(b))
	}
}

// pathEscape URL 路径段转义（owner/repo/path 可能含特殊字符）。
func pathEscape(s string) string {
	return url.PathEscape(s)
}
