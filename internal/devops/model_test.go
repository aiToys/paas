package devops

import "testing"

func TestReleaseLaneAndSourceRunFields(t *testing.T) {
	r := Release{LaneID: "feature-x", SourceRunID: "run-abc"}
	if r.LaneID != "feature-x" || r.SourceRunID != "run-abc" {
		t.Error("Release 新字段未生效")
	}
}

// TestBaselineWorkloadName 验证基线 Workload 命名规则（CreateRelease 新建时用）。
func TestBaselineWorkloadName(t *testing.T) {
	cases := []struct{ app, svc, lane, want string }{
		{"app-cs", "", "default", "app-cs-svc"},                            // 单服务基线（向后兼容）
		{"app-cs", "", "", "app-cs-svc"},                                   // 空 lane 同 default
		{"app-cs", "", "feature-x", "app-cs-svc-feature-x"},                // 单服务泳道
		{"paas-shop", "product", "default", "paas-shop-product-svc"},       // 多服务基线
		{"paas-shop", "recommend", "main", "paas-shop-recommend-svc-main"}, // 多服务泳道
	}
	for _, c := range cases {
		if got := BaselineWorkloadName(c.app, c.svc, c.lane); got != c.want {
			t.Errorf("BaselineWorkloadName(%q,%q,%q)=%q, want %q", c.app, c.svc, c.lane, got, c.want)
		}
	}
}
