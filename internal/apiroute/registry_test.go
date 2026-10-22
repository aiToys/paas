package apiroute

import (
	"testing"
)

func TestRawResponseDoesNotChangeCRUDEnvelope(t *testing.T) {
	r := New(nil, Info{})
	var rt Route
	WithRawResp("text/markdown", "")(&rt)
	response := r.buildOperation(rt).Responses[200]
	if response.Content["text/markdown"].Schema.Type != "string" {
		t.Fatal("Markdown 应为原始字符串")
	}
	if _, ok := response.Content["application/json"]; ok {
		t.Fatal("导出不应假称 JSON")
	}
	WithResp("")(&rt)
	if r.buildOperation(rt).Responses[200].Content["application/json"].Schema.Properties["data"].Type != "string" {
		t.Fatal("普通响应仍需 data 包裹")
	}
}

func TestRegisteredPaths(t *testing.T) {
	reg := New(nil, Info{Title: "t", Version: "1"})
	reg.Operation("GET", "/api/applications", Summary("x"))
	reg.Operation("GET", "/api/applications/{id}", Summary("x"))
	reg.Operation("POST", "/v1/chat/completions", Summary("x"))

	paths := reg.RegisteredPaths()

	// 去重 + 排序
	want := []string{"/api/applications", "/api/applications/{id}", "/v1/chat/completions"}
	if len(paths) != len(want) {
		t.Fatalf("期望 %d 条路径，实际 %d: %v", len(want), len(paths), paths)
	}
	for i, p := range paths {
		if p != want[i] {
			t.Fatalf("paths[%d]=%q, want %q", i, p, want[i])
		}
	}
}
