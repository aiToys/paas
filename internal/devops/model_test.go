package devops

import (
	"strings"
	"testing"
)

func TestReleaseLaneAndSourceRunFields(t *testing.T) {
	r := Release{LaneID: "feature-x", SourceRunID: "run-abc"}
	if r.LaneID != "feature-x" || r.SourceRunID != "run-abc" {
		t.Error("Release 新字段未生效")
	}
}

// TestBaselineWorkloadName 验证基线 Workload 命名规则（CreateRelease 新建时用）。
func TestBaselineWorkloadName(t *testing.T) {
	cases := []struct{ app, env, svc, lane, want string }{
		{"app-cs", "", "", "default", "app-cs-svc"},                            // 单服务基线（env 空=不加段，兼容）
		{"app-cs", "", "", "", "app-cs-svc"},                                   // 空 lane 同 default
		{"app-cs", "", "", "feature-x", "app-cs-svc-feature-x"},                // 单服务泳道
		{"paas-shop", "", "product", "default", "paas-shop-product-svc"},       // 多服务基线
		{"paas-shop", "", "recommend", "main", "paas-shop-recommend-svc-main"}, // 多服务泳道
	}
	for _, c := range cases {
		if got := BaselineWorkloadName(c.app, c.env, c.svc, c.lane); got != c.want {
			t.Errorf("BaselineWorkloadName(%q,%q,%q,%q)=%q, want %q", c.app, c.env, c.svc, c.lane, got, c.want)
		}
	}
}

// TestBaselineWorkloadNameEnvSegment default 基线名含 env 短段：双环境基线不再同名互撞
// （同租户单 K8s ns 名全局唯一，canary promote 实测 test/prod 基线重名冲突）。
func TestBaselineWorkloadNameEnvSegment(t *testing.T) {
	test := BaselineWorkloadName("app-cs", "env-acme-test", "web", "default")
	prod := BaselineWorkloadName("app-cs", "env-acme-prod-bj", "web", "default")
	if test == prod {
		t.Fatalf("双环境基线同名: %q", test)
	}
	if test != BaselineWorkloadName("app-cs", "env-acme-test", "web", "default") {
		t.Fatal("命名非确定性（同 env 应同名以复用查找语义）")
	}
	again := BaselineWorkloadName("app-cs", "", "web", "default")
	if again != "app-cs-web-svc" {
		t.Fatalf("env 空=不加段（向后兼容）: %q", again)
	}
}

// TestBaselineWorkloadNameDNS1035 集成分支 lane（含 /）清洗为合法 K8s Service 名（e2e 实测修复）。
func TestBaselineWorkloadNameDNS1035(t *testing.T) {
	cases := map[string]string{
		"app-cs|":                          "app-cs-svc",
		"app-cs|default":                   "app-cs-svc",
		"paas-shop|feature-x":              "paas-shop-svc-feature-x",
		"paas-shop|integration/20260815-1": "paas-shop-svc-integration-20260815-1",
	}
	for in, want := range cases {
		parts := strings.SplitN(in, "|", 2)
		if got := BaselineWorkloadName(parts[0], "", "", parts[1]); got != want {
			t.Fatalf("BaselineWorkloadName(%q) = %q, want %q", in, got, want)
		}
	}
}
