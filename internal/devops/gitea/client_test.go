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
			io.WriteString(w, `{"number":1,"head":{"sha":"head-sha"}}`)
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
			io.WriteString(w, `[{"sha":"merge-sha","commit":{"message":"m","author":{"name":"a","date":"d"}}}]`)
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
