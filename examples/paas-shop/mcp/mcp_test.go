package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fake product 服务：详情 + 搜索两端点。
func fakeProduct(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/products/1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":1,"name":"机械键盘 X1","price":299,"category":"外设","stock":5}`))
	})
	mux.HandleFunc("/products", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "" {
			_, _ = w.Write([]byte(`[{"id":1},{"id":2}]`))
			return
		}
		_, _ = w.Write([]byte(`[{"id":1,"name":"机械键盘 X1"}]`))
	})
	return httptest.NewServer(mux)
}

// fakeOrder 服务：查询 + 退款两端点（响应形态对齐 examples/paas-shop/order）。
func fakeOrder(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/orders/ORD-1001", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"ORD-1001","userId":"u1","status":"shipped","totalCents":29900,"items":[{"productId":1,"name":"机械键盘 X1","qty":1,"priceCents":29900}]}`))
	})
	mux.HandleFunc("/orders/ORD-1001/refund", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"ORD-1001","userId":"u1","status":"refunded","totalCents":29900,"items":[{"productId":1,"name":"机械键盘 X1","qty":1,"priceCents":29900}]}`))
	})
	// completed 订单退款被状态机拒绝（order 服务映射 409）
	mux.HandleFunc("/orders/ORD-2002/refund", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "当前状态不允许退款: completed", http.StatusConflict)
	})
	// 其余订单号（如 NOPE）走 mux 默认 404
	return httptest.NewServer(mux)
}

func TestCallToolQueryProduct(t *testing.T) {
	srv := fakeProduct(t)
	defer srv.Close()
	productURL = srv.URL
	out, isErr := callTool(context.Background(), "query_product", map[string]any{"productId": "1"})
	if isErr {
		t.Fatal("成功查询不应标记 isError")
	}
	if want := "商品详情"; !contains(out, want) {
		t.Fatalf("query_product 应含 %q，got %s", want, out)
	}
	if !contains(out, "机械键盘") {
		t.Fatalf("query_product 应含商品名，got %s", out)
	}
}

func TestCallToolSearchProducts(t *testing.T) {
	srv := fakeProduct(t)
	defer srv.Close()
	productURL = srv.URL
	out, isErr := callTool(context.Background(), "search_products", map[string]any{"q": "键"})
	if isErr {
		t.Fatal("成功搜索不应标记 isError")
	}
	if !contains(out, "机械键盘") {
		t.Fatalf("search 应返回匹配商品，got %s", out)
	}
	// 参数全空 = 全量
	out, isErr = callTool(context.Background(), "search_products", map[string]any{})
	if !contains(out, `"id":2`) && !contains(out, "2") {
		t.Fatalf("空参数应返回全量，got %s", out)
	}
}

func TestCallToolQueryOrder(t *testing.T) {
	srv := fakeOrder(t)
	defer srv.Close()
	t.Setenv("ORDER_URL", srv.URL)
	out, isErr := callTool(context.Background(), "query_order", map[string]any{"orderId": "ORD-1001"})
	if isErr {
		t.Fatal("成功查询不应标记 isError")
	}
	if !contains(out, "shipped") {
		t.Fatalf("query_order 应含状态，got %s", out)
	}
	out, isErr = callTool(context.Background(), "query_order", map[string]any{"orderId": "NOPE"})
	if !isErr {
		t.Fatal("未知订单应标记 isError")
	}
	if !contains(out, "不存在") {
		t.Fatalf("未知订单应提示不存在，got %s", out)
	}
}

func TestCallToolRefundOrder(t *testing.T) {
	srv := fakeOrder(t)
	defer srv.Close()
	t.Setenv("ORDER_URL", srv.URL)
	out, isErr := callTool(context.Background(), "refund_order", map[string]any{"orderId": "ORD-1001", "reason": "质量问题"})
	if isErr {
		t.Fatal("成功退款不应标记 isError")
	}
	if !contains(out, "refunded") {
		t.Fatalf("refund_order 应返回退款后订单，got %s", out)
	}
	// 状态机拒绝（409）透出原因
	out, isErr = callTool(context.Background(), "refund_order", map[string]any{"orderId": "ORD-2002", "reason": "质量问题"})
	if !isErr {
		t.Fatal("状态机拒绝应标记 isError（否则 workflow run 伪 succeeded，用户以为退款成功）")
	}
	if !contains(out, "退款被拒绝") {
		t.Fatalf("非法状态应提示退款被拒绝，got %s", out)
	}
}

// TestRefundForwardsActionToken 变更动作应携带 X-Action-Token（env 配置时）。
func TestRefundForwardsActionToken(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Action-Token")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	t.Setenv("ORDER_URL", srv.URL)
	t.Setenv("PAAS_ORDER_ACTION_TOKEN", "tok-123")
	_, _ = callTool(context.Background(), "refund_order", map[string]any{"orderId": "ORD-1001", "reason": "x"})
	if got != "tok-123" {
		t.Fatalf("refund_order 应携带 X-Action-Token，got %q", got)
	}
}

func TestToolNamesListed(t *testing.T) {
	// 4 工具名与平台 Tool 实体 name 逐字一致（runtime 按 Name 匹配）
	for _, n := range []string{"query_product", "search_products", "query_order", "refund_order"} {
		if _, ok := toolSchemas[n]; !ok {
			t.Fatalf("toolSchemas 缺工具 %s", n)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestCallToolQueryProductNumericID(t *testing.T) {
	// LLM 可能传数字 1 而非 "1"（schema 声明 string 但模型不严格遵守）
	srv := fakeProduct(t)
	defer srv.Close()
	productURL = srv.URL
	out, isErr := callTool(context.Background(), "query_product", map[string]any{"productId": float64(1)})
	if isErr {
		t.Fatal("数字形态 productId 查询成功，不应标记 isError")
	}
	if !contains(out, "机械键盘") {
		t.Fatalf("数字形态 productId 应正常查询，got %s", out)
	}
}
