package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aitoys/paas/internal/ai/tool"
)

// fakeMCPServer 模拟 MCP server（JSON-RPC over HTTP）：tools/list 返回两个工具
// （first_tool 在前、target_tool 在后），tools/call 记录实际收到的工具名。
// 用于锁住 buildTools 按实体名精确匹配的语义。
type fakeMCPServer struct {
	mu       sync.Mutex
	lastCall string
}

func (f *fakeMCPServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("解析 MCP 请求失败: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"protocolVersion": "2025-06-18"},
			})
		case "tools/list":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"tools": []map[string]any{
					{"name": "first_tool", "description": "server 第一个工具"},
					{"name": "target_tool", "description": "被测目标工具",
						"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}},
				}},
			})
		case "tools/call":
			var p struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(req.Params, &p)
			f.mu.Lock()
			f.lastCall = p.Name
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"result": map[string]any{"content": []map[string]any{{"type": "text", "text": "ok:" + p.Name}}},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req.ID,
				"error": map[string]any{"code": -32601, "message": "unknown method"},
			})
		}
	}
}

// fakeToolRepo 仅实现 buildTools 用到的 Get。
type fakeToolRepo struct{ t tool.Tool }

func (f *fakeToolRepo) Get(context.Context, string) (tool.Tool, error) { return f.t, nil }
func (f *fakeToolRepo) List(context.Context) ([]tool.Tool, error)      { panic("unused") }
func (f *fakeToolRepo) ListAll(context.Context) ([]tool.Tool, error)   { panic("unused") }
func (f *fakeToolRepo) Create(context.Context, tool.Tool) (tool.Tool, error) {
	panic("unused")
}
func (f *fakeToolRepo) Update(context.Context, tool.Tool) (tool.Tool, error) {
	panic("unused")
}
func (f *fakeToolRepo) Delete(context.Context, string) error    { panic("unused") }
func (f *fakeToolRepo) ToolsCount(context.Context) (int, error) { panic("unused") }

// TestBuildToolsExactMatch 锁住 R11-C1：工具实体名匹配 MCP server 的**第二个**工具时，
// invoker 必须调 target_tool（原实现 `|| mcpName == t.Name` 恒真短路，
// 任何实体都被改写为调 server 第一个工具——多工具 server 全部串调 tools[0]）。
func TestBuildToolsExactMatch(t *testing.T) {
	srv := &fakeMCPServer{}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	rt := &Runtime{tools: &fakeToolRepo{t: tool.Tool{
		ID: "tl-1", Name: "target_tool", Type: tool.TypeMCP, Enabled: true,
		Config: map[string]string{tool.CfgMCPServerURL: ts.URL},
	}}}
	defs, invokers, err := rt.buildTools(context.Background(), Agent{Tools: []string{"tl-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || len(invokers) != 1 {
		t.Fatalf("应构建 1 个工具定义，got defs=%d invokers=%d", len(defs), len(invokers))
	}
	// schema 应取 target_tool 的 inputSchema（含 q 属性），而非默认 permissive schema。
	props, ok := defs[0].Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties 应为 map，got %v", defs[0].Function.Parameters)
	}
	if _, hasQ := props["q"]; !hasQ {
		t.Fatalf("schema 应为 target_tool 的真实 schema（含 q 属性），got %v", defs[0].Function.Parameters)
	}
	out, err := invokers["target_tool"](context.Background(), `{"q":"hi"}`)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	srv.mu.Lock()
	called := srv.lastCall
	srv.mu.Unlock()
	if called != "target_tool" {
		t.Fatalf("invoker 应调 target_tool，实际调了 %q（串调第一个工具回归）", called)
	}
	if !strings.Contains(out, "ok:target_tool") {
		t.Fatalf("工具结果应透传，got %q", out)
	}
}

// TestBuildToolsNameMismatchKeepsEntityName 匹配失败（实体名不在 server 工具列表）：
// 保持实体名直调（MCP 端报 unknown tool，调用方见真实错误），不得改写为第一个工具。
func TestBuildToolsNameMismatchKeepsEntityName(t *testing.T) {
	srv := &fakeMCPServer{}
	ts := httptest.NewServer(srv.handler(t))
	defer ts.Close()

	rt := &Runtime{tools: &fakeToolRepo{t: tool.Tool{
		ID: "tl-2", Name: "ghost_tool", Type: tool.TypeMCP, Enabled: true,
		Config: map[string]string{tool.CfgMCPServerURL: ts.URL},
	}}}
	_, _, err := rt.buildTools(context.Background(), Agent{Tools: []string{"tl-2"}})
	if err == nil || !strings.Contains(err.Error(), "未在 MCP 服务暴露") {
		t.Fatalf("未知方法必须在调用前阻断，got %v", err)
	}
	srv.mu.Lock()
	called := srv.lastCall
	srv.mu.Unlock()
	if called != "" {
		t.Fatalf("不应调用未知方法: %s", called)
	}
}
