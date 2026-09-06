package messaging

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aitoys/paas/pkg/tenant"
)

// stubRepo 内存桩：只实现写路径用到的最小方法集。
type stubRepo struct{ topics map[string]Topic }

func (s *stubRepo) ListTopics(_ context.Context, _, _ string) ([]Topic, error) { return nil, nil }
func (s *stubRepo) CreateTopic(_ context.Context, t Topic) error {
	if s.topics == nil {
		s.topics = map[string]Topic{}
	}
	s.topics[t.ID] = t
	return nil
}
func (s *stubRepo) DeleteTopic(_ context.Context, _, id string) error {
	delete(s.topics, id)
	return nil
}
func (s *stubRepo) ListConsumerGroups(_ context.Context, _, _ string) ([]ConsumerGroup, error) {
	return nil, nil
}
func (s *stubRepo) CreateConsumerGroup(_ context.Context, _ ConsumerGroup) error { return nil }
func (s *stubRepo) DeleteConsumerGroup(_ context.Context, _, _ string) error     { return nil }

// auditStub 捕获审计调用。
type auditStub struct{ calls []string }

func (a *auditStub) Record(_ context.Context, _, actor, action, rtype, rid, _ string) error {
	a.calls = append(a.calls, action+"/"+rtype+"/"+rid+" by="+actor)
	return nil
}

// TestTopicWriteAudited 验证 Topic 创建/删除记审计（元审计 2026-09-04 回归防护）。
func TestTopicWriteAudited(t *testing.T) {
	repo := &stubRepo{}
	au := &auditStub{}
	h := NewHandler(repo)
	h.WithAudit(au, func(*http.Request) string { return "u1" })

	r := httptest.NewRequest(http.MethodPost, "/api/mq-topics", strings.NewReader(`{"mqId":"mq-1","name":"orders"}`))
	r = r.WithContext(tenant.WithTenant(r.Context(), "t-acme"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("创建应 200，got %d body=%s", w.Code, w.Body.String())
	}

	r2 := httptest.NewRequest(http.MethodDelete, "/api/mq-topics/tp-orders", nil)
	r2 = r2.WithContext(tenant.WithTenant(r2.Context(), "t-acme"))
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Code != http.StatusNoContent {
		t.Fatalf("删除应 204，got %d", w2.Code)
	}

	if len(au.calls) != 2 || !strings.HasPrefix(au.calls[0], "mq_topic_create/messaging/tp-orders") ||
		!strings.HasPrefix(au.calls[1], "mq_topic_delete/messaging/tp-orders") {
		t.Fatalf("应记 create+delete 两条审计，got %v", au.calls)
	}
}
