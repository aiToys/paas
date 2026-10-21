package diagnostics

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSecurityChecksNeverLeakValues 锁死「只报状态不回显值」：
// 响应里出现密钥明文即视为严重泄漏（页面/浏览器缓存/日志都会二次扩散）。
func TestSecurityChecksNeverLeakValues(t *testing.T) {
	const (
		jwt    = "super-secret-jwt-value-should-never-appear"
		master = "master-key-value-should-never-appear"
	)
	env := map[string]string{
		"PAAS_PROD":              "true",
		"PAAS_JWT_SECRET":        jwt,
		"PAAS_SECRET_MASTER_KEY": master,
		"PAAS_COOKIE_SECURE":     "true",
	}
	getenv := func(k string) string { return env[k] }

	checks := SecurityChecks(getenv)
	raw, err := json.Marshal(checks)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, secret := range []string{jwt, master} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("安全配置状态泄漏了配置值: %s", raw)
		}
	}
	if len(checks) != 4 {
		t.Fatalf("应返回 4 项检查，got %d", len(checks))
	}
	for _, c := range checks {
		if c.Severity != "info" {
			t.Fatalf("生产模式且全部已配置时不应有 warn: %+v", c)
		}
	}
}

// TestSecurityChecksProdWarn 生产模式缺配置必须 warn（否则页面看起来一切正常）。
func TestSecurityChecksProdWarn(t *testing.T) {
	checks := SecurityChecks(func(k string) string {
		if k == "PAAS_PROD" {
			return "true"
		}
		return ""
	})
	warned := map[string]bool{}
	for _, c := range checks {
		if c.Severity == "warn" {
			warned[c.Key] = true
		}
	}
	for _, k := range []string{"jwt_secret", "cookie_secure", "secret_master_key"} {
		if !warned[k] {
			t.Fatalf("生产模式缺 %s 应告警: %+v", k, checks)
		}
	}
}

// TestSecurityChecksShortJWTStaysSet 短密钥仍报「已配置」但在说明里点明长度不足，
// 不把「配了但不合规」伪装成「未配置」（两者处置动作不同）。
func TestSecurityChecksShortJWTStaysSet(t *testing.T) {
	for _, c := range SecurityChecks(func(k string) string {
		if k == "PAAS_JWT_SECRET" {
			return "short"
		}
		return ""
	}) {
		if c.Key != "jwt_secret" {
			continue
		}
		if c.Status != "set" {
			t.Fatalf("短密钥应为 set，got %s", c.Status)
		}
		if !strings.Contains(c.Note, "32") {
			t.Fatalf("说明应点明长度要求: %s", c.Note)
		}
		return
	}
	t.Fatal("缺少 jwt_secret 检查")
}

// TestHandlerRejectsNonGET 只读端点不接受写方法。
func TestHandlerRejectsNonGET(t *testing.T) {
	h := Handler(func(ctx context.Context) Snapshot { return Snapshot{} })
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/admin/diagnostics", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST 应 405，got %d", rec.Code)
	}
}

// TestHandlerWrapsData GET 返回 {data: snapshot} 平台契约。
func TestHandlerWrapsData(t *testing.T) {
	h := Handler(func(ctx context.Context) Snapshot {
		return Snapshot{Deploy: DeployInfo{Mode: "local"}}
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/admin/diagnostics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET 应 200，got %d", rec.Code)
	}
	var body struct {
		Data Snapshot `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if body.Data.Deploy.Mode != "local" {
		t.Fatalf("data 未透传: %s", rec.Body.String())
	}
}
