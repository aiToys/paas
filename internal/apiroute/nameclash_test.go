package apiroute

import (
	"net/http"
	"testing"

	"github.com/aitoys/paas/internal/diagnose"
	"github.com/aitoys/paas/internal/diagnose/scoped"
)

// 跨包同名类型分离登记：先注册者保短名，后来者用「包.类型」限定名——
// 否则后者静默覆盖前者契约（diagnose.Evidence vs scoped.Evidence 实锤案例：
// /api/assistant 的 Evidence schema 自上线起一直被 admin 诊断版覆盖）。
func TestCrossPackageNameClashDisambiguated(t *testing.T) {
	r := New(http.NewServeMux(), Info{})
	r.Operation("GET", "/a", Tags("t"), WithResp(diagnose.Evidence{}))
	r.Operation("GET", "/b", Tags("t"), WithResp(scoped.Evidence{}))
	schemas := r.Document().Components.Schemas
	// 先注册者保短名，且字段是 admin 诊断版（detail/source/weight）。
	first, ok := schemas["Evidence"]
	if !ok || first.Properties["detail"] == nil {
		t.Fatalf("first registrant should keep short name with its own fields")
	}
	// 后来者用限定名，字段是助手账本版（id/tool/status）。
	second, ok := schemas["scoped.Evidence"]
	if !ok || second.Properties["id"] == nil || second.Properties["tool"] == nil {
		t.Fatalf("second registrant should get qualified schema scoped.Evidence")
	}
}
