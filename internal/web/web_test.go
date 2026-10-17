package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// spaFS fallback 语义（2026-10-13 白屏根因回归锁）：导航路径 fallback index.html、
// 静态资源后缀未命中直通 404（防 HTML 被浏览器当 JS 模块执行抛 SyntaxError）。
func TestSpaFSFallback(t *testing.T) {
	root := fstest.MapFS{
		"index.html":              &fstest.MapFile{Data: []byte("<html>")},
		"assets/app-abc123.js":    &fstest.MapFile{Data: []byte("console.log(1)")},
		"assets/style-abc123.css": &fstest.MapFile{Data: []byte("body{}")},
	}
	s := spaFS{root: root}

	// 命中文件原样返回
	if _, err := s.Open("index.html"); err != nil {
		t.Fatalf("index.html 应命中: %v", err)
	}
	if _, err := s.Open("assets/app-abc123.js"); err != nil {
		t.Fatalf("已存在 chunk 应命中: %v", err)
	}

	// 导航路径（无扩展名/前端路由）fallback 到 index.html
	for _, p := range []string{"devops/agent-runs/pl-1", "applications/app-1/detail"} {
		if _, err := s.Open(p); err != nil {
			t.Fatalf("导航路径 %s 应 fallback index.html: %v", p, err)
		}
	}

	// 静态资源后缀未命中：404（fs.ErrNotExist），绝不 fallback 成 HTML
	for _, p := range []string{
		"assets/AgentRunDetail-oldhash.js", // 部署窗口期失效 chunk（本案）
		"assets/app-oldhash.css",
		"favicon-missing.ico",
		"manifest-old.webmanifest",
	} {
		if _, err := s.Open(p); err == nil {
			t.Fatalf("静态资源 %s 未命中应返回错误（404），不应 fallback HTML", p)
		} else if !errorsIs(err, fs.ErrNotExist) {
			// FileServer 直接用返回的 fs.FS；MapFS.Open 的错误即 fs.ErrNotExist。
			// 非预期错误形态也接受（实现上 Open 原样透传底层错误），但需为「不存在」语义。
			t.Logf("静态资源 %s 返回错误: %v", p, err)
		}
	}
}

// errorsIs 薄封装（避免为单断言引 errors 依赖歧义）。
func errorsIs(err, target error) bool { return err == target || err.Error() == target.Error() }

// 缓存策略回归锁（2026-10-15 发版后用户看到老版本根因）：index.html 无 hash 必须
// no-cache（每次协商，发版即生效），/assets/* content-hash 文件名 immutable 长缓存。
// 此前裸 FileServer 无任何缓存头 → 浏览器对 index.html 启发式缓存 → 旧入口滞留。
func TestCachePolicy(t *testing.T) {
	h := cachePolicy(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	cases := []struct {
		path string
		want string
	}{
		{"/console/", "no-cache"},
		{"/console", "no-cache"},                    // FileServer 会 308 补尾斜杠，头同样要设
		{"/console/applications/app-1", "no-cache"}, // SPA 路由 fallback index.html
		{"/console/favicon.ico", "no-cache"},        // 无 hash 公共资源
		{"/console/assets/index-C4dKF8Xe.js", "public, max-age=31536000, immutable"},
		{"/console/assets/style-abc123.css", "public, max-age=31536000, immutable"},
		{"/assets/index-C4dKF8Xe.js", "public, max-age=31536000, immutable"}, // landing（prefix "/"）
		{"/", "no-cache"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if got := rec.Header().Get("Cache-Control"); got != c.want {
			t.Errorf("%s: Cache-Control = %q, want %q", c.path, got, c.want)
		}
	}
}
