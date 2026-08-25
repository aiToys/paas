package paasregistry

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-zeus/zeus/types"
)

func TestResolveFromURL(t *testing.T) {
	cases := []struct {
		url       string
		wantBase  string
		wantToken string
	}{
		{"paas://paas-core.paas.svc/dp?token=sk-1", "http://paas-core.paas.svc/dp", "sk-1"},
		{"paas://core:8080/dp", "http://core:8080/dp", ""},
	}
	for _, c := range cases {
		reg, err := resolveFromURL(c.url)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", c.url, err)
		}
		p := reg.(*paasRegistry)
		if p.base != c.wantBase || p.token != c.wantToken {
			t.Fatalf("解析 %q: base=%q token=%q，期望 base=%q token=%q", c.url, p.base, p.token, c.wantBase, c.wantToken)
		}
	}
}

func TestResolveFromURLInvalid(t *testing.T) {
	if _, err := resolveFromURL("paas:///dp"); err == nil { // 缺 host
		t.Fatalf("缺 host 应报错")
	}
}

func TestGetService(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dp/instances" || r.URL.Query().Get("service") != "user-svc" {
			t.Errorf("请求不符: %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer sk-1" {
			t.Errorf("缺 token 鉴权头")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service":   "user-svc",
			"instances": []map[string]any{{"id": "a", "ip": "10.0.0.1", "port": 8080, "cluster": "default"}},
			"signature": "abc",
		})
	}))
	defer srv.Close()
	reg := &paasRegistry{base: srv.URL + "/dp", token: "sk-1", client: srv.Client()}
	se, err := reg.GetService(context.Background(), "user-svc")
	if err != nil {
		t.Fatalf("GetService 失败: %v", err)
	}
	if se.Name != "user-svc" || len(se.Instances) != 1 {
		t.Fatalf("解析错误: name=%q instances=%d", se.Name, len(se.Instances))
	}
	// 实例应同时进 Instances 与 Clusters（AddInstance 双索引）
	if len(se.Clusters) == 0 {
		t.Fatalf("实例应入 Clusters 索引")
	}
}

func TestGetServiceWithLane(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service":   "user-svc",
			"instances": []map[string]any{},
			"signature": "abc",
		})
	}))
	defer srv.Close()
	reg := &paasRegistry{base: srv.URL + "/dp", token: "sk-1", client: srv.Client()}

	// ctx 注入 lane=feature-x → URL 应含 &lane=feature-x
	ctx := WithLane(context.Background(), "feature-x")
	if _, err := reg.GetService(ctx, "user-svc"); err != nil {
		t.Fatalf("GetService 失败: %v", err)
	}
	if gotPath != "/dp/instances" {
		t.Errorf("path 错误: %q", gotPath)
	}
	if gotQuery != "service=user-svc&lane=feature-x" {
		t.Errorf("期望 query 含 lane，得 %q", gotQuery)
	}

	// ctx 无 lane → URL 不含 lane query（向后兼容）
	gotQuery = ""
	if _, err := reg.GetService(context.Background(), "user-svc"); err != nil {
		t.Fatalf("GetService 失败: %v", err)
	}
	if gotQuery != "service=user-svc" {
		t.Errorf("无 lane 时 query 应不含 lane，得 %q", gotQuery)
	}
}

func TestRegister(t *testing.T) {
	var gotBody dpInstance
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dp/register" || r.Method != http.MethodPost {
			t.Errorf("请求不符: %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	reg := &paasRegistry{base: srv.URL + "/dp", token: "sk-1", client: srv.Client()}
	if err := reg.Register(context.Background(), &types.Instance{
		ID: "x", Name: "user-svc", Cluster: "default", Protocol: "http", IP: "10.0.0.1", Port: 8080,
	}); err != nil {
		t.Fatalf("Register 失败: %v", err)
	}
	if gotBody.Name != "user-svc" || gotBody.Port != 8080 {
		t.Fatalf("注册 body 错误: %+v", gotBody)
	}
}

func TestEntrySignatureChange(t *testing.T) {
	se1 := types.NewServiceEntry()
	_ = se1.AddInstance(&types.Instance{ID: "a", IP: "1.1.1.1", Port: 80, Cluster: "default"})
	sig1 := entrySignature(se1)

	se2 := types.NewServiceEntry()
	_ = se2.AddInstance(&types.Instance{ID: "a", IP: "1.1.1.1", Port: 80, Cluster: "default"})
	_ = se2.AddInstance(&types.Instance{ID: "b", IP: "2.2.2.2", Port: 80, Cluster: "default"})
	sig2 := entrySignature(se2)

	if sig1 == sig2 {
		t.Fatalf("实例集变化应导致签名变化")
	}
	if entrySignature(se1) != sig1 {
		t.Fatalf("同实例集签名应稳定")
	}
}

func TestNonEmpty(t *testing.T) {
	if nonEmpty("", "def") != "def" {
		t.Fatal()
	}
	if nonEmpty("x", "def") != "x" {
		t.Fatal()
	}
}

// TestGetServiceEnvLaneFallback：无 ctx lane 时 GetService 应回落 SelfLane env，
// URL 带 &lane=<env 泳道>（L3 零改动染色核心链路）。
func TestGetServiceEnvLaneFallback(t *testing.T) {
	var gotLane string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotLane = r.URL.Query().Get("lane")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service":   "user-svc",
			"instances": []map[string]any{{"id": "a", "ip": "10.0.0.1", "port": 8080, "cluster": "default"}},
			"signature": "abc",
		})
	}))
	defer srv.Close()
	t.Setenv("PAAS_LANE_ID", "feature-env")
	selfLane.Store("")
	t.Cleanup(func() { selfLane.Store("") })
	reg := &paasRegistry{base: srv.URL + "/dp", token: "sk-1", client: srv.Client()}
	if _, err := reg.GetService(context.Background(), "user-svc"); err != nil {
		t.Fatalf("GetService 失败: %v", err)
	}
	if gotLane != "feature-env" {
		t.Fatalf("URL lane = %q, want feature-env（env 回落）", gotLane)
	}
}
