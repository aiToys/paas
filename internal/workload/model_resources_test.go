package workload

import "testing"

// 资源规格校验（Task 3）：Quantity 格式 + IsEmpty。
func TestValidateResourcesQuantity(t *testing.T) {
	base := func() Workload {
		return Workload{ID: "wl-1", AppID: "app-1", EnvID: "env-1", Type: TypeService,
			Name: "wl-1", Image: "nginx"}
	}
	// 合法 Quantity
	for _, v := range []string{"500m", "2", "512Mi", "1Gi", "100m", "0.5"} {
		w := base()
		w.Resources = ResourceSpec{CPURequest: v}
		if err := w.Validate(); err != nil {
			t.Errorf("CPURequest=%q 应合法: %v", v, err)
		}
	}
	// 非法 Quantity
	for _, v := range []string{"abc", "1X", "m500", "1.2.3"} {
		w := base()
		w.Resources = ResourceSpec{MemLimit: v}
		if err := w.Validate(); err == nil {
			t.Errorf("MemLimit=%q 应非法", v)
		}
	}
}

// metricsPath 校验：空合法（默认不采集）；非空须以 / 开头的绝对路径。
// 写成 "metrics" 会被拼成相对路径静默 404，与「不声明」难区分，故在入口拦下。
func TestValidateMetricsPath(t *testing.T) {
	base := func() Workload {
		return Workload{ID: "wl-1", AppID: "app-1", EnvID: "env-1", Type: TypeService,
			Name: "wl-1", Image: "nginx"}
	}
	w := base()
	if err := w.Validate(); err != nil {
		t.Errorf("metricsPath 空应合法（默认不采集）: %v", err)
	}
	for _, v := range []string{"/metrics", "/actuator/prometheus", "/"} {
		w := base()
		w.MetricsPath = v
		if err := w.Validate(); err != nil {
			t.Errorf("metricsPath=%q 应合法: %v", v, err)
		}
	}
	for _, v := range []string{"metrics", "http://x/metrics", " /metrics"} {
		w := base()
		w.MetricsPath = v
		if err := w.Validate(); err == nil {
			t.Errorf("metricsPath=%q 应非法（须绝对路径）", v)
		}
	}
}

func TestResourceSpecIsEmpty(t *testing.T) {
	var zero ResourceSpec
	if !zero.IsEmpty() {
		t.Fatal("零值应 IsEmpty")
	}
	nonZero := ResourceSpec{CPURequest: "500m"}
	if nonZero.IsEmpty() {
		t.Fatal("非零不应 IsEmpty")
	}
}
