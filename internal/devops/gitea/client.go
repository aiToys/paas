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
	ErrMergeConflict    = errors.New("合并冲突，请手动解决")
	ErrBranchExists     = errors.New("分支已存在")
	ErrBranchNotFound   = errors.New("分支不存在")
	ErrPRExists         = errors.New("同源 PR 已存在")
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
	Name     string `json:"name"`
	Path     string `json:"path"`
	Content  string `json:"content"` // base64 编码
	Encoding string `json:"encoding"`
	Size     int64  `json:"size"`
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

// ListCommits 最近 N 条提交（Gitea commits API，倒序）。sha 非空时查指定分支/commit（变更收件箱看工作分支提交用）。
func (c *Client) ListCommits(ctx context.Context, owner, name string, limit int, sha ...string) ([]Commit, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	ref := ""
	if len(sha) > 0 && sha[0] != "" {
		ref = "&sha=" + url.QueryEscape(sha[0])
	}
	p := fmt.Sprintf("/api/v1/repos/%s/%s/commits?limit=%d%s", pathEscape(owner), pathEscape(name), limit, ref)
	var raw []struct {
		SHA    string `json:"sha"`
		Commit struct {
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
		Tree      []TreeNode `json:"tree"`
		Truncated bool       `json:"truncated"`
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
	defer func() { _ = resp.Body.Close() }()

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
		// 409 仓库已存在；400 可能是 name 重复等。
		// 例外：POST /pulls 的 409 = 同 head→base PR 已存在，归一 ErrPRExists
		// 供上层复用未合并的 PR（Gitea 对重复 PR 返回 409 而非 422；与 out 是否传无关）。
		if method == http.MethodPost && strings.HasSuffix(path, "/pulls") {
			return ErrPRExists
		}
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

// CreateTag 在指定 commit 上打 annotated tag（release stage 打版本号里程碑用）。
// 调 Gitea git tags API：POST /repos/{owner}/{repo}/git/tags，body {tag,message,target}，
// 返回 {sha,...}。message 固定为 "release <tag>"（tag 即版本号）。
// 仓库不存在 -> ErrRepoNotFound；Gitea 不可达 -> ErrGiteaUnavailable。
func (c *Client) CreateTag(ctx context.Context, owner, repo, tag, commit string) (string, error) {
	body := map[string]any{
		"tag":     tag,
		"message": "release " + tag,
		"target":  commit,
	}
	var out struct {
		SHA string `json:"sha"`
	}
	p := fmt.Sprintf("/api/v1/repos/%s/%s/git/tags", pathEscape(owner), pathEscape(repo))
	if err := c.doJSON(ctx, http.MethodPost, p, body, &out); err != nil {
		return "", err
	}
	return out.SHA, nil
}

// Merge 把 head 分支合并到 base（baseline stage 合并主干用）。
// 两步：创建 PR -> merge PR。mode: "ff"(merge commit) | "squash"。
// 合并冲突（分叉且 ff 不可行）-> ErrMergeConflict；仓库不存在 -> ErrRepoNotFound；
// Gitea 不可达 -> ErrGiteaUnavailable。
// 返回合并后 base 分支最新 commit SHA（mergeSHA，baseline stage 记录用）。
func (c *Client) Merge(ctx context.Context, owner, repo, head, base, mode string) (string, error) {
	// 1. 创建 PR
	prBody := map[string]any{
		"base":  base,
		"head":  head,
		"title": fmt.Sprintf("Merge %s into %s", head, base),
	}
	var pr struct {
		Number int `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	prPath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls", pathEscape(owner), pathEscape(repo))
	err := c.doJSON(ctx, http.MethodPost, prPath, prBody, &pr)
	if errors.Is(err, ErrPRExists) {
		// 同 head→base 的 open PR 已存在（重试集成/发布时）：查回复用，不重复建。
		if pr, err = c.findOpenPR(ctx, owner, repo, head, base); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	// 无差异合并防护：head 与 base 指向同一 commit（平台代建分支未 push 新提交即入批集成）
	// 时 Gitea merge API 返 405 "Please try again later"。跳过合并、关闭空 PR、返当前 sha。
	if hb, err := c.GetBranch(ctx, owner, repo, head); err == nil {
		if bb, err := c.GetBranch(ctx, owner, repo, base); err == nil && hb.CommitSHA == bb.CommitSHA {
			_ = c.closePR(ctx, owner, repo, pr.Number)
			return bb.CommitSHA, nil
		}
	}
	// 2. merge PR（Do=merge/squash）
	mergeBody := map[string]any{
		"Do":                mergeDo(mode),
		"MergeTitleField":   "",
		"MergeMessageField": "",
	}
	mergePath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/merge", pathEscape(owner), pathEscape(repo), pr.Number)
	if err := c.doMerge(ctx, mergePath, mergeBody); err != nil {
		return "", err
	}
	// 3. 取 merge 后 base 最新 commit SHA（squash=新 commit / ff=head commit）
	commits, err := c.ListCommits(ctx, owner, repo, 1)
	if err != nil || len(commits) == 0 {
		// 降级：用 PR head SHA（mergeSHA 近似，baseline 记录足够）
		return pr.Head.SHA, nil
	}
	return commits[0].SHA, nil
}

// closePR 关闭 PR（无差异 PR 不留残留；PATCH state=closed）。
func (c *Client) closePR(ctx context.Context, owner, repo string, number int) error {
	p := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d", pathEscape(owner), pathEscape(repo), number)
	body := map[string]any{"state": "closed"}
	var out any
	return c.doJSON(ctx, http.MethodPatch, p, body, &out)
}

// mergeDo 把 mode 映射为 Gitea PR merge 的 Do 字段。
// "squash" -> "squash"；其余（含 "ff"）-> "merge"（创建 merge commit）。
func mergeDo(mode string) string {
	if mode == "squash" {
		return "squash"
	}
	return "merge"
}

// findOpenPR 查找同 head→base 的 open PR（ErrPRExists 时复用，不重复建）。
// head+base 双匹配——仅按 base 匹配可能复用他人同 base 不同 head 的 PR（误合并无关分支）。
func (c *Client) findOpenPR(ctx context.Context, owner, repo, head, base string) (struct {
	Number int `json:"number"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
}, error) {
	var out struct {
		Number int `json:"number"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	listPath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls?state=open&limit=50", pathEscape(owner), pathEscape(repo))
	type prItem = struct { // 与 out 同构 + head/base ref（双匹配用）
		Number int `json:"number"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	var list []prItem
	if err := c.doJSON(ctx, http.MethodGet, listPath, nil, &list); err != nil {
		return out, err
	}
	for _, p := range list {
		if p.Base.Ref == base && p.Head.Ref == head {
			out.Number = p.Number
			out.Head.SHA = p.Head.SHA
			return out, nil
		}
	}
	return out, ErrPRExists // 找不到（理论不达，409 必有 open PR）
}

// Branch 分支最小子集。
type Branch struct {
	Name      string `json:"name"`
	CommitSHA string `json:"-"` // 从 commit.id 提取
}

// CreateBranch 从 from 分支/commit 创建新分支（POST /repos/{o}/{r}/branches）。
// 422（分支已存在）-> ErrBranchExists。
func (c *Client) CreateBranch(ctx context.Context, owner, repo, branch, from string) error {
	body := map[string]any{"new_branch_name": branch, "old_branch_name": from, "old_ref_name": from}
	p := fmt.Sprintf("/api/v1/repos/%s/%s/branches", pathEscape(owner), pathEscape(repo))
	return c.doBranch(ctx, http.MethodPost, p, body)
}

// GetBranch 查询分支（不存在返 ErrBranchNotFound）。
func (c *Client) GetBranch(ctx context.Context, owner, repo, branch string) (Branch, error) {
	var out struct {
		Name   string `json:"name"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	p := fmt.Sprintf("/api/v1/repos/%s/%s/branches/%s", pathEscape(owner), pathEscape(repo), pathEscape(branch))
	if err := c.doBranchJSON(ctx, http.MethodGet, p, nil, &out); err != nil {
		return Branch{}, err
	}
	return Branch{Name: out.Name, CommitSHA: out.Commit.ID}, nil
}

// DeleteBranch 删除分支（集成分支重建用）。
func (c *Client) DeleteBranch(ctx context.Context, owner, repo, branch string) error {
	p := fmt.Sprintf("/api/v1/repos/%s/%s/branches/%s", pathEscape(owner), pathEscape(repo), pathEscape(branch))
	return c.doBranch(ctx, http.MethodDelete, p, nil)
}

// doBranch 分支 API 请求（无响应体解码）。与 doMerge 同构：
// 200/201/204 成功；404->ErrBranchNotFound；422->ErrBranchExists（POST 创建重复分支）；
// 401/403->ErrUnauthorized；网络错->ErrGiteaUnavailable 包装。
func (c *Client) doBranch(ctx context.Context, method, path string, body any) error {
	return c.doBranchJSON(ctx, method, path, body, nil)
}

// doBranchJSON 分支 API 请求 + 可选响应解码（GetBranch 用）。
func (c *Client) doBranchJSON(ctx context.Context, method, path string, body, out any) error {
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
		return fmt.Errorf("%w: %v", ErrGiteaUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
		if out != nil {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				return fmt.Errorf("解析响应失败: %w", err)
			}
		}
		return nil
	case http.StatusNotFound:
		return ErrBranchNotFound
	case http.StatusUnprocessableEntity:
		return ErrBranchExists
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	default:
		b, _ := io.ReadAll(resp.Body)
		// Gitea 删除不存在分支返 500 + "object does not exist"（非 404），
		// 按语义归一为 ErrBranchNotFound（幂等删除依赖此容错）。
		if resp.StatusCode == http.StatusInternalServerError && bytes.Contains(b, []byte("object does not exist")) {
			return ErrBranchNotFound
		}
		return fmt.Errorf("gitea branch %s %s 返回 %d: %s", method, path, resp.StatusCode, string(b))
	}
}

// doMerge 合并 PR；200/201 成功，409 冲突 -> ErrMergeConflict
// （与 doJSON 的 409->ErrRepoExists 语义区分，merge 冲突不是仓库已存在）。
func (c *Client) doMerge(ctx context.Context, path string, body any) error {
	if c.baseURL == "" {
		return ErrGiteaUnavailable
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("编码请求体失败: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return ErrGiteaUnavailable
	}
	req.SetBasicAuth(c.username, c.password)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGiteaUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusConflict:
		return ErrMergeConflict
	case http.StatusNotFound:
		return ErrRepoNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	default:
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gitea merge %s 返回 %d: %s", path, resp.StatusCode, string(b))
	}
}
