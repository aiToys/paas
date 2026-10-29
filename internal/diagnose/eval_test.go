package diagnose

import (
	"strings"
	"testing"
)

// 评测集结构完整性：≥15 条、ID 唯一、category 合法、关键词非空、目标非空。
// 真跑（Top-1/Top-3 命中率）在集群验收执行（fake 环境测不出诊断质量）。
func TestEvalCasesSanity(t *testing.T) {
	cases := EvalCases()
	if len(cases) < 15 {
		t.Fatalf("评测集应 ≥15 条（spec §7），got %d", len(cases))
	}
	validCat := map[string]bool{
		"code_bug": true, "config_error": true, "resource_exhaustion": true,
		"dependency_failure": true, "infra": true,
	}
	ids := map[string]bool{}
	for _, c := range cases {
		if ids[c.ID] {
			t.Errorf("用例 ID 重复: %s", c.ID)
		}
		ids[c.ID] = true
		if !validCat[c.ExpectedCategory] {
			t.Errorf("%s category 非法: %q", c.ID, c.ExpectedCategory)
		}
		if len(c.ExpectedKeywords) == 0 {
			t.Errorf("%s 缺判定关键词", c.ID)
		}
		if c.Target.Namespace == "" && c.Target.Workload == "" {
			t.Errorf("%s 目标为空", c.ID)
		}
		if c.Source == "" {
			t.Errorf("%s 缺来源记录", c.ID)
		}
	}
	// category 覆盖：评测集声称覆盖 §3 全部 category。
	got := map[string]bool{}
	for _, c := range cases {
		got[c.ExpectedCategory] = true
	}
	for cat := range validCat {
		if !got[cat] {
			t.Errorf("评测集未覆盖 category %q", cat)
		}
	}
}

// Grade 判定语义：Top-1/Top-3/关键词命中（不依赖真实模型，锁判定器本身）。
func TestGradeSemantics(t *testing.T) {
	c := EvalCases()[0] // EV-01 乐观锁，关键词含 "conflict"
	rep := Report{Verdict: Verdict{Category: "code_bug"}}
	rep.RootCauseHypotheses = []RootCauseHypothesis{
		{Hypothesis: "reconcile 乐观锁 conflict", Evidence: []Evidence{{Detail: "Operation cannot be fulfilled"}}},
	}
	v := Grade(c, rep)
	if !v.HitCategory || !v.HitTop1 || !v.HitTop3 {
		t.Fatalf("应全命中: %+v", v)
	}

	// Top-3 而非 Top-1。
	rep2 := Report{Verdict: Verdict{Category: "code_bug"}}
	rep2.RootCauseHypotheses = []RootCauseHypothesis{
		{Hypothesis: "无关假设"},
		{Hypothesis: "resourceVersion has been modified 冲突"},
	}
	v2 := Grade(c, rep2)
	if v2.HitTop1 || !v2.HitTop3 {
		t.Fatalf("Top-3 命中语义错: %+v", v2)
	}

	// category 不符。
	rep3 := rep2
	rep3.Verdict = Verdict{Category: "infra"}
	v3 := Grade(c, rep3)
	if v3.HitCategory || v3.HitTop1 {
		t.Fatalf("category 不应命中: %+v", v3)
	}
}

func TestGradeSummaryString(t *testing.T) {
	s := GradeSummary{Total: 15, Top1Hits: 11, Top3Hits: 14, CatHits: 12}.String()
	if !strings.Contains(s, "top1=11(73%)") || !strings.Contains(s, "top3=14(93%)") {
		t.Fatalf("汇总格式错: %s", s)
	}
}
