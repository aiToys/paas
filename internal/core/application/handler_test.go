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

func (s *stubRepo) List(context.Context) ([]Application, error) { return s.apps, nil }
func (s *stubRepo) Get(_ context.Context, id string) (Application, error) {
	for _, a := range s.apps {
		if a.ID == id {
			return a, nil
		}
	}
	return Application{}, assertNotFoundErr
}
func (s *stubRepo) Create(_ context.Context, a Application) error {
	s.apps = append(s.apps, a)
	return nil
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
