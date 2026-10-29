package diagnose

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseReportHappyPath(t *testing.T) {
	raw := `{"verdict":{"severity":"warning","category":"code_bug","summary":"乐观锁冲突"},
		"rootCauseHypotheses":[{"hypothesis":"reconcile 乐观锁冲突","confidence":0.8,
		"evidence":[{"source":"query_loki","detail":"近1h 22次冲突报错","weight":"high"}]}],
		"recommendedActions":[{"action":"apply 前重新 GET","type":"code","risk":"low","requiresApproval":true}],
		"dataSourceCoverage":{"loki":"ok"},"confidenceNote":"jaeger 缺失"}`
	rep := ParseReport(raw)
	if rep.Verdict.Category != "code_bug" || len(rep.RootCauseHypotheses) != 1 || len(rep.RecommendedActions) != 1 {
		t.Fatalf("正常报告应完整解析: %+v", rep)
	}
	if rep.RootCauseHypotheses[0].Evidence[0].Source != "query_loki" {
		t.Fatalf("证据应保留, got %+v", rep.RootCauseHypotheses[0])
	}
}

func TestParseReportFencedJSON(t *testing.T) {
	raw := "```json\n{\"verdict\":{\"severity\":\"info\",\"category\":\"unknown\",\"summary\":\"x\"},\"rootCauseHypotheses\":[],\"recommendedActions\":[]}\n```"
	rep := ParseReport(raw)
	if rep.Verdict.Severity != "info" || rep.RawOutput != "" {
		t.Fatalf("围栏 JSON 应剥掉解析成功（非降级）: %+v", rep)
	}
}

// 真实集群首跑形态：中文推理正文在前 + ```json 围栏报告在后（曾致降级）。
func TestParseReportProseThenFence(t *testing.T) {
	raw := "我会先并行采集首轮信号。发现两个真实信号 {杂散}。确认范围。\n\n```json\n{\"verdict\":{\"severity\":\"warning\",\"category\":\"code_bug\",\"summary\":\"s\"},\"rootCauseHypotheses\":[{\"hypothesis\":\"h\",\"confidence\":0.8,\"evidence\":[{\"source\":\"get_pod_logs\",\"detail\":\"d\",\"weight\":\"high\"}]}],\"recommendedActions\":[]}\n```"
	rep := ParseReport(raw)
	if rep.Verdict.Category != "code_bug" || len(rep.RootCauseHypotheses) != 1 {
		t.Fatalf("正文+围栏混排应提取围栏 JSON: %+v", rep)
	}
}

// 无围栏：正文后直接落裸 JSON（紧凑与 pretty 两种形态；pretty 形态来自集群二跑实测）。
func TestParseReportProseThenBareJSON(t *testing.T) {
	compact := "分析完毕，结论如下：{\"verdict\":{\"severity\":\"info\",\"category\":\"infra\",\"summary\":\"s\"},\"rootCauseHypotheses\":[],\"recommendedActions\":[]}"
	if rep := ParseReport(compact); rep.Verdict.Category != "infra" {
		t.Fatalf("紧凑裸 JSON 应经锚点提取成功: %+v", rep)
	}
	pretty := "我正在确认这次发版是否真的解决了冲突。{\n  \"verdict\": {\n    \"severity\": \"info\",\n    \"category\": \"infra\",\n    \"summary\": \"s\"\n  },\n  \"rootCauseHypotheses\": [],\n  \"recommendedActions\": []\n}"
	if rep := ParseReport(pretty); rep.Verdict.Category != "infra" {
		t.Fatalf("pretty 裸 JSON 应经锚点提取成功: %+v", rep)
	}
}

func TestParseReportDegradedOnGarbage(t *testing.T) {
	rep := ParseReport("这不是 JSON，是我自由发挥的结论")
	if rep.Verdict.Severity != "info" || len(rep.RootCauseHypotheses) != 0 {
		t.Fatalf("垃圾输入应降级报告, got %+v", rep)
	}
	if !strings.Contains(rep.ConfidenceNote, "不可解析") || rep.RawOutput == "" {
		t.Fatalf("降级报告应说明原因并保留原始输出: %+v", rep)
	}
}

func TestParseReportDropsEvidencelessHypotheses(t *testing.T) {
	raw := `{"verdict":{"severity":"warning","category":"infra","summary":"s"},
		"rootCauseHypotheses":[
			{"hypothesis":"有证据","confidence":0.9,"evidence":[{"source":"get_k8s_events","detail":"OOMKilled","weight":"high"}]},
			{"hypothesis":"无证据猜测","confidence":0.5,"evidence":[]}
		],"recommendedActions":[]}`
	rep := ParseReport(raw)
	if len(rep.RootCauseHypotheses) != 1 {
		t.Fatalf("无证据假设应剔除, got %d 条", len(rep.RootCauseHypotheses))
	}
	if !strings.Contains(rep.ConfidenceNote, "1 条无证据假设") {
		t.Fatalf("剔除应写进 confidenceNote, got %q", rep.ConfidenceNote)
	}
}

func TestRequestNormalize(t *testing.T) {
	req := Request{Target: DiagTarget{Namespace: "paas", Workload: "paas-core"}}
	if err := req.Normalize(); err != nil {
		t.Fatal(err)
	}
	if req.Trigger != "manual" || req.TimeWindow.LookbackMinutes != 30 || req.Target.Service != "paas-core" {
		t.Fatalf("默认值应补齐: %+v", req)
	}
	bad := Request{Target: DiagTarget{Namespace: "paas"}}
	if err := bad.Normalize(); err == nil {
		t.Fatal("无 workload/pod/service 应报错")
	}
	if _, err := json.Marshal(UserPrompt(req)); err != nil {
		t.Fatal(err)
	}
}
