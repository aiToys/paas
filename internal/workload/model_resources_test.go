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
