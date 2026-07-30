package apiroute

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/core/application"
)

// sampleTypes 覆盖 reflector 的各分支。
type EmbedMe struct {
	Embedded string `json:"embedded"`
}

type sample struct {
	EmbedMe         // 匿名字段 inline（嵌入类型须导出，匹配领域模型实际用法）
	Name    string  `json:"name"`
	Opt     string  `json:"opt,omitempty"`
	Hidden  string  `json:"-"`
	Count   int     `json:"count"`
	Ratio   float64 `json:"ratio"`
	Flag    bool    `json:"flag"`
	Tags    []string
	Meta    map[string]int
	When    time.Time `json:"when"`
	Ptr     *string   `json:"ptr,omitempty"`
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestSchemaOfPrimitives(t *testing.T) {
	r := New(nil, Info{})
	cases := []struct {
		in   any
		want string
	}{
		{"", "string"},
		{0, "integer"},
		{0.0, "number"},
		{false, "boolean"},
	}
	for _, c := range cases {
		s := r.schemaOf(reflect.TypeOf(c.in))
		if s.Type != c.want {
			t.Fatalf("%v: want %s got %s", c.in, c.want, s.Type)
		}
	}
	s := r.schemaOf(reflect.TypeOf(time.Time{}))
	if s.Type != "string" || s.Format != "date-time" {
		t.Fatalf("time.Time want string/date-time got %s/%s", s.Type, s.Format)
	}
}

func TestSchemaOfSliceAndMap(t *testing.T) {
	r := New(nil, Info{})
	s := r.schemaOf(reflect.TypeOf([]string{}))
	if s.Type != "array" || s.Items.Type != "string" {
		t.Fatalf("slice want array/string got %+v", s)
	}
	m := r.schemaOf(reflect.TypeOf(map[string]int{}))
	if m.Type != "object" || m.AdditionalProperties.Type != "integer" {
		t.Fatalf("map want object/additionalProperties[integer] got %+v", m)
	}
}

func TestSchemaOfStruct(t *testing.T) {
	r := New(nil, Info{})
	s := r.schemaOf(reflect.TypeOf(sample{}))
	if s.Ref == "" {
		t.Fatal("命名 struct 应返回 $ref")
	}
	got := r.schemas["sample"]
	if got == nil {
		t.Fatal("sample 应登记进 schemas")
	}
	for _, name := range []string{"name", "count", "ratio", "flag", "Tags", "Meta", "when", "embedded"} {
		if _, ok := got.Properties[name]; !ok {
			t.Errorf("缺少属性 %s", name)
		}
	}
	if _, ok := got.Properties["Hidden"]; ok {
		t.Error(`json:"-" 字段应被跳过`)
	}
	if !contains(got.Required, "name") {
		t.Error("name 应 required")
	}
	if contains(got.Required, "opt") {
		t.Error("opt 有 omitempty 应非 required")
	}
}

func TestNamedTypeDedup(t *testing.T) {
	r := New(nil, Info{})
	a := r.schemaOf(reflect.TypeOf(application.Application{}))
	before := len(r.schemas)
	b := r.schemaOf(reflect.TypeOf(application.Application{}))
	if a.Ref != b.Ref || a.Ref == "" {
		t.Fatalf("同名类型应返回同一 $ref: %s vs %s", a.Ref, b.Ref)
	}
	if r.schemas["Application"] == nil {
		t.Fatal("Application 应登记进 schemas")
	}
	// 第二次调用不应新增 schema（去重）。
	if len(r.schemas) != before {
		t.Fatalf("重复调用不应新增 schema：before %d after %d", before, len(r.schemas))
	}
}

func TestRegisterDrivesMuxAndSpec(t *testing.T) {
	mux := http.NewServeMux()
	called := false
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
	reg := New(mux, Info{Title: "T", Version: "1.0"})
	reg.Register("GET", "/api/widgets", h,
		Summary("列出"),
		Perm("widget:read"),
		WithResp([]application.Application{}),
	)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/widgets", nil))
	if !called {
		t.Fatal("Register 应注册 mux，handler 未被调用")
	}

	doc := reg.Document()
	item := doc.Paths["/api/widgets"]
	if item == nil || item.Get == nil {
		t.Fatal("spec 应含 GET /api/widgets")
	}
	if item.Get.Summary != "列出" {
		t.Errorf("summary 不符: %s", item.Get.Summary)
	}
	if len(item.Get.Security) != 1 || len(item.Get.Security[0][BearerScheme]) != 1 {
		t.Fatalf("security scope 不符: %+v", item.Get.Security)
	}
	if item.Get.Security[0][BearerScheme][0] != "widget:read" {
		t.Errorf("scope 应为 widget:read")
	}
	resp := item.Get.Responses[200]
	if resp == nil || resp.Content["application/json"].Schema.Properties["data"] == nil {
		t.Fatalf("200 响应应含 data 包裹: %+v", resp)
	}
	if doc.Components.SecuritySchemes[BearerScheme] == nil {
		t.Fatal("应登记 Bearer 安全方案")
	}
}

func TestOperationDoesNotRegisterMux(t *testing.T) {
	mux := http.NewServeMux()
	reg := New(mux, Info{})
	reg.Operation("GET", "/api/apps/{id}", Perm("application:read"), WithResp(application.Application{}))
	if reg.Document().Paths["/api/apps/{id}"] == nil {
		t.Fatal("Operation 应登记进 spec")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/apps/x", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("Operation 不应注册 mux，应 404，got %d", rec.Code)
	}
}

func TestServeSpecValidJSON(t *testing.T) {
	mux := http.NewServeMux()
	reg := New(mux, Info{Title: "PaaS", Version: "1.0"})
	reg.Operation("GET", "/api/apps", Perm("application:read"), WithResp([]application.Application{}))
	mux.Handle("/openapi.json", ServeSpec(reg))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/openapi.json", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", rec.Code)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("spec 非合法 JSON: %v", err)
	}
	if doc["openapi"] != "3.0.3" {
		t.Errorf("openapi 版本不符: %v", doc["openapi"])
	}
}
