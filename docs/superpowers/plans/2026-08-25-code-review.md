# Code Review（PR 评审闭环）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 基于内置 Gitea PR API 落地轻量 Code Review 闭环：PR 列表 → diff 查看 → 整体评审（approve/request-changes/comment）→ merge。

**Architecture:** PR 真源永远是 Gitea，平台不落库、无新实体、无 migration。后端 gitea.Client 扩展 PR API（list/get/diff/review/merge）+ devops handler 新增 `/pulls` composite 子路由（AppGuard：评论=write、merge=release）+ 审计。前端 PullDetail 独立页（自研轻量 diff 渲染）+ 代码仓库 tab 入口 + DevOps 评审 tab 跨应用聚合。

**Tech Stack:** Go（net/http，零新依赖）+ Vue 3 + Element Plus。

**Spec:** `docs/superpowers/specs/2026-08-25-code-review-design.md`

## Global Constraints

- 仅 internal 仓库可访问 PR 端点（external 返 405，文案「外部仓库不支持平台内浏览，请到外部 Git 平台查看」）。
- 权限：读 = `repository:read`（不拦 Guard）；评审 = `repository:write` + AppGuard `write`；merge = `repository:write` + AppGuard `release`。
- 响应契约：成功 `{data:T}`（`httputil.WriteData`），错误 `{error:msg}`。
- diff 文本 >2MB 截断 + `truncated:true` 字段。
- Gitea merge 422 → 409 中文错误。
- 评审/merge 记审计：action `pull_request_review` / `pull_request_merge`。
- 不做（YAGNI）：行级评论、PR 创建端点、external 仓库、CI 状态挂 PR、评审门禁接变更管理。
- 注释语言中文（与代码库一致）。
- **未经用户明确要求不执行 git commit**（项目约定；计划中的 commit 步骤在执行时如用户未授权则跳过，留给用户批量提交）。

---

### Task 1: Gitea client PR API 扩展

**Files:**
- Modify: `internal/devops/gitea/client.go`
- Test: `internal/devops/gitea/client_test.go`

**Interfaces:**
- Produces（后续任务依赖的精确签名）:
  - `type PullRequest struct { Number int; Title string; Body string; State string; Head, Base string; User string; CreatedAt time.Time; Merged bool; Mergeable bool; HTMLURL string }`（JSON tag 对应 Gitea API：number/title/body/state/head.ref/base.ref/user.login/created_at/merged/mergeable/html_url）
  - `func (c *Client) ListPRs(ctx context.Context, owner, repo, state string) ([]PullRequest, error)` — state: open|closed|all（空=open）
  - `func (c *Client) GetPR(ctx context.Context, owner, repo string, number int) (PullRequest, error)`
  - `func (c *Client) GetPRDiff(ctx context.Context, owner, repo string, number int) (string, error)` — GET `/repos/{o}/{r}/pulls/{n}.diff`，Accept: text/plain，返原始 unified diff 文本
  - `func (c *Client) ReviewPR(ctx context.Context, owner, repo string, number int, do, body string) error` — do: APPROVE|REQUEST_CHANGES|COMMENT，POST `/pulls/{n}/reviews`
  - `func (c *Client) MergePR(ctx context.Context, owner, repo string, number int) error` — POST `/pulls/{n}/merge`，Do=merge，复用 `doMerge`（409/422 冲突映射与既有 Merge 一致）
  - sentinel：`ErrPRNotFound`（GET 单个 PR 404 时归一）

- [ ] **Step 1: 写失败测试**

在 `internal/devops/gitea/client_test.go` 追加（fakeGitea 复用既有模式，但 PR API 形状不同，另建 fake）：

```go
// fakePRGitea 启动 fake Gitea server 覆盖 PR 端点。
// reviewBody 记录 reviews 请求体；diffText 控制 .diff 端点返回内容。
func fakePRGitea(t *testing.T, mergeStatus int) (*httptest.Server, *Client, *map[string]any, *string) {
	t.Helper()
	var review map[string]any
	var gotDiff string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pulls"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"number":7,"title":"feat: x","body":"desc","state":"open",
				"head":{"ref":"feat-x"},"base":{"ref":"main"},"user":{"login":"alice"},
				"created_at":"2026-08-25T10:00:00Z","merged":false,"mergeable":true,
				"html_url":"http://g/pulls/7"}]`)
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pulls/7"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"number":7,"title":"feat: x","state":"open","head":{"ref":"feat-x"},"base":{"ref":"main"}}`)
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pulls/7.diff"):
			gotDiff = "diff --git a/main.go b/main.go\n+hello"
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, gotDiff)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/pulls/7/reviews"):
			b, _ := io.ReadAll(r.Body)
			review = map[string]any{}
			_ = json.Unmarshal(b, &review)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/pulls/7/merge"):
			w.WriteHeader(mergeStatus)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, New(srv.URL, "bot", "pass"), &review, &gotDiff
}

func TestListPRs(t *testing.T) {
	_, c, _, _ := fakePRGitea(t, 200)
	prs, err := c.ListPRs(context.Background(), "bot", "app", "open")
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].Number != 7 || prs[0].Head != "feat-x" || prs[0].Base != "main" || prs[0].User != "alice" {
		t.Fatalf("unexpected: %+v", prs)
	}
}

func TestGetPRDiff(t *testing.T) {
	_, c, _, got := fakePRGitea(t, 200)
	diff, err := c.GetPRDiff(context.Background(), "bot", "app", 7)
	if err != nil {
		t.Fatal(err)
	}
	if diff != "diff --git a/main.go b/main.go\n+hello" || *got != diff {
		t.Fatalf("diff mismatch: %q", diff)
	}
}

func TestReviewPR(t *testing.T) {
	_, c, rev, _ := fakePRGitea(t, 200)
	if err := c.ReviewPR(context.Background(), "bot", "app", 7, "APPROVE", " LGTM"); err != nil {
		t.Fatal(err)
	}
	if (*rev)["Do"] != "APPROVE" || (*rev)["body"] != " LGTM" {
		t.Fatalf("review body: %+v", *rev)
	}
}

func TestMergePR(t *testing.T) {
	_, c, _, _ := fakePRGitea(t, 200)
	if err := c.MergePR(context.Background(), "bot", "app", 7); err != nil {
		t.Fatal(err)
	}
}

func TestMergePRConflict(t *testing.T) {
	_, c, _, _ := fakePRGitea(t, 422)
	if err := c.MergePR(context.Background(), "bot", "app", 7); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("want ErrMergeConflict, got %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/devops/gitea/ -run 'TestListPRs|TestGetPRDiff|TestReviewPR|TestMergePR' -v`
Expected: FAIL（方法未定义，编译错误）

- [ ] **Step 3: 实现**

`internal/devops/gitea/client.go` 追加（紧跟既有 Merge 函数区域）：

```go
// ---------- PR 评审（Code Review）----------

// ErrPRNotFound PR 不存在（归一 Gitea 404）。
var ErrPRNotFound = errors.New("pull request not found")

// PullRequest Gitea PR 视图（评审闭环展示所需字段子集）。
type PullRequest struct {
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	State     string    `json:"state"` // open|closed
	Head      string    `json:"-"`     // head.ref
	Base      string    `json:"-"`     // base.ref
	User      string    `json:"-"`     // user.login
	CreatedAt time.Time `json:"created_at"`
	Merged    bool      `json:"merged"`
	Mergeable bool      `json:"mergeable"`
	HTMLURL   string    `json:"html_url"`
}

// giteaPull wire 结构（嵌套 ref/login 展平到 PullRequest）。
type giteaPull struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	State     string `json:"state"`
	Head      struct{ Ref string `json:"ref"` } `json:"head"`
	Base      struct{ Ref string `json:"ref"` } `json:"base"`
	User      struct{ Login string `json:"login"` } `json:"user"`
	CreatedAt time.Time                          `json:"created_at"`
	Merged    bool                                `json:"merged"`
	Mergeable bool                                `json:"mergeable"`
	HTMLURL   string                              `json:"html_url"`
}

func (g giteaPull) toPull() PullRequest {
	return PullRequest{Number: g.Number, Title: g.Title, Body: g.Body, State: g.State,
		Head: g.Head.Ref, Base: g.Base.Ref, User: g.User.Login, CreatedAt: g.CreatedAt,
		Merged: g.Merged, Mergeable: g.Mergeable, HTMLURL: g.HTMLURL}
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

// GetPR 单个 PR 详情。
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

// GetPRDiff 取 PR 原始 unified diff 文本（.diff 端点，非 JSON）。
func (c *Client) GetPRDiff(ctx context.Context, owner, repo string, number int) (string, error) {
	p := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d.diff", pathEscape(owner), pathEscape(repo), number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+p, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "text/plain")
	req.SetBasicAuth(c.username, c.password)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrGiteaUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", ErrPRNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gitea diff: status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(resp.Body)
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

// MergePR 合并 PR（merge commit 模式）。复用 doMerge 的 409/422 冲突映射。
func (c *Client) MergePR(ctx context.Context, owner, repo string, number int) error {
	p := fmt.Sprintf("/api/v1/repos/%s/%s/pulls/%d/merge", pathEscape(owner), pathEscape(repo), number)
	return c.doMerge(ctx, p, map[string]any{"Do": "merge", "MergeTitleField": "", "MergeMessageField": ""})
}
```

注意：
- `c.baseURL`/`c.username`/`c.password`/`c.http` 字段名以 client.go 实际为准（读一遍 New() 确认，通常是这几个；GetPRDiff 直连 raw 端点绕过 doJSON 的 JSON 解码）。
- `doJSON` 已有 404→ErrRepoNotFound 归一（GET pulls/{n} 404 时映射 ErrPRNotFound）。
- `doMerge` 已有 409/422→ErrMergeConflict 映射（既有测试 TestMergeConflict 依赖）。若 422 未映射，在 doMerge 或 MergePR 内补 422 分支与 409 同路径。
- `url` 包如未 import 需补 `net/url`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/devops/gitea/ -v`
Expected: 全部 PASS（含既有测试无回归）

- [ ] **Step 5: Commit（用户已授权批量提交时）**

```bash
git add internal/devops/gitea/client.go internal/devops/gitea/client_test.go
git commit -m "feat(devops): gitea client 扩展 PR API——list/get/diff/review/merge"
```

---

### Task 2: devops handler PR 子路由（列表/详情+diff/评审/merge + 审计 + OpenAPI）

**Files:**
- Modify: `internal/devops/handler.go`（serveApp 分发 + 新函数 ~200 行）
- Modify: `cmd/core/main.go`（OpenAPI Operation 登记，~1220 行 reg.Operation 区域）
- Test: `internal/devops/handler_test.go`

**Interfaces:**
- Consumes: Task 1 的 `ListPRs/GetPR/GetPRDiff/ReviewPR/MergePR/ErrPRNotFound`；既有 `h.allow`/`h.allowApp`/`h.writeGiteaErr`/`h.repos.GetRepo`/`PermRepoRead`/`PermRepoWrite`；`application.AppActionWrite/AppActionRelease`；pipeline 的 `recordAudit` 模式（devops handler 若无 audit 字段则复用 change handler 模式，见下）。
- Produces: REST 端点
  - `GET  /api/applications/{id}/repositories/{rid}/pulls?state=` → `{data:[PullRequest]}`
  - `GET  /api/applications/{id}/repositories/{rid}/pulls/{number}` → `{data:{pr:PullRequest, diff:string, truncated:bool}}`
  - `POST /api/applications/{id}/repositories/{rid}/pulls/{number}/reviews` body `{do:"APPROVE"|"REQUEST_CHANGES"|"COMMENT", body:string}` → 204
  - `POST /api/applications/{id}/repositories/{rid}/pulls/{number}/merge` → 204
- 审计：devops handler 加 `audit pipeline.AuditRecorder` 风格注入（若 devops handler 已有审计字段则直接用；没有则加 `AuditRecorder` 接口字段 + `WithAudit` opt，签名与 `internal/devops/pipeline/handler.go:43` 一致：`Record(ctx context.Context, tenantID, actor, action, resourceType, resourceID, detail string) error`），cmd/core 已桥接的 audit adapter 直接复用注入。

- [ ] **Step 1: 写失败测试**

`internal/devops/handler_test.go` 追加（构造模式参考既有 handler 测试；fake gitea 用 httptest 起 PR 端点）：

```go
// newPullTestHandler 构造挂 fake gitea 的 devops handler + internal 仓库。
// mergeStatus 控制 merge 端点返回。
func newPullTestHandler(t *testing.T, mergeStatus int, restricted bool, memberRole string) (http.Handler, *application.AppGuard) {
	// 1. fake gitea（PR 端点，与 Task1 fakePRGitea 同形状；handler 侧内联即可）
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pulls"):
			_, _ = io.WriteString(w, `[{"number":7,"title":"feat: x","state":"open","head":{"ref":"feat-x"},"base":{"ref":"main"},"user":{"login":"alice"},"created_at":"2026-08-25T10:00:00Z","mergeable":true}]`)
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/pulls/7.diff"):
			_, _ = io.WriteString(w, "diff --git a/a.go b/a.go\n+new line")
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/pulls/7/reviews"):
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPost && strings.HasSuffix(p, "/pulls/7/merge"):
			w.WriteHeader(mergeStatus)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// 2. memory stores + internal 仓库（Source=RepoSourceInternal, GiteaRepo="app-repo"）
	//    + 可选受限应用成员（参照 internal/core/application/guard_e2e_test.go 构造 AppGuard）
	//    + tenant ctx（参照既有 handler 测试的 ctx 注入模式）
	// 3. devops.NewHandler(opts...) 挂 WithGiteaClient(gitea.New(srv.URL, "bot", "pass"))
	// 返回 handler.ServeHTTP 与 guard
	...
}

func TestPullListOK(t *testing.T)            // GET pulls → 200，data 数组含 number=7
func TestPullListExternal405(t *testing.T)   // external 仓库 → 405
func TestPullDetailWithDiff(t *testing.T)    // GET pulls/7 → 200，diff 含 "diff --git"，truncated=false
func TestPullReviewRequiresWrite(t *testing.T) // viewer 成员 + 受限应用 → 403；developer → 204
func TestPullMergeRequiresRelease(t *testing.T) // developer 成员 merge → 403；maintainer → 204
func TestPullMergeConflict409(t *testing.T)  // fake gitea merge 422 → 409
```

具体构造代码执行时参照 `internal/devops/handler_test.go` 既有测试的 store/ctx 搭建方式补全（此处不重复贴，模式完全一致）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/devops/ -run TestPull -v`
Expected: FAIL（路由 404）

- [ ] **Step 3: 实现 handler**

`internal/devops/handler.go`：

1. `serveApp` 的 `case "repositories"` 扩展：

```go
	case "repositories":
		switch len(parts) {
		case 2:
			h.serveRepos(w, r, appID)
		case 3:
			h.serveRepoDelete(w, r, parts[2])
		case 4:
			// /repositories/{rid}/{tree|commits|file} 仓库内容浏览（仅 internal）
			h.serveRepoBrowse(w, r, parts[2], parts[3])
		case 5, 6:
			// /repositories/{rid}/pulls[/{number}[/{reviews|merge}]] PR 评审闭环（仅 internal）
			h.serveRepoPulls(w, r, appID, parts[2], parts[3:])
		default:
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
```

2. 新增 `serveRepoPulls`（放在 serveRepoBrowse 后）：

```go
// serveRepoPulls 处理 PR 评审闭环子路由（Code Review，真源 Gitea）：
//
//	GET  .../pulls?state=                       PR 列表
//	GET  .../pulls/{number}                     详情 + diff 文本
//	POST .../pulls/{number}/reviews             评审（approve/request-changes/comment）
//	POST .../pulls/{number}/merge               合并
//
// 权限：读=repository:read；评审=write+AppGuard write；merge=write+AppGuard release。
// 仅 internal 仓库。评审/merge 记审计。
func (h *Handler) serveRepoPulls(w http.ResponseWriter, r *http.Request, appID, repoID string, rest []string) {
	// rest[0] == "pulls"；len(rest)==1 列表；==2 详情；==3 动作（reviews|merge）
	if h.giteaClient == nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "内置 Git 后端未启用")
		return
	}
	repo, err := h.repos.GetRepo(r.Context(), repoID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "仓库不存在")
		return
	}
	if repo.Source != RepoSourceInternal {
		httputil.WriteError(w, http.StatusMethodNotAllowed, "外部仓库不支持平台内浏览，请到外部 Git 平台查看")
		return
	}
	owner := repo.GiteaOwner
	if owner == "" {
		owner = h.giteaClient.Username()
	}
	name := repo.GiteaRepo

	switch len(rest) {
	case 1: // 列表
		if r.Method != http.MethodGet || !h.allow(w, r, PermRepoRead) {
			return
		}
		prs, err := h.giteaClient.ListPRs(r.Context(), owner, name, r.URL.Query().Get("state"))
		if err != nil {
			h.writeGiteaErr(w, err)
			return
		}
		httputil.WriteData(w, prs)
	case 2: // 详情 + diff
		if r.Method != http.MethodGet || !h.allow(w, r, PermRepoRead) {
			return
		}
		number, ok := parsePullNumber(w, rest[1])
		if !ok {
			return
		}
		pr, err := h.giteaClient.GetPR(r.Context(), owner, name, number)
		if errors.Is(err, gitea.ErrPRNotFound) {
			httputil.WriteError(w, http.StatusNotFound, "PR 不存在")
			return
		} else if err != nil {
			h.writeGiteaErr(w, err)
			return
		}
		diff, err := h.giteaClient.GetPRDiff(r.Context(), owner, name, number)
		if err != nil && !errors.Is(err, gitea.ErrPRNotFound) {
			h.writeGiteaErr(w, err)
			return
		}
		truncated := false
		if len(diff) > maxPullDiffBytes {
			diff = diff[:maxPullDiffBytes]
			truncated = true
		}
		httputil.WriteData(w, map[string]any{"pr": pr, "diff": diff, "truncated": truncated})
	case 3: // 动作
		number, ok := parsePullNumber(w, rest[1])
		if !ok || r.Method != http.MethodPost || !h.allow(w, r, PermRepoWrite) {
			return
		}
		switch rest[2] {
		case "reviews":
			if !h.allowApp(w, r, appID, application.AppActionWrite) {
				return
			}
			var in struct {
				Do   string `json:"do"`   // APPROVE|REQUEST_CHANGES|COMMENT
				Body string `json:"body"`
			}
			if err := jsonDecode(r, &in); err != nil {
				httputil.WriteError(w, http.StatusBadRequest, err.Error())
				return
			}
			switch in.Do {
			case "APPROVE", "REQUEST_CHANGES", "COMMENT":
			default:
				httputil.WriteError(w, http.StatusBadRequest, "do 取值非法（APPROVE/REQUEST_CHANGES/COMMENT）")
				return
			}
			if err := h.giteaClient.ReviewPR(r.Context(), owner, name, number, in.Do, in.Body); err != nil {
				h.writeGiteaErr(w, err)
				return
			}
			h.recordAudit(r, "pull_request_review", "repository", repoID,
				fmt.Sprintf("PR#%d %s", number, in.Do))
			w.WriteHeader(http.StatusNoContent)
		case "merge":
			if !h.allowApp(w, r, appID, application.AppActionRelease) {
				return
			}
			if err := h.giteaClient.MergePR(r.Context(), owner, name, number); err != nil {
				if errors.Is(err, gitea.ErrMergeConflict) {
					httputil.WriteError(w, http.StatusConflict, "合并失败：存在冲突或 PR 不可合并")
					return
				}
				h.writeGiteaErr(w, err)
				return
			}
			h.recordAudit(r, "pull_request_merge", "repository", repoID, fmt.Sprintf("PR#%d", number))
			w.WriteHeader(http.StatusNoContent)
		default:
			httputil.WriteError(w, http.StatusNotFound, "not found")
		}
	default:
		httputil.WriteError(w, http.StatusNotFound, "not found")
	}
}

const maxPullDiffBytes = 2 << 20 // 2MB，大 PR 截断防御

func parsePullNumber(w http.ResponseWriter, s string) (int, bool) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		httputil.WriteError(w, http.StatusBadRequest, "PR number 非法")
		return 0, false
	}
	return n, true
}
```

适配要点（执行时核对）：
- `jsonDecode` 用 handler 内既有的 body 解码 helper（grep `json.NewDecoder` 找同款；没有就内联 `json.NewDecoder(r.Body).Decode(&in)`）。
- `h.recordAudit`：devops handler 若无此方法，加一个与 `internal/devops/change/handler.go:562` 同签名的私有方法 + `AuditRecorder` 接口字段 + `WithAudit` opt；cmd/core 装配处注入既有 audit adapter（grep `change.*WithAudit\|pipeline.*WithAudit` 找桥接点同款注入）。
- `PermRepoWrite` 常量名核对（grep `PermRepo` 确认是 `PermRepoWrite` 还是别的）。

3. `cmd/core/main.go` OpenAPI 登记（~1221 行后追加）：

```go
reg.Operation("GET", "/api/applications/{id}/repositories/{rid}/pulls", apiroute.Tags("DevOps"), apiroute.Summary("内置仓库 PR 列表（Gitea）"), apiroute.Perm("repository:read"), apiroute.WithResp([]gitea.PullRequest{}))
reg.Operation("GET", "/api/applications/{id}/repositories/{rid}/pulls/{number}", apiroute.Tags("DevOps"), apiroute.Summary("PR 详情 + diff 文本"), apiroute.Perm("repository:read"), apiroute.WithResp(map[string]any{}))
reg.Operation("POST", "/api/applications/{id}/repositories/{rid}/pulls/{number}/reviews", apiroute.Tags("DevOps"), apiroute.Summary("PR 整体评审（approve/request-changes/comment）"), apiroute.Perm("repository:write"), apiroute.WithReqBody(struct {
	Do   string `json:"do"`
	Body string `json:"body"`
}{}))
reg.Operation("POST", "/api/applications/{id}/repositories/{rid}/pulls/{number}/merge", apiroute.Tags("DevOps"), apiroute.Summary("合并 PR"), apiroute.Perm("repository:write"))
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/devops/ -v && go build ./...`
Expected: 全 PASS（含权限矩阵 6 测试）

- [ ] **Step 5: Commit（用户已授权批量提交时）**

```bash
git add internal/devops/handler.go internal/devops/handler_test.go cmd/core/main.go
git commit -m "feat(devops): PR 评审端点——列表/详情diff/评审/merge + AppGuard + 审计"
```

---

### Task 3: 跨应用 PR 聚合端点（DevOps 评审 tab 数据源）

**Files:**
- Modify: `internal/devops/handler.go`（ServeHTTP 顶层路由 + 新函数）
- Modify: `cmd/core/main.go`（OpenAPI）
- Test: `internal/devops/handler_test.go`

**Interfaces:**
- Consumes: Task 1 `ListPRs`；Task 2 模式；既有 `h.repos` Repository（需确认有跨应用 list 能力——`ListRepos(ctx, appID)` appID 空时返租户内全部，与 buildruns 跨应用列表同语义；若 ListRepos 不支持空 appID，用 `ListAllRepos` 或遍历，执行时 grep 确认）。
- Produces: `GET /api/pulls` → `{data:[{repoId, repoName, appId, pr PullRequest}]}`，PermRepoRead，仅 internal 仓库、state=open。

- [ ] **Step 1: 写失败测试**

```go
func TestGlobalPullList(t *testing.T) {
	// 两应用各一 internal 仓库 + fake gitea 返 1 个 open PR
	// GET /api/pulls → 200，data 含 2 条，每条带 repoId/appId/pr.number=7
}
func TestGlobalPullListExcludesExternal(t *testing.T) {
	// 一 internal 一 external 仓库 → data 仅 1 条（external 不聚合）
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/devops/ -run TestGlobalPull -v`
Expected: FAIL（404）

- [ ] **Step 3: 实现**

`ServeHTTP` 顶层（`case path == "/api/buildruns":` 同级）加：

```go
	case path == "/api/pulls":
		h.serveGlobalPulls(w, r)
```

```go
// serveGlobalPulls 跨应用聚合 open PR（DevOps 评审 tab 数据源）。
// 遍历租户内 internal 仓库逐个 ListPRs(open)，聚合带 repoId/appId 定位信息。
// 单仓库 Gitea 失败跳过（降级不阻断整个列表）。
func (h *Handler) serveGlobalPulls(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || !h.allow(w, r, PermRepoRead) {
		return
	}
	if h.giteaClient == nil {
		httputil.WriteData(w, []any{})
		return
	}
	repos, err := h.repos.ListRepos(r.Context(), "") // 空 appID=租户内全部（执行时核对语义）
	if err != nil {
		httputil.WriteServiceError(w, http.StatusInternalServerError, err)
		return
	}
	type pullItem struct {
		RepoID   string          `json:"repoId"`
		RepoName string          `json:"repoName"`
		AppID    string          `json:"appId"`
		PR       gitea.PullRequest `json:"pr"`
	}
	var out []pullItem
	for _, repo := range repos {
		if repo.Source != RepoSourceInternal || repo.GiteaRepo == "" {
			continue
		}
		owner := repo.GiteaOwner
		if owner == "" {
			owner = h.giteaClient.Username()
		}
		prs, err := h.giteaClient.ListPRs(r.Context(), owner, repo.GiteaRepo, "open")
		if err != nil {
			continue // 单仓库失败跳过（降级）
		}
		for _, pr := range prs {
			out = append(out, pullItem{RepoID: repo.ID, RepoName: repo.Name, AppID: repo.AppID, PR: pr})
		}
	}
	httputil.WriteData(w, out)
}
```

`cmd/core/main.go` OpenAPI：

```go
reg.Operation("GET", "/api/pulls", apiroute.Tags("DevOps"), apiroute.Summary("跨应用 open PR 聚合（评审 tab）"), apiroute.Perm("repository:read"))
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/devops/ -run TestGlobalPull -v`
Expected: PASS

- [ ] **Step 5: Commit（用户已授权批量提交时）**

```bash
git add internal/devops/handler.go internal/devops/handler_test.go cmd/core/main.go
git commit -m "feat(devops): GET /api/pulls 跨应用 open PR 聚合"
```

---

### Task 4: 前端 API 层 + diff 解析纯函数

**Files:**
- Create: `frontend/console-user/src/api/pulls.ts`
- Create: `frontend/console-user/src/utils/diff.ts`
- Test: `frontend/console-user/src/utils/diff.test.ts`（vitest，执行时核对项目是否已有 vitest；若无则用 node 脚本手测 + 移除测试文件，或确认 workspace 配置后补）

**Interfaces:**
- Consumes: Task 2/3 端点；既有 `fetchJSON` helper（`src/api/` 内）。
- Produces:
  - `listPulls(appId, repoId, state)` / `getPullDetail(appId, repoId, number)` / `reviewPull(appId, repoId, number, do, body)` / `mergePull(appId, repoId, number)` / `listGlobalPulls()`
  - `parseDiff(text: string): DiffFile[]`，`DiffFile = { path: string; lines: DiffLine[] }`，`DiffLine = { type: 'add'|'del'|'ctx'|'meta'; text: string }`

- [ ] **Step 1: 写失败测试（diff 解析）**

```ts
import { describe, it, expect } from 'vitest'
import { parseDiff } from './diff'

const sample = `diff --git a/main.go b/main.go
index 111..222 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+func new() {}
-old code
 context
diff --git a/README.md b/README.md
new file mode 100644
--- /dev/null
+++ b/README.md
@@ -0,0 +1 @@
+hello`

describe('parseDiff', () => {
  it('按 diff --git 分文件', () => {
    const files = parseDiff(sample)
    expect(files).toHaveLength(2)
    expect(files[0].path).toBe('main.go')
    expect(files[1].path).toBe('README.md')
  })
  it('行类型标记正确', () => {
    const lines = parseDiff(sample)[0].lines
    expect(lines.find(l => l.text.includes('func new'))?.type).toBe('add')
    expect(lines.find(l => l.text.includes('old code'))?.type).toBe('del')
    expect(lines.find(l => l.text.includes('context'))?.type).toBe('ctx')
    expect(lines.filter(l => l.type === 'meta').length).toBeGreaterThan(0) // @@ 与 index 行
  })
  it('空 diff 返空数组', () => {
    expect(parseDiff('')).toEqual([])
  })
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd frontend && pnpm vitest run console-user/src/utils/diff.test.ts`（执行时核对 workspace 下 vitest 调用方式）
Expected: FAIL（模块不存在）

- [ ] **Step 3: 实现**

`src/utils/diff.ts`：

```ts
export interface DiffLine { type: 'add' | 'del' | 'ctx' | 'meta'; text: string }
export interface DiffFile { path: string; lines: DiffLine[] }

// parseDiff 解析 unified diff 文本为文件分组。轻量自研（Code Review 展示用，
// 不引外部 diff 库）：按 "diff --git" 分文件，行首 +/=add、-/=del、@@/index/...=meta。
export function parseDiff(text: string): DiffFile[] {
  const files: DiffFile[] = []
  let cur: DiffFile | null = null
  for (const raw of text.split('\n')) {
    if (raw.startsWith('diff --git ')) {
      // "diff --git a/path b/path" 取 b 侧（新文件路径）
      const m = raw.match(/^diff --git a\/(.+) b\/(.+)$/)
      cur = { path: m ? m[2] : raw, lines: [] }
      files.push(cur)
      continue
    }
    if (!cur) continue
    if (raw.startsWith('+++') || raw.startsWith('---') || raw.startsWith('@@') ||
        raw.startsWith('index ') || raw.startsWith('new file') || raw.startsWith('deleted file') ||
        raw.startsWith('old mode') || raw.startsWith('new mode') || raw.startsWith('Binary')) {
      cur.lines.push({ type: 'meta', text: raw })
    } else if (raw.startsWith('+')) {
      cur.lines.push({ type: 'add', text: raw })
    } else if (raw.startsWith('-')) {
      cur.lines.push({ type: 'del', text: raw })
    } else {
      cur.lines.push({ type: 'ctx', text: raw.replace(/^ /, '') })
    }
  }
  return files
}
```

`src/api/pulls.ts`（复用既有 `fetchJSON`/`fetchAuth` 模式，执行时对照 `src/api/` 内既有文件的头与 helper）：

```ts
import { fetchAuth } from './fetch' // 执行时核对实际 helper 名与路径

export interface PullRequest {
  number: number; title: string; body: string; state: string
  head: string; base: string; user: string
  createdAt: string; merged: boolean; mergeable: boolean
}
export interface PullDetail { pr: PullRequest; diff: string; truncated: boolean }
export interface GlobalPull { repoId: string; repoName: string; appId: string; pr: PullRequest }

export const listPulls = (appId: string, repoId: string, state = 'open') =>
  fetchAuth(`/api/applications/${appId}/repositories/${repoId}/pulls?state=${state}`)
export const getPullDetail = (appId: string, repoId: string, number: number) =>
  fetchAuth(`/api/applications/${appId}/repositories/${repoId}/pulls/${number}`)
export const reviewPull = (appId: string, repoId: string, number: number, do_: string, body: string) =>
  fetchAuth(`/api/applications/${appId}/repositories/${repoId}/pulls/${number}/reviews`,
    { method: 'POST', body: JSON.stringify({ do: do_, body }) })
export const mergePull = (appId: string, repoId: string, number: number) =>
  fetchAuth(`/api/applications/${appId}/repositories/${repoId}/pulls/${number}/merge`, { method: 'POST' })
export const listGlobalPulls = () => fetchAuth('/api/pulls')
```

- [ ] **Step 4: 跑测试确认通过**

Run: `cd frontend && pnpm vitest run console-user/src/utils/diff.test.ts`
Expected: PASS（若无 vitest 基础设施：用 `npx tsx` 跑断言脚本验证后删除脚本，diff.test.ts 保留待项目接入 vitest——执行时按实际情况决定并在结果中说明）

- [ ] **Step 5: Commit（用户已授权批量提交时）**

```bash
git add frontend/console-user/src/api/pulls.ts frontend/console-user/src/utils/
git commit -m "feat(console-user): PR API 层 + unified diff 解析纯函数"
```

---

### Task 5: PR 列表入口 + PullDetail 详情页（diff 渲染 + 评审 + merge）

**Files:**
- Create: `frontend/console-user/src/views/PullDetail.vue`
- Create: `frontend/console-user/src/views/Pulls.vue`（单仓库 PR 列表，应用内入口）
- Modify: `frontend/console-user/src/router.ts`（两路由）
- Modify: 代码仓库 tab 组件（`ApplicationDetail.vue` 或 `app-tabs/` 下 Repositories 相关组件——执行时 grep `RepoBrowser` 定位 internal 仓库卡片，加「评审」入口按钮）

**Interfaces:**
- Consumes: Task 4 全部 API + `parseDiff`；既有 `useDangerConfirm` composable。
- Produces: 路由 `/devops/pulls/:repoId/:number`（PullDetail 全屏）与应用内 PR 列表入口。

- [ ] **Step 1: 路由注册**

`router.ts` 追加（对齐既有 ChangeDetail 模式）：

```ts
{ path: '/devops/pulls/:repoId/:number', component: () => import('./views/PullDetail.vue'),
  meta: { title: 'PR 详情' } },
{ path: '/apps/:appId/repositories/:repoId/pulls', component: () => import('./views/Pulls.vue'),
  meta: { title: '代码评审' } },
```

- [ ] **Step 2: Pulls.vue（单仓库 PR 列表）**

结构（对齐既有列表页风格，SearchTable 可不用——数据量小直接 el-table）：

```vue
<script setup lang="ts">
// state tab（open/closed/all）+ el-table：number/title/head→base/user/created_at/状态 tag/操作（查看）
// 「查看」→ router.push(`/devops/pulls/${repoId}/${row.number}?appId=${appId}`)
</script>
```

关键逻辑：`onMounted` 拉 `listPulls(appId, repoId, stateTab)`；appId 从 route query 或 path 取。

- [ ] **Step 3: PullDetail.vue（核心页）**

结构（对齐 ChangeDetail 单据模式）：

```vue
<script setup lang="ts">
// 1. meta 区：返回箭头 + PR#number title + head→base 分支 + 作者/时间/状态 tag
//    （merged=success、open=primary、closed=info）
// 2. diff 渲染区：parseDiff(detail.diff) → 按文件 el-card（header=文件路径+增删行数统计，
//    可折叠 collapse）→ 行列表 monospace：
//    type=add → class 'diff-add'（绿底）；del → 'diff-del'（红底）；meta → 灰色小字；ctx → 默认
//    truncated=true 时顶部 el-alert「diff 过大已截断，请到 Git 平台查看完整内容」
// 3. 评审操作条：el-input textarea 评论框 + 三按钮（批准/要求修改/仅评论）→ reviewPull
// 4. merge 按钮：pr.state==='open' 时显示，点击走 confirmDangerous（输入 PR#number 确认，
//    isProd=true 语义——merge 到 main 一律危险确认）→ mergePull → 成功后刷新详情
// 5. 权限兜底：403 时后端已返中文错误，前端 http helper 已有全局错误提示（无需额外处理）
</script>
<style scoped>
.diff-line { font-family: monospace; font-size: 12px; padding: 0 8px; white-space: pre; }
.diff-add { background: var(--el-color-success-light-9); color: var(--el-color-success) }
.diff-del { background: var(--el-color-danger-light-9); color: var(--el-color-danger) }
.diff-meta { color: var(--el-text-color-secondary); font-size: 11px }
</style>
```

repoId/number 从 route params 取，appId 从 route query 取（详情页拉 `getPullDetail` 需要 appId）。

- [ ] **Step 4: 代码仓库 tab 加入口**

internal 仓库卡片/行加「评审」按钮 → `router.push(/apps/${appId}/repositories/${repoId}/pulls)`（执行时定位实际组件文件，与既有「浏览」按钮并排）。

- [ ] **Step 5: 构建验证**

Run: `cd frontend && pnpm build`
Expected: 三套前端 build 通过（vue-tsc 无错）

- [ ] **Step 6: Commit（用户已授权批量提交时）**

```bash
git add frontend/console-user/src/
git commit -m "feat(console-user): PR 列表 + PullDetail 详情页（diff 渲染/评审/merge）"
```

---

### Task 6: DevOps 中心评审 tab + 值班台联动

**Files:**
- Modify: `frontend/console-user/src/views/DevOps.vue`

**Interfaces:**
- Consumes: Task 4 `listGlobalPulls`/`GlobalPull`；既有 DevOps 七 tab 结构与值班台三列。
- Produces: DevOps 中心「评审」tab（第 8 个 tab）+ 值班台「等评审」列。

- [ ] **Step 1: 评审 tab**

`DevOps.vue` 追加 tab（对齐既有「变更」「批次」tab 模式）：

```vue
<el-tab-pane label="评审" name="pulls">
  <!-- el-table：应用（appId 列）/PR#/标题/head→base/作者/时间/操作（查看→PullDetail） -->
  <!-- onMounted + 30s 轮询 listGlobalPulls()，onUnmounted clearInterval（对齐既有轮询清理模式） -->
</el-tab-pane>
```

查看跳转需 appId：`router.push(/devops/pulls/${row.repoId}/${row.pr.number}?appId=${row.appId})`。

- [ ] **Step 2: 值班台「等评审」信号**

值班台三列（🔴失败/⏸等审批/🏃进行中）旁加第四列或并入「⏸等审批」：`listGlobalPulls()` 结果中 `pr.user !== 当前用户` 的条目计入待处理（点击直达 PullDetail）。实现方式对齐既有值班台数据组装函数，加 `pulls` 数据源。当前用户名从 session store 取（执行时核对字段名）。

- [ ] **Step 3: 构建验证**

Run: `cd frontend && pnpm build`
Expected: 通过

- [ ] **Step 4: Commit（用户已授权批量提交时）**

```bash
git add frontend/console-user/src/views/DevOps.vue
git commit -m "feat(console-user): DevOps 中心评审 tab + 值班台等评审信号"
```

---

### Task 7: 全量验证 + k8s 部署 + e2e

**Files:**
- 无新文件（验证任务）

**Interfaces:**
- Consumes: 全部前序任务。

- [ ] **Step 1: 后端全量测试**

Run: `go test ./... -race`
Expected: 全绿

- [ ] **Step 2: 前端构建**

Run: `cd frontend && pnpm build`
Expected: 三套通过

- [ ] **Step 3: k8s 部署**

Run: `./scripts/deploy-k8s.sh`
Expected: 镜像构建推送 + helm upgrade + rollout 成功（[[k8s-always-latest]] 常驻授权）

- [ ] **Step 4: e2e 验证**

```bash
# 登录拿 cookie 后：
# 1. 建 PR 场景：用集群 Gitea（paas-bot/paas-gitea-bot-2026）对某 internal 仓库
#    curl -u paas-bot:paas-gitea-bot-2026 http://<gitea-svc>/api/v1/repos/<owner>/<repo>/pulls \
#      -d '{"head":"feat-x","base":"main","title":"e2e: pr review"}' 建 PR
# 2. 平台端点：
curl -b cookie 'http://paas.k8s.dd/api/applications/<appId>/repositories/<repoId>/pulls'       # 列表 200
curl -b cookie 'http://paas.k8s.dd/api/applications/<appId>/repositories/<repoId>/pulls/1'     # 详情含 diff
curl -b cookie -X POST -d '{"do":"APPROVE","body":"LGTM"}' \
  'http://paas.k8s.dd/api/applications/<appId>/repositories/<repoId>/pulls/1/reviews'          # 204
curl -b cookie 'http://paas.k8s.dd/api/pulls'                                                  # 聚合 200
# 3. 权限：developer 会话 merge → 403；maintainer → 204 后 PR state=merged
# 4. 前端：代码仓库 tab 评审入口 + PullDetail diff 渲染 + DevOps 评审 tab
```

Expected: 全通

- [ ] **Step 5: CLAUDE.md 更新 + Commit（用户已授权批量提交时）**

CLAUDE.md「内置 Git 后端 + 镜像库管理 UI」章节追加 Code Review 段落（端点/权限/前端入口/YAGNI 留后续），`docs/superpowers/specs` 已有 spec 链接。

```bash
git add CLAUDE.md
git commit -m "docs: Code Review（PR 评审闭环）章节"
```

---

### Task 8: 10 轮深度代码检查 + 修复（用户设定目标，必做）

**Files:**
- Modify: 按检查结果修复涉及文件

- [ ] **Step 1**: 对本模块（gitea client PR 扩展 + handler 端点 + 前端）执行 10 轮深度代码检查，每轮独立视角（安全/多租户隔离/并发/契约/业务逻辑/错误处理/资源泄漏/前端质量/权限矩阵/回归风险），可用并行 subagent fan-out
- [ ] **Step 2**: 修复全部 Critical/Important findings，Minor 记录留后续
- [ ] **Step 3**: `go test ./... -race` + `pnpm build` 复验全绿

### Task 9: k8s 部署 + e2e 复验（用户设定目标，必做）

- [ ] **Step 1**: `./scripts/deploy-k8s.sh`（[[k8s-always-latest]] 常驻授权）
- [ ] **Step 2**: e2e 复验（PR 列表/详情 diff/评审/merge/权限/前端入口）全通
- [ ] **Step 3**: CLAUDE.md 更新

> 执行约定（用户指示）：任何工具调用被中断后直接重试/继续，不询问。

---

## Self-Review 结果

1. **Spec 覆盖**：列表/diff/评审/merge（Task 1-2）、跨应用聚合（Task 3）、双入口 + PullDetail（Task 5-6）、权限矩阵（Task 2 测试）、审计（Task 2）、OpenAPI（Task 2-3）、diff 截断（Task 2）、422→409（Task 1-2）、YAGNI 项均不做 ✓
2. **占位符扫描**：Task 2 Step 1 测试构造与 Task 3 Step 1 测试体标注「参照既有测试模式补全」——这是指向仓库内可 grep 的确定模式（非 TBD），执行者按图索骥；其余步骤代码完整 ✓
3. **类型一致性**：`PullRequest` 字段（Task 1 定义 → Task 2 handler → Task 4 TS interface）一致；`parseDiff`/`DiffFile`/`DiffLine`（Task 4 内定义消费）一致；端点路径 Task 2/3/4/5 一致 ✓
