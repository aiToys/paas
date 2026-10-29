package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/apiroute"
)

// TestAssistantOpenAPIContract 不启动 core 或集群，验证用户端助手的真实装配契约。
// PAAS_EXPORT_ASSISTANT_SPEC 仅供本地生成 TypeScript 类型，日常测试不写文件。
func TestAssistantOpenAPIContract(t *testing.T) {
	mux := http.NewServeMux()
	reg := apiroute.New(mux, apiroute.Info{Title: "PaaS AI Assistant", Version: "1"})
	registerAssistantRoutes(mux, reg, http.NotFoundHandler(), func(h http.Handler) http.Handler { return h })
	recorder := httptest.NewRecorder()
	apiroute.ServeSpec(reg).ServeHTTP(recorder, httptest.NewRequest("GET", "/openapi.json", nil))
	var spec struct {
		Paths map[string]map[string]struct {
			Responses map[string]json.RawMessage `json:"responses"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}
	if len(spec.Paths) < 8 {
		t.Fatal("助手路由未完整登记")
	}
	for path, methods := range spec.Paths {
		for method, op := range methods {
			if len(op.Responses) == 0 {
				t.Fatalf("%s %s 缺响应契约", method, path)
			}
		}
	}
	if path := os.Getenv("PAAS_EXPORT_ASSISTANT_SPEC"); path != "" {
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		root := filepath.Clean(filepath.Join(cwd, "..", "..", "tmp"))
		out, err := filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(root, out)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatal("契约仅可导出到项目 tmp 目录")
		}
		// #nosec G703 -- 已验证仅写项目 tmp，且输入来自显式测试环境变量，不来自 HTTP 请求。
		if err := os.WriteFile(out, recorder.Body.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
