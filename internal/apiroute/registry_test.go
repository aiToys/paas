package apiroute

import (
	"testing"
)

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
