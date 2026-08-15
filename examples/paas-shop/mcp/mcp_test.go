package main

import (
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

func TestCallToolQueryProduct(t *testing.T) {
	srv := fakeProduct(t)
	defer srv.Close()
	productURL = srv.URL
	out := callTool("query_product", map[string]any{"productId": "1"})
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
	out := callTool("search_products", map[string]any{"q": "键"})
	if !contains(out, "机械键盘") {
		t.Fatalf("search 应返回匹配商品，got %s", out)
	}
	// 参数全空 = 全量
	out = callTool("search_products", map[string]any{})
	if !contains(out, `"id":2`) && !contains(out, "2") {
		t.Fatalf("空参数应返回全量，got %s", out)
	}
}

func TestCallToolQueryOrder(t *testing.T) {
	out := callTool("query_order", map[string]any{"orderId": "ORD-1001"})
	if !contains(out, "shipped") {
		t.Fatalf("query_order 应含状态，got %s", out)
	}
	out = callTool("query_order", map[string]any{"orderId": "NOPE"})
	if !contains(out, "不存在") {
		t.Fatalf("未知订单应提示不存在，got %s", out)
	}
}

func TestCallToolRefundOrder(t *testing.T) {
	out := callTool("refund_order", map[string]any{"orderId": "ORD-1001", "reason": "质量问题"})
	if !contains(out, "退款") {
		t.Fatalf("refund_order 应受理，got %s", out)
	}
	// 受理后 query_order 应带 refundStatus
	out = callTool("query_order", map[string]any{"orderId": "ORD-1001"})
	if !contains(out, "refunding") {
		t.Fatalf("受理后查询应带退款状态，got %s", out)
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
	out := callTool("query_product", map[string]any{"productId": float64(1)})
	if !contains(out, "机械键盘") {
		t.Fatalf("数字形态 productId 应正常查询，got %s", out)
	}
}
