package dataplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aitoys/paas/internal/governance"
)

// fakeReader mock EndpointsReader。
type fakeReader struct {
	insts   []Instance
	svcs    []ServiceInfo
	instErr error
}

func (f fakeReader) Instances(context.Context, string, string) ([]Instance, error) {
	return f.insts, f.instErr
}
func (f fakeReader) Services(context.Context, string) ([]ServiceInfo, error) {
	return f.svcs, nil
}

// fakeSvcStore mock governance.ServiceStore。
type fakeSvcStore struct {
	list []governance.Service
}

func (s *fakeSvcStore) ListServices(ctx context.Context, envID, appID string) ([]governance.Service, error) {
	return s.list, nil
}
func (s *fakeSvcStore) GetService(context.Context, string) (governance.Service, error) {
	return governance.Service{}, nil
}
func (s *fakeSvcStore) CreateService(_ context.Context, sv governance.Service) (governance.Service, error) {
	s.list = append(s.list, sv)
	return sv, nil
}
func (s *fakeSvcStore) DeleteService(_ context.Context, id string) error { return nil }

func (s *fakeSvcStore) ListAllServices(ctx context.Context) ([]governance.Service, error) {
	return s.list, nil
}

func TestListInstances(t *testing.T) {
	h := NewHandler(fakeReader{insts: []Instance{{ID: "a", IP: "10.0.0.1", Port: 8080}}}, &fakeSvcStore{})
	req := httptest.NewRequest(http.MethodGet, "/dp/instances?service=user-svc", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("期望 200，实际 %d", rec.Code)
	}
	var body struct {
		Service   string     `json:"service"`
		Instances []Instance `json:"instances"`
		Signature string     `json:"signature"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.Service != "user-svc" || len(body.Instances) != 1 || body.Instances[0].IP != "10.0.0.1" {
		t.Fatalf("实例解析错误: %+v", body)
	}
	if body.Signature == "" {
		t.Fatalf("signature 不应为空")
	}
}

func TestListInstancesDegradedNoCluster(t *testing.T) {
	// reader=nil + ns=""（非集群）：instances 返空切片，不报错。
	h := NewHandler(nil, &fakeSvcStore{})
	req := httptest.NewRequest(http.MethodGet, "/dp/instances?service=user-svc", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("非集群应降级 200，实际 %d", rec.Code)
	}
	var body struct {
		Instances []Instance `json:"instances"`
	}
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if len(body.Instances) != 0 {
		t.Fatalf("非集群应返空实例，实际 %d", len(body.Instances))
	}
}

func TestListInstancesMissingService(t *testing.T) {
	h := NewHandler(nil, &fakeSvcStore{})
	req := httptest.NewRequest(http.MethodGet, "/dp/instances", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("缺 service 参数应 400，实际 %d", rec.Code)
	}
}

func TestRegisterCreatesService(t *testing.T) {
	store := &fakeSvcStore{}
	h := NewHandler(nil, store)
	req := httptest.NewRequest(http.MethodPost, "/dp/register",
		strings.NewReader(`{"name":"user-svc","protocol":"http"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("期望 201，实际 %d", rec.Code)
	}
	if len(store.list) != 1 || store.list[0].Name != "user-svc" {
		t.Fatalf("应创建服务声明，实际 %+v", store.list)
	}
}

// TestEndpointsToInstancesReadyOnly 验证只返 ready address（not-ready 排除）。
//
//nolint:staticcheck // corev1.Endpoints 在 K8s v0.36 仍主流；EndpointSlice 迁移留后续
func TestEndpointsToInstancesReadyOnly(t *testing.T) {
	ep := &corev1.Endpoints{ObjectMeta: metav1.ObjectMeta{Name: "user-svc"}}
	ep.Subsets = []corev1.EndpointSubset{{
		Addresses:         []corev1.EndpointAddress{{IP: "10.0.0.1"}}, // ready
		NotReadyAddresses: []corev1.EndpointAddress{{IP: "10.0.0.2"}}, // not-ready，应排除
		Ports:             []corev1.EndpointPort{{Port: 8080, Name: "http"}},
	}}
	insts := endpointsToInstances(ep)
	if len(insts) != 1 {
		t.Fatalf("应只返 1 个 ready 实例（排除 not-ready），实际 %d", len(insts))
	}
	if insts[0].IP != "10.0.0.1" || insts[0].Port != 8080 || insts[0].Cluster != "default" {
		t.Fatalf("实例字段错误: %+v", insts[0])
	}
}

// TestSignatureStable 验证签名确定性（同实例集顺序无关，相同输入同输出）。
func TestSignatureStable(t *testing.T) {
	a := []Instance{{ID: "1", IP: "a", Port: 1}, {ID: "2", IP: "b", Port: 2}}
	b := []Instance{{ID: "2", IP: "b", Port: 2}, {ID: "1", IP: "a", Port: 1}}
	if signature(a) != signature(b) {
		t.Fatalf("签名应顺序无关")
	}
	// 相同实例集多次计算应稳定（顺序无关已隐含确定性）
	if signature(a) != signature(b) {
		t.Fatalf("签名应确定性")
	}
}
