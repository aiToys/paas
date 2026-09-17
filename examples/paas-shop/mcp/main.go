// Command mcp 是 paas-shop 示例的 MCP（Model Context Protocol）server。
//
// 提供四个工具供 Agent FunctionCalling 调用（均 HTTP 直调 paas-shop 后端微服务，自身无业务状态）：
//   - query_product：按商品 ID 查详情（调 product 服务 GET /products/{id}）
//   - search_products：按关键字/分类搜索商品（调 product 服务 GET /products?q=&category=&limit=）
//   - query_order：查询订单状态（调 order 服务 GET /orders/{id}）
//   - refund_order：对订单发起退款（调 order 服务 POST /orders/{id}/refund）
//
// 协议：JSON-RPC 2.0 over HTTP（POST /mcp），实现 initialize / tools/list / tools/call。
// 与 internal/ai/tool/mcp/client.go 的客户端协议匹配（Streamable HTTP 简化形态）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"

	"github.com/aitoys/paas-examples/paas-shop/internal/observ"
)

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResp struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      uint64    `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// httpClient 带超时（observ 注入 tracing transport），不用裸 http.Get。
var httpClient = observ.NewClient()

// toolSchemas 是 4 工具的 schema 单一真源（tools/list 与测试断言共用）。
// name 与平台 Tool 实体 name 逐字一致（runtime 按 Name 匹配）。
var toolSchemas = map[string]map[string]any{
	"query_product": {
		"name":        "query_product",
		"description": "按商品 ID 查详情（已知编号时用），返回名称/价格/分类/库存",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"productId": map[string]any{"type": "string", "description": "商品 ID，如 1"},
			},
			"required": []string{"productId"},
		},
	},
	"search_products": {
		"name":        "search_products",
		"description": "按关键字/分类搜索商品，用于找商品/推荐/比价，参数可全空返回全量",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"q":        map[string]any{"type": "string", "description": "关键字"},
				"category": map[string]any{"type": "string", "description": "分类"},
				"limit":    map[string]any{"type": "string", "description": "返回条数上限"},
			},
		},
	},
	"query_order": {
		"name":        "query_order",
		"description": "查询订单状态（按订单号返回订单详情：状态/金额/商品）",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"orderId": map[string]any{"type": "string", "description": "订单号，如 ORD-0123456789"},
			},
			"required": []string{"orderId"},
		},
	},
	"refund_order": {
		"name":        "refund_order",
		"description": "对指定订单发起退款（真实调用订单服务；订单状态机不允许时被拒绝）",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"orderId": map[string]any{"type": "string", "description": "订单号"},
				"reason":  map[string]any{"type": "string", "description": "退款原因（供审批工作流 LLM 分析留痕，不传给订单服务）"},
			},
			"required": []string{"orderId", "reason"},
		},
	},
}

// productURL 是 product 服务地址（query_product/search_products 调用目标）。
var productURL = "http://paas-shop-product:8081"

func main() {
	if v := os.Getenv("PRODUCT_SERVICE_URL"); v != "" {
		productURL = v
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", handleMCP)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/metrics", observ.MetricsHandler())
	shutdown := observ.Init("paas-shop-mcp")
	defer shutdown()
	log.Println("MCP server listening :8080")
	log.Fatal(http.ListenAndServe(":8080", observ.Recover(observ.Handler("mcp", mux))))
}

func handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req rpcReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, rpcResp{JSONRPC: "2.0", ID: 0, Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch req.Method {
	case "initialize":
		writeJSON(w, rpcResp{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{
				"protocolVersion": "2025-06-18",
				"serverInfo":      map[string]string{"name": "paas-shop-mcp", "version": "1.0"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			},
		})
	case "tools/list":
		tools := make([]map[string]any, 0, len(toolSchemas))
		for _, s := range toolSchemas {
			tools = append(tools, s)
		}
		writeJSON(w, rpcResp{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{"tools": tools},
		})
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		text, isErr := callTool(r.Context(), p.Name, p.Arguments)
		// MCP isError 语义：失败分支（订单状态机拒绝/下游不可达/参数缺失）标记后，
		// 平台 client（internal/ai/tool/mcp/client.go Invoke）转 error——
		// workflow tool 节点标 failed（run 不再伪 succeeded）、agent 把原因回给 LLM。
		result := map[string]any{
			"content": []map[string]any{{"type": "text", "text": text}},
		}
		if isErr {
			result["isError"] = true
		}
		writeJSON(w, rpcResp{
			JSONRPC: "2.0", ID: req.ID,
			Result: result,
		})
	default:
		writeJSON(w, rpcResp{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}})
	}
}

// callTool 分发工具调用，返回 (结果文本, 是否失败)；失败时 handler 组装 isError=true。
func callTool(ctx context.Context, name string, args map[string]any) (string, bool) {
	switch name {
	case "query_product":
		return queryProduct(args)
	case "search_products":
		return searchProducts(args)
	case "query_order":
		return queryOrder(ctx, args)
	case "refund_order":
		return refundOrder(ctx, args)
	default:
		return "未知工具: " + name, true
	}
}

// orderURL order 服务地址（默认集群内基线 Service；泳道无关——mcp 是平台 AI 层单实例，
// 退款/query 演示走 test 订单，spec §6 环境边界）。
func orderURL() string {
	if v := os.Getenv("ORDER_URL"); v != "" {
		return v
	}
	return "http://paas-shop-order:8084"
}

// callOrder 调 order 服务（GET 查询 / POST 动作带 X-Action-Token）。
func callOrder(ctx context.Context, method, path string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, orderURL()+path, nil)
	if err != nil {
		return 0, nil, err
	}
	if method != http.MethodGet {
		if tok := os.Getenv("PAAS_ORDER_ACTION_TOKEN"); tok != "" {
			req.Header.Set("X-Action-Token", tok)
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, body, err
}

// queryOrder 调 order 服务查真实订单（GET /orders/{id}；404 归一为工具失败非协议错误）。
func queryOrder(ctx context.Context, args map[string]any) (string, bool) {
	code, body, err := callOrder(ctx, http.MethodGet, "/orders/"+argString(args, "orderId"))
	if err != nil || code == http.StatusNotFound {
		return "订单不存在或查询失败: " + argString(args, "orderId"), true
	}
	return "订单详情: " + string(body), false
}

// refundOrder 调 order 服务退款（POST /orders/{id}/refund；状态机拒绝 409 时标失败透出原因——
// 否则 workflow run 伪 succeeded，用户看到「成功」但订单未退）。
func refundOrder(ctx context.Context, args map[string]any) (string, bool) {
	code, body, err := callOrder(ctx, http.MethodPost, "/orders/"+argString(args, "orderId")+"/refund")
	if err != nil {
		return "退款请求失败: " + err.Error(), true
	}
	if code != http.StatusOK {
		return "退款被拒绝: " + string(body), true
	}
	return "退款结果: " + string(body), false
}

// queryProduct 调 product 服务查商品详情（GET /products/{id}）。
func queryProduct(args map[string]any) (string, bool) {
	pid := argString(args, "productId")
	if pid == "" {
		return "参数 productId 缺失", true
	}
	resp, err := httpClient.Get(productURL + "/products/" + pid)
	if err != nil {
		return fmt.Sprintf("查询商品失败: %v", err), true
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("商品服务返回 %d: %s", resp.StatusCode, string(body)), true
	}
	return "商品详情: " + string(body), false
}

// searchProducts 调 product 服务搜索商品（GET /products?q=&category=&limit=）。
func searchProducts(args map[string]any) (string, bool) {
	vals := url.Values{}
	if v := argString(args, "q"); v != "" {
		vals.Set("q", v)
	}
	if v := argString(args, "category"); v != "" {
		vals.Set("category", v)
	}
	// limit 经 JSON 反序列化可能是 float64/json.Number/string，统一 %v。
	if v, ok := args["limit"]; ok && fmt.Sprintf("%v", v) != "" && fmt.Sprintf("%v", v) != "<nil>" {
		vals.Set("limit", fmt.Sprintf("%v", v))
	}
	resp, err := httpClient.Get(productURL + "/products?" + vals.Encode())
	if err != nil {
		return fmt.Sprintf("搜索商品失败: %v", err), true
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("商品服务返回 %d: %s", resp.StatusCode, string(body)), true
	}
	return "商品搜索结果: " + string(body), false
}

// argString 兼容 string/float64/json.Number 形态取参数（LLM 可能传数字 1 而非 "1"）。
func argString(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case string:
		return v
	case float64:
		if v == float64(int64(v)) {
			return fmt.Sprintf("%d", int64(v))
		}
		return fmt.Sprintf("%v", v)
	case int:
		return fmt.Sprintf("%d", v)
	case int64:
		return fmt.Sprintf("%d", v)
	default:
		return ""
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(v)
}
