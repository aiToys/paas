package main

import "testing"

func TestBuildSummaryJSON(t *testing.T) {
	summary := map[string]CategoryStat{
		"外设": {Count: 2, Stock: 300},
		"音频": {Count: 1, Stock: 80},
	}
	total := 3
	out := buildSummaryJSON(summary, total)
	if out["total"] != total {
		t.Fatalf("total = %v, want 3", out["total"])
	}
	cats, ok := out["categories"].(map[string]CategoryStat)
	if !ok {
		t.Fatalf("categories 类型错误: %T", out["categories"])
	}
	if cats["外设"].Count != 2 || cats["外设"].Stock != 300 {
		t.Fatalf("categories[外设] = %v, want {2 300}", cats["外设"])
	}
	if out["at"] == "" {
		t.Fatalf("at 不应为空")
	}
}
