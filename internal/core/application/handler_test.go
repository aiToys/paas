package application

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubRepo 用于 handler 测试。
type stubRepo struct {
	apps []Application
}

func (s *stubRepo) List(context.Context) ([]Application, error)    { return s.apps, nil }
func (s *stubRepo) ListAll(context.Context) ([]Application, error) { return s.apps, nil }
func (s *stubRepo) Get(_ context.Context, id string) (Application, error) {
	for _, a := range s.apps {
		if a.ID == id {
			return a, nil
		}
	}
	return Application{}, assertNotFoundErr
}
func (s *stubRepo) SetRestricted(_ context.Context, id string, restricted bool) error {
	for i := range s.apps {
		if s.apps[i].ID == id {
			s.apps[i].Restricted = restricted
			return nil
		}
	}
	return ErrMemberNotFound
}

func (s *stubRepo) Create(_ context.Context, a Application) error {
	s.apps = append(s.apps, a)
	return nil
}
func (s *stubRepo) Delete(_ context.Context, id string) error {
	for i, a := range s.apps {
		if a.ID == id {
			s.apps = append(s.apps[:i], s.apps[i+1:]...)
			return nil
		}
	}
	return assertNotFoundErr
}
func (s *stubRepo) BindResource(_ context.Context, id, t, name string) (Application, error) {
	for i, a := range s.apps {
		if a.ID == id {
			a.Bindings = append(a.Bindings, Binding{Type: t, Name: name})
			a.Recount()
			s.apps[i] = a
			return a, nil
		}
	}
	return Application{}, assertNotFoundErr
}

func (s *stubRepo) Unbind(_ context.Context, id, t, name string) (Application, error) {
	for i, a := range s.apps {
		if a.ID == id {
			next := a.Bindings[:0]
			for _, b := range a.Bindings {
				if b.Type == t && b.Name == name {
					continue
				}
				next = append(next, b)
			}
			a.Bindings = next
			a.Recount()
			s.apps[i] = a
			return a, nil
		}
	}
	return Application{}, assertNotFoundErr
}

type notFoundErr struct{}

func (notFoundErr) Error() string { return "not found" }

var assertNotFoundErr = notFoundErr{}

func TestListHandler(t *testing.T) {
	h := NewHandler(&stubRepo{apps: []Application{{ID: "a1", Name: "A"}}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/applications", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"id":"a1"`)
}

func TestBindHandler(t *testing.T) {
	h := NewHandler(&stubRepo{apps: []Application{{ID: "a1", Name: "A"}}})
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"type":"mq","name":"mq-new"}`)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/applications/a1/bindings", body))

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, rec.Body.String(), `"mq":1`)
	assert.Contains(t, rec.Body.String(), `"name":"mq-new"`)
}

func TestUnbindHandler(t *testing.T) {
	h := NewHandler(&stubRepo{apps: []Application{
		{ID: "a1", Name: "A", Bindings: []Binding{{Type: "mq", Name: "mq-x"}}}},
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/applications/a1/bindings/mq/mq-x", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"mq":0`)
}

// TestCreateHandlerQuotaExceeded 验证横切配额拦截：QuotaCheck 返回错误时
// Create 中止、回 429、不调用 repo.Create。
func TestCreateHandlerQuotaExceeded(t *testing.T) {
	repo := &stubRepo{}
	h := NewHandler(repo)
	h.QuotaCheck = func(_ context.Context, _ int) error {
		return assertNotFoundErr // 模拟 billing.ErrQuotaExceeded
	}
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"id":"a-new","name":"A"}`)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/applications", body))

	require.Equal(t, StatusQuotaExceeded, rec.Code)
	assert.Empty(t, repo.apps, "配额超限时不应调用 repo.Create")
}

// TestCreateHandlerQuotaRollback 验证 repo.Create 失败时回滚已递增的配额（delta=-1）。
func TestCreateHandlerQuotaRollback(t *testing.T) {
	// failRepo.Create 总是失败。
	repo := &failCreateRepo{}
	h := NewHandler(repo)
	var net int
	h.QuotaCheck = func(_ context.Context, delta int) error {
		net += delta
		return nil
	}
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"id":"a-new","name":"A"}`)
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/applications", body))

	require.Equal(t, http.StatusConflict, rec.Code) // Create 失败 -> 409
	assert.Equal(t, 0, net, "Create 失败应回滚配额（+1 -1 = 0）")
}

// failCreateRepo.Create 总返回错误，用于触发回滚路径。
type failCreateRepo struct{ stubRepo }

func (f *failCreateRepo) Create(_ context.Context, _ Application) error {
	return assertNotFoundErr
}

// TestDeleteHandler 验证删除应用闭环 + 跨租户不泄漏（404）。
func TestDeleteHandler(t *testing.T) {
	repo := &stubRepo{apps: []Application{{ID: "a1", Name: "A"}}}
	var cascaded string
	h := NewHandler(repo)
	h.CascadeDelete = func(_ context.Context, appID string) error {
		cascaded = appID
		return nil
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/applications/a1", nil))

	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "a1", cascaded, "应先触发级联清理")
	assert.Empty(t, repo.apps, "应用应已删除")

	// 跨租户/不存在统一 404 不泄漏。
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodDelete, "/api/applications/ghost", nil))
	require.Equal(t, http.StatusNotFound, rec2.Code)
}
