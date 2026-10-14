package observability

import "testing"

// validRule 返回一份全字段合法的规则基线（各用例按需破坏单字段）。
func validRule() AlertRule {
	return AlertRule{
		Name: "CPU 高", MetricName: MetricCPU, TargetType: TargetApp,
		Operator: OpGT, Threshold: 80, Severity: SeverityCritical, Enabled: true,
	}
}

// TestAlertRuleValidate：合法基线通过；各字段非法/未知值被拒（含 targetType/metricName
// 合法集合收口——2026-09-25 告警审计：收口前 API 可直配任意值，real 模式静默失效）。
func TestAlertRuleValidate(t *testing.T) {
	if err := validRule().Validate(); err != nil {
		t.Fatalf("合法规则不应报错: %v", err)
	}
	for name, mutate := range map[string]func(*AlertRule){
		"空 name":        func(r *AlertRule) { r.Name = "" },
		"空 metricName":  func(r *AlertRule) { r.MetricName = "" },
		"未知 metricName": func(r *AlertRule) { r.MetricName = "not_a_metric" },
		"空 targetType":  func(r *AlertRule) { r.TargetType = "" },
		"未知 targetType": func(r *AlertRule) { r.TargetType = "cluster" },
		"env 暂不支持":      func(r *AlertRule) { r.TargetType = TargetEnv },
		"非法 operator":   func(r *AlertRule) { r.Operator = "!=" },
		"非法 severity":   func(r *AlertRule) { r.Severity = "info" },
	} {
		r := validRule()
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s 应被拒绝", name)
		}
	}
	// 数据服务业务指标（前端曾不暴露、但属于合法集合——能力曝光对账后补齐）。
	for _, m := range []string{MetricConnections, MetricQPS, MetricHitRate, MetricLag, MetricVectors, MetricDiskUsage, MetricDiskIO, MetricNetIO} {
		r := validRule()
		r.MetricName = m
		r.TargetType = TargetDataservice
		if err := r.Validate(); err != nil {
			t.Errorf("数据服务指标 %s 应合法: %v", m, err)
		}
	}
	// workload 维度合法（real 已补指标源，2026-09-25）。
	r := validRule()
	r.TargetType = TargetWorkload
	if err := r.Validate(); err != nil {
		t.Errorf("workload 维度应合法: %v", err)
	}
}

// TestDefaultAlertRulesValid：默认规则集必须全部通过 Validate
// （元防护：合法集合收口后若默认集自身含非法值，启动 seed 会逐条失败——开箱零规则回归）。
func TestDefaultAlertRulesValid(t *testing.T) {
	rules := DefaultAlertRules()
	if len(rules) == 0 {
		t.Fatal("默认规则集不应为空")
	}
	for _, r := range rules {
		if err := r.Validate(); err != nil {
			t.Errorf("默认规则 %q 未过 Validate: %v", r.Name, err)
		}
	}
}
