package gitea

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitea 启动 fake Gitea server，mergeStatus 控制 merge 端点返 200（成功）或 409（冲突）。
// 记录 merge 请求体的 Do 字段供 squash 验证。
func fakeGitea(t *testing.T, mergeStatus int) (*httptest.Server, *Client, *string) {
	t.Helper()
	var gotDo string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
			// 创建 PR -> 201 {number, head.sha}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"number":1,"head":{"sha":"head-sha"}}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/merge"):
			// merge PR -> 记录 Do + 返 mergeStatus
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			if v, ok := body["Do"].(string); ok {
				gotDo = v
			}
			w.WriteHeader(mergeStatus)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/commits"):
			// ListCommits -> 返最新 commit SHA
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `[{"sha":"merge-sha","commit":{"message":"m","author":{"name":"a","date":"d"}}}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, New(srv.URL, "bot", "pass"), &gotDo
}

func TestMergeSuccess(t *testing.T) {
	_, c, _ := fakeGitea(t, http.StatusOK)
	sha, err := c.Merge(context.Background(), "paas-bot", "repo1", "feature", "main", "ff")
	if err != nil {
		t.Fatalf("Merge 成功路径失败: %v", err)
	}
	if sha != "merge-sha" {
		t.Fatalf("mergeSHA 期望 merge-sha（来自 ListCommits），got %q", sha)
	}
}

func TestMergeConflict(t *testing.T) {
	_, c, _ := fakeGitea(t, http.StatusConflict)
	_, err := c.Merge(context.Background(), "paas-bot", "repo1", "feature", "main", "ff")
	if !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("期望 ErrMergeConflict，got %v", err)
	}
}

// TestMergeSquashDoField 验证 squash 模式发 Do=squash（ff 发 Do=merge）。
func TestMergeSquashDoField(t *testing.T) {
	t.Run("squash", func(t *testing.T) {
		_, c, gotDo := fakeGitea(t, http.StatusOK)
		if _, err := c.Merge(context.Background(), "paas-bot", "r", "f", "main", "squash"); err != nil {
			t.Fatal(err)
		}
		if *gotDo != "squash" {
			t.Fatalf("squash 模式 Do 期望 squash，got %q", *gotDo)
		}
	})
	t.Run("ff", func(t *testing.T) {
		_, c, gotDo := fakeGitea(t, http.StatusOK)
		if _, err := c.Merge(context.Background(), "paas-bot", "r", "f", "main", "ff"); err != nil {
			t.Fatal(err)
		}
		if *gotDo != "merge" {
			t.Fatalf("ff 模式 Do 期望 merge，got %q", *gotDo)
		}
	})
}

// TestMergeUnavailable 验证 baseURL 空时降级 ErrGiteaUnavailable。
func TestMergeUnavailable(t *testing.T) {
	c := New("", "bot", "pass")
	_, err := c.Merge(context.Background(), "paas-bot", "r", "f", "main", "ff")
	if !errors.Is(err, ErrGiteaUnavailable) {
		t.Fatalf("期望 ErrGiteaUnavailable，got %v", err)
	}
}

// fakeGitea 分支 API 模拟：记录调用 + 可注入状态码。
// 既有测试若已有 fake server helper，复用其模式；否则新建。
func TestBranchAPIs(t *testing.T) {
	// CreateBranch 成功（201）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/branches"):
			w.WriteHeader(http.StatusCreated)
		case r.Method == "GET" && strings.Contains(r.URL.Path, "/branches/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name": "feat/x", "commit": map[string]any{"id": "abc123"},
			})
		case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/branches/"):
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "paas-bot", "pw")

	if err := c.CreateBranch(context.Background(), "paas-bot", "app-1", "feat/x", "main"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	b, err := c.GetBranch(context.Background(), "paas-bot", "app-1", "feat/x")
	if err != nil || b.Name != "feat/x" || b.CommitSHA != "abc123" {
		t.Fatalf("GetBranch: %+v err=%v", b, err)
	}
	if err := c.DeleteBranch(context.Background(), "paas-bot", "app-1", "feat/x"); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
}

func TestBranchAPIErrors(t *testing.T) {
	// CreateBranch 422 -> ErrBranchExists；GetBranch 404 -> ErrBranchNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(srv.URL, "paas-bot", "pw")

	if err := c.CreateBranch(context.Background(), "o", "r", "b", "main"); err != ErrBranchExists {
		t.Fatalf("CreateBranch 422 期望 ErrBranchExists, got %v", err)
	}
	if _, err := c.GetBranch(context.Background(), "o", "r", "b"); err != ErrBranchNotFound {
		t.Fatalf("GetBranch 404 期望 ErrBranchNotFound, got %v", err)
	}
}
