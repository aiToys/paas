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
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aitoys/paas/internal/devops/builder"
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
	// ErrTagExists tag 已存在（CreateTag 幂等回查后仍非同 commit 才返回；
	// release stage Retry 同版本号重打 tag 的常见路径）。
	ErrTagExists = errors.New("tag 已存在")
	// errMergeNotReady Gitea merge 405 瞬态：PR 刚创建可合并性未算完（"Please try again later"）。
	// 无差异场景已被 Merge 的同 SHA 防护拦截，命中即按瞬态短暂重试（mergeWithRetry）。
	errMergeNotReady = errors.New("gitea merge 暂未就绪")
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

// DeleteRepo 删除 Gitea 仓库（平台 DeleteRepo 级联用）。已不存在视为成功（幂等）。
func (c *Client) DeleteRepo(ctx context.Context, owner, name string) error {
	err := c.doJSON(ctx, http.MethodDelete, "/api/v1/repos/"+pathEscape(owner)+"/"+pathEscape(name), nil, nil)
	if errors.Is(err, ErrRepoNotFound) {
		return nil // 幂等：残留清理场景
	}
	return err
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
	if resp.Truncated {
		// 树超 Gitea 单次响应上限被截断：不阻断（部分树仍可浏览），但必须留服务端信号，
		// 否则「文件树不全」无从排查（R16-M1）。
		log.Printf("gitea GetTree %s/%s@%s 树被截断，前端仅显示部分文件", owner, name, ref)
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
	case http.StatusNoContent:
		// DELETE 类 API（如 DeleteRepo）成功返 204 无 body。
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
		// 响应体只进服务端日志，不回传客户端（防 Gitea 内部细节泄漏，审计 M-1）
		b, _ := io.ReadAll(resp.Body)
		log.Printf("gitea API %s %s 返回 %d: %s", method, path, resp.StatusCode, builder.MaskToken(string(b)))
		return fmt.Errorf("gitea API %s %s 返回 %d", method, path, resp.StatusCode)
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
		// 幂等恢复：release stage Retry 确定性生成同版本号，首次 CreateTag 可能实际成功
		// 但响应丢失/超时——409 时回查 tag，同 commit 视为成功（否则 Retry 永久失败且
		// 文案误导为「仓库已存在」，须手工去 Gitea 删 tag）。
		if errors.Is(err, ErrRepoExists) { // doJSON 把 POST 409 统一映射为 ErrRepoExists
			sha, gerr := c.getTagSHA(ctx, owner, repo, tag)
			if gerr == nil && sha == commit {
				return sha, nil
			}
			return "", ErrTagExists
		}
		return "", err
	}
	return out.SHA, nil
}

// getTagSHA 查单tag 指向的 commit SHA（CreateTag 幂等回查用）。不存在返 ErrRepoNotFound。
func (c *Client) getTagSHA(ctx context.Context, owner, repo, tag string) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	p := fmt.Sprintf("/api/v1/repos/%s/%s/git/tags/%s", pathEscape(owner), pathEscape(repo), pathEscape(tag))
	if err := c.doJSON(ctx, http.MethodGet, p, nil, &out); err != nil {
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
	// 1. 建 PR：已存在同 head→base 的 open PR（重试集成/发布时）则查回复用，不重复建。
	// 提取为闭包供 404 竞态下重建 PR 复用（见步骤 2 注释）。
	createPull := func() (prRef, error) {
		var pr prRef
		prBody := map[string]any{
			"base":  base,
			"head":  head,
			"title": fmt.Sprintf("Merge %s into %s", head, base),
		}
		prPath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls", pathEscape(owner), pathEscape(repo))
		err := c.doJSON(ctx, http.MethodPost, prPath, prBody, &pr)
		if errors.Is(err, ErrPRExists) {
			return c.findOpenPR(ctx, owner, repo, head, base)
		}
		return pr, err
	}
	pr, err := createPull()
	if err != nil {
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
	// 2. merge PR（Do=merge/squash）。mergeWithRetry 容忍 405 可合并性竞态（新 PR 秒级 merge）。
	mergeBody := map[string]any{
		"Do":                mergeDo(mode),
		"MergeTitleField":   "",
		"MergeMessageField": "",
	}
	mergePath := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/merge", pathEscape(owner), pathEscape(repo), pr.Number)
	err = c.mergeWithRetry(ctx, mergePath, mergeBody)
	// 404 = 复用的 open PR 被并发关闭：批次集成重试「先删后建集成分支」，Gitea 删 base
	// 分支会异步关旧 PR，与本次 merge 竞态（2026-10-12 实测）。重建 PR 一次再合。
	if errors.Is(err, ErrRepoNotFound) {
		log.Printf("gitea merge: PR #%d 已失效（被并发关闭），重建 PR 重试", pr.Number)
		if pr, err = createPull(); err != nil {
			return "", err
		}
		mergePath = fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/merge", pathEscape(owner), pathEscape(repo), pr.Number)
		err = c.mergeWithRetry(ctx, mergePath, mergeBody)
	}
	if err != nil {
		// 已合并幂等恢复：change.Release 半程失败（merge 全成但 TriggerAppRun 失败回 tested）
		// 重试时对已合并分支再 merge，Gitea 返 409「已合并」——误判 ErrMergeConflict 会把
		// 批次锁在 tested 死循环（每次重试都「冲突」）。回查 PR 状态，已合并视为成功。
		if errors.Is(err, ErrMergeConflict) {
			if merged, _ := c.isPRMerged(ctx, owner, repo, pr.Number); merged {
				if commits, cerr := c.ListCommits(ctx, owner, repo, 1); cerr == nil && len(commits) > 0 {
					return commits[0].SHA, nil
				}
				return pr.Head.SHA, nil
			}
		}
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

// mergeWithRetry 合并请求容忍 405 瞬态（新 PR 可合并性计算竞态）：最多 5 次间隔 2s
// （ctx 感知，总窗口 ~10s——2026-10-12 实测 agentrun 刚建的 PR 计算可超过 3s，3×1s 不够）；
// 非 405 立即返回。Merge 与 MergePR 共用（agentrun 建 PR 后 paas 秒级合入同撞此竞态）。
func (c *Client) mergeWithRetry(ctx context.Context, path string, body any) error {
	var err error
	for i := 0; i < 5; i++ {
		if err = c.doMerge(ctx, path, body); !errors.Is(err, errMergeNotReady) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return err
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

// isPRMerged 查 PR 是否已合并（Merge 的 409 已合并幂等回查用）。查询失败返 false（保守走冲突路径）。
func (c *Client) isPRMerged(ctx context.Context, owner, repo string, number int) (bool, error) {
	var out struct {
		Merged bool `json:"merged"`
	}
	p := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d", pathEscape(owner), pathEscape(repo), number)
	if err := c.doJSON(ctx, http.MethodGet, p, nil, &out); err != nil {
		return false, err
	}
	return out.Merged, nil
}

// prRef PR 最小引用（Merge 建 PR / 复用 open PR / 404 竞态重建共用形状）。
type prRef struct {
	Number int `json:"number"`
	Head   struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

// findOpenPR 查找同 head→base 的 open PR（ErrPRExists 时复用，不重复建）。
// head+base 双匹配——仅按 base 匹配可能复用他人同 base 不同 head 的 PR（误合并无关分支）。
func (c *Client) findOpenPR(ctx context.Context, owner, repo, head, base string) (prRef, error) {
	var out prRef
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

// CreateWebhook 注册仓库 webhook（push 事件 -> pipeline 自动触发）。
// URL 需集群内可达（core Service FQDN）；已存在同 URL webhook 时幂等跳过（Gitea 返 422）。
func (c *Client) CreateWebhook(ctx context.Context, owner, repo, url string) error {
	err := c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/repos/%s/%s/hooks", pathEscape(owner), pathEscape(repo)),
		map[string]any{
			"type": "gitea", "active": true,
			"branch_filter": "*",
			"events":        []string{"push"},
			"config":        map[string]any{"url": url, "content_type": "json", "secret": ""},
		}, nil)
	if err != nil && strings.Contains(err.Error(), "422") {
		return nil // 同 URL 已存在（幂等）
	}
	return err
}

// CreateFile 向仓库提交单个文件（Gitea contents API；新建/更新均可，按 content base64）。
// 模板 seed 用（一键建应用预置 Dockerfile/index.html 等）。
func (c *Client) CreateFile(ctx context.Context, owner, repo, path, message, contentB64 string) error {
	return c.doJSON(ctx, http.MethodPost,
		fmt.Sprintf("/api/v1/repos/%s/%s/contents/%s", pathEscape(owner), pathEscape(repo), pathEscape(path)),
		map[string]any{"content": contentB64, "message": message}, nil)
}

// CreateBranch 从 from 分支创建新分支（POST /repos/{o}/{r}/branches）。
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

// ListBranches 列仓库分支（旅程审计 R5-I6：构建触发对话框分支选择数据源）。
func (c *Client) ListBranches(ctx context.Context, owner, repo string, limit int) ([]Branch, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var out []struct {
		Name   string `json:"name"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	p := fmt.Sprintf("/api/v1/repos/%s/%s/branches?limit=%d", pathEscape(owner), pathEscape(repo), limit)
	if err := c.doBranchJSON(ctx, http.MethodGet, p, nil, &out); err != nil {
		return nil, err
	}
	bs := make([]Branch, 0, len(out))
	for _, b := range out {
		bs = append(bs, Branch{Name: b.Name, CommitSHA: b.Commit.ID})
	}
	return bs, nil
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
		// 响应体只进服务端日志，不拼进 error（Gitea 5xx body 可能含内部堆栈/路径/连接串，
		// error 会经 writeGiteaErr/WriteServiceError 到达用户——与 doJSON 对齐，集成审计 I3）。
		log.Printf("gitea branch %s %s 返回 %d: %s", method, path, resp.StatusCode, string(b))
		return fmt.Errorf("gitea branch %s %s 返回 %d", method, path, resp.StatusCode)
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
	case http.StatusConflict, http.StatusUnprocessableEntity:
		// 409 冲突；422 Gitea 不可合并（冲突/分叉），同归冲突语义
		return ErrMergeConflict
	case http.StatusMethodNotAllowed:
		// 405 瞬态：PR 可合并性未算完（无差异已被 Merge 同 SHA 防护前置拦截），上层短暂重试
		return errMergeNotReady
	case http.StatusNotFound:
		return ErrRepoNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrUnauthorized
	default:
		b, _ := io.ReadAll(resp.Body)
		log.Printf("gitea merge %s 返回 %d: %s", path, resp.StatusCode, string(b))
		return fmt.Errorf("gitea merge %s 返回 %d", path, resp.StatusCode)
	}
}

// ---------- PR 评审（Code Review）----------

// ErrPRNotFound PR 不存在（归一 Gitea 404）。
var ErrPRNotFound = errors.New("PR 不存在")

// maxPullDiffBytes PR diff 读取上限（与 devops handler 的响应截断上限同值，2MB）。
const maxPullDiffBytes = 2 << 20

// PullRequest Gitea PR 视图（评审闭环展示所需字段子集；嵌套 ref/login 展平）。
type PullRequest struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"` // open|closed
	Head      string    `json:"head"`
	Base      string    `json:"base"`
	User      string    `json:"user"`
	CreatedAt time.Time `json:"createdAt"`
	Merged    bool      `json:"merged"`
	Mergeable bool      `json:"mergeable"`
}

// giteaPull wire 结构（Gitea API 原生嵌套形状）。
type giteaPull struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	Head   struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt time.Time `json:"created_at"`
	Merged    bool      `json:"merged"`
	Mergeable bool      `json:"mergeable"`
}

func (g giteaPull) toPull() PullRequest {
	return PullRequest{Number: g.Number, Title: g.Title, Body: g.Body, State: g.State,
		Head: g.Head.Ref, Base: g.Base.Ref, User: g.User.Login, CreatedAt: g.CreatedAt,
		Merged: g.Merged, Mergeable: g.Mergeable}
}

// ListPRs 列 PR。state: open|closed|all（空=open）。
func (c *Client) ListPRs(ctx context.Context, owner, repo, state string) ([]PullRequest, error) {
	if state == "" {
		state = "open"
	}
	p := fmt.Sprintf("/api/v1/repos/%s/%s/pulls?state=%s", pathEscape(owner), pathEscape(repo), url.QueryEscape(state))
	var wire []giteaPull
	if err := c.doJSON(ctx, http.MethodGet, p, nil, &wire); err != nil {
		return nil, err
	}
	out := make([]PullRequest, len(wire))
	for i, g := range wire {
		out[i] = g.toPull()
	}
	return out, nil
}

// GetPR 单个 PR 详情。不存在返 ErrPRNotFound。
func (c *Client) GetPR(ctx context.Context, owner, repo string, number int) (PullRequest, error) {
	p := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d", pathEscape(owner), pathEscape(repo), number)
	var wire giteaPull
	if err := c.doJSON(ctx, http.MethodGet, p, nil, &wire); err != nil {
		if errors.Is(err, ErrRepoNotFound) {
			return PullRequest{}, ErrPRNotFound
		}
		return PullRequest{}, err
	}
	return wire.toPull(), nil
}

// GetPRDiff 取 PR 原始 unified diff 文本（.diff 端点，text/plain，绕过 doJSON 的 JSON 解码）。
// 不存在返 ErrPRNotFound。
func (c *Client) GetPRDiff(ctx context.Context, owner, repo string, number int) (string, error) {
	if c.baseURL == "" {
		return "", ErrGiteaUnavailable
	}
	p := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d.diff", pathEscape(owner), pathEscape(repo), number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+p, nil)
	if err != nil {
		return "", ErrGiteaUnavailable
	}
	req.Header.Set("Accept", "text/plain")
	req.SetBasicAuth(c.username, c.password)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrGiteaUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrPRNotFound
	}
	if resp.StatusCode != http.StatusOK {
		log.Printf("gitea diff %s 返回 %d", p, resp.StatusCode)
		return "", fmt.Errorf("gitea diff 返回 %d", resp.StatusCode)
	}
	// 读取上限 maxPullDiffBytes+1：+1 字节用于判定截断；防恶意/超大 diff 无界读撑爆内存
	// （handler 层截断发生在内存峰值之后，此处是真正的防线）。
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxPullDiffBytes+1))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ReviewPR 提交整体评审。do: APPROVE|REQUEST_CHANGES|COMMENT。
func (c *Client) ReviewPR(ctx context.Context, owner, repo string, number int, do, body string) error {
	p := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/reviews", pathEscape(owner), pathEscape(repo), number)
	var out any
	return c.doJSON(ctx, http.MethodPost, p, map[string]any{"Do": do, "body": body}, &out)
}

// MergePR 按 number 合并 PR（merge commit 模式）。复用 doMerge 的冲突映射 + 405 瞬态重试。
func (c *Client) MergePR(ctx context.Context, owner, repo string, number int) error {
	p := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/merge", pathEscape(owner), pathEscape(repo), number)
	return c.mergeWithRetry(ctx, p, map[string]any{"Do": "merge", "MergeTitleField": "", "MergeMessageField": ""})
}
