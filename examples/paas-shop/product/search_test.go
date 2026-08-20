package main

import (
	"reflect"
	"testing"
)

func TestBuildSearchQueryAll(t *testing.T) {
	// 无过滤：全量，limit 生效
	q, args := buildSearchQuery("", "", 20)
	want := "SELECT id,name,price,category,stock,description,image_url,created_at FROM products ORDER BY created_at DESC LIMIT $1"
	if q != want {
		t.Fatalf("SQL:\n got: %s\nwant: %s", q, want)
	}
	if !reflect.DeepEqual(args, []any{20}) {
		t.Fatalf("args = %v, want [20]", args)
	}
}

func TestBuildSearchQueryKeyword(t *testing.T) {
	// 关键字：name ILIKE，参数化（防注入）
	q, args := buildSearchQuery("键", "", 20)
	if !contains(q, "name ILIKE $1") {
		t.Fatalf("应含 name ILIKE $1，got %s", q)
	}
	if len(args) != 2 || args[0] != "%键%" || args[1] != 20 {
		t.Fatalf("args 应为 [%%键%%, 20]，got %v", args)
	}
}

func TestBuildSearchQueryCategoryAndKeyword(t *testing.T) {
	q, args := buildSearchQuery("鼠", "外设", 50)
	if !contains(q, "name ILIKE $1") || !contains(q, "category = $2") {
		t.Fatalf("应同时含 name ILIKE + category，got %s", q)
	}
	if len(args) != 3 || args[0] != "%鼠%" || args[1] != "外设" || args[2] != 50 {
		t.Fatalf("args = %v", args)
	}
}

func TestBuildSearchQueryOnlyCategory(t *testing.T) {
	q, args := buildSearchQuery("", "音频", 20)
	if !contains(q, "category = $1") {
		t.Fatalf("应含 category = $1，got %s", q)
	}
	if args[0] != "音频" {
		t.Fatalf("args[0] = %v, want 音频", args[0])
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOfStr(s, sub) >= 0)
}

func indexOfStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
