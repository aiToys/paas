package configcenter

import (
	"testing"
)

// TestMergeSnapshot 覆盖语义：覆盖项覆盖基线、新 key 追加、无覆盖原样返回基线。
func TestMergeSnapshot(t *testing.T) {
	base := map[string]string{"a": "1", "b": "2"}
	ovs := []LaneOverride{{Key: "b", Value: "override"}, {Key: "c", Value: "new"}}
	got := MergeSnapshot(base, ovs)
	if got["a"] != "1" || got["b"] != "override" || got["c"] != "new" {
		t.Fatalf("merge 结果错误: %v", got)
	}
	if len(got) != 3 {
		t.Fatalf("应 3 项: %v", got)
	}
	// 基线 map 不被修改（深拷隔离）。
	if base["b"] != "2" {
		t.Fatal("基线 map 不应被修改")
	}
	// 无覆盖：返回基线等值副本。
	only := MergeSnapshot(base, nil)
	if len(only) != 2 || only["a"] != "1" {
		t.Fatalf("无覆盖应等值基线: %v", only)
	}
}

// TestOverrideHash hash 稳定性：排序 key=value 串 FNV-1a；无覆盖返空；覆盖变化 hash 变化。
func TestOverrideHash(t *testing.T) {
	if OverrideHash(nil) != "" || OverrideHash([]LaneOverride{}) != "" {
		t.Fatal("无覆盖应返空串")
	}
	a := []LaneOverride{{Key: "k1", Value: "v1"}, {Key: "k2", Value: "v2"}}
	b := []LaneOverride{{Key: "k2", Value: "v2"}, {Key: "k1", Value: "v1"}}
	if OverrideHash(a) == "" {
		t.Fatal("有覆盖 hash 非空")
	}
	if OverrideHash(a) != OverrideHash(b) {
		t.Fatal("顺序无关：同集合应同 hash")
	}
	c := []LaneOverride{{Key: "k1", Value: "changed"}, {Key: "k2", Value: "v2"}}
	if OverrideHash(a) == OverrideHash(c) {
		t.Fatal("值变化 hash 应变")
	}
}

// TestLaneOverrideValidate 基本校验（handler 写路径前置）：appID/laneID/key 非空。
func TestLaneOverrideValidate(t *testing.T) {
	cases := []LaneOverride{
		{LaneID: "feat", Key: "k"},            // 缺 appID
		{AppID: "a", Key: "k"},                // 缺 laneID
		{AppID: "a", LaneID: "feat"},          // 缺 key
		{AppID: "a", LaneID: "feat", Key: "k"}, // 合法
	}
	if cases[0].Validate() == nil || cases[1].Validate() == nil || cases[2].Validate() == nil {
		t.Fatal("缺字段应拒")
	}
	if cases[3].Validate() != nil {
		t.Fatal("合法覆盖不应报错")
	}
}

// TestMergeSnapshot3 三层 merge 优先级：shared（引用顺序后者覆盖前者）→ 基线 → lane，右者胜。
func TestMergeSnapshot3(t *testing.T) {
	shared := []SharedLayer{
		{NSID: "ns-s1", Snapshot: map[string]string{"a": "s1", "b": "s1", "c": "s1"}},
		{NSID: "ns-s2", Snapshot: map[string]string{"b": "s2", "d": "s2"}},
	}
	base := map[string]string{"a": "base", "e": "base"}
	ovs := []LaneOverride{{Key: "a", Value: "lane"}}
	got := MergeSnapshot3(shared, base, ovs)
	want := map[string]string{"a": "lane", "b": "s2", "c": "s1", "d": "s2", "e": "base"}
	if len(got) != len(want) {
		t.Fatalf("应 %d 项，实得 %v", len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("key=%s want %s got %s", k, v, got[k])
		}
	}
	// 输入不被修改（拷贝隔离）
	if base["a"] != "base" || shared[0].Snapshot["a"] != "s1" {
		t.Fatal("merge 污染了输入层")
	}
}

// TestSharedHash 指纹：无引用空串；version 变化 hash 变；顺序无关。
func TestSharedHash(t *testing.T) {
	if SharedHash(nil) != "" {
		t.Fatal("无引用应空串")
	}
	l1 := []SharedLayer{{NSID: "a", Version: 1}, {NSID: "b", Version: 3}}
	l2 := []SharedLayer{{NSID: "b", Version: 3}, {NSID: "a", Version: 1}} // 顺序无关
	if SharedHash(l1) != SharedHash(l2) {
		t.Fatal("顺序无关")
	}
	l1[0].Version = 2 // shared 重发布
	if SharedHash(l1) == SharedHash(l2) {
		t.Fatal("version 变化应改变 hash")
	}
}
