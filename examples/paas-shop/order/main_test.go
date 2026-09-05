// examples/paas-shop/order/main_test.go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTokenRequired 变更端点缺 X-Action-Token 拒 401、带 token 放行（直测中间件，不触 DB）。
func TestTokenRequired(t *testing.T) {
	actionToken = "test-token"
	defer func() { actionToken = "" }()
	called := false
	h := requireToken(func(http.ResponseWriter, *http.Request) { called = true })
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest(http.MethodPost, "/orders", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("缺 token 应 401，got %d", w.Code)
	}
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/orders", nil)
	req2.Header.Set("X-Action-Token", "test-token")
	h(w2, req2)
	if !called {
		t.Fatal("带 token 应放行到后端 handler")
	}
}

// TestOrderJSONContract Order 序列化契约（bff/frontend 消费的字段名锁定）。
func TestOrderJSONContract(t *testing.T) {
	b, err := json.Marshal(Order{ID: "ORD-1", UserID: "u1", Status: StatusPending, TotalCents: 9900,
		Items: []Item{{ProductID: 1, Name: "机械键盘", Qty: 1, PriceCents: 9900}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"id":"ORD-1"`, `"userId":"u1"`, `"status":"pending"`, `"totalCents":9900`, `"productId":1`, `"priceCents":9900`} {
		if !strings.Contains(string(b), key) {
			t.Errorf("Order JSON 应含 %s，got %s", key, b)
		}
	}
}

// TestNewOrderID ID 形态 ORD-<后10位数字>（文档/frontend 展示依赖前缀）。
func TestNewOrderID(t *testing.T) {
	id := newOrderID()
	if !strings.HasPrefix(id, "ORD-") || len(id) != 14 {
		t.Fatalf("ID 形态应 ORD-+10位，got %s", id)
	}
}
