// Command mcp-server 是平台 AI 工具示例的 MCP（Model Context Protocol）server。
//
// 提供三个工具供 Agent FunctionCalling 调用：
//   - query_product：查询商品详情（调 product 服务 GET /products/{id}）
//   - query_order：查询订单状态（订单号 -> 详情）
//   - refund_order：对订单发起退款（演示：标记为处理中）
//
// 协议：JSON-RPC 2.0 over HTTP（POST /mcp），实现 initialize / tools/list / tools/call。
// 与 internal/ai/tool/mcp/client.go 的客户端协议匹配（Streamable HTTP 简化形态）。
//
// 部署为 K8s Deployment + Service，平台「工具管理」创建 type=mcp 工具指向本服务，
// Agent 引用该工具后，runtime 经 ListTools 取真实 schema 并在多轮循环中 tools/call。
//
// 数据为内存演示（重启丢失）；生产应接真实订单/退款系统。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
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

// 订单内存存储（演示用；生产接真实订单系统）。
var (
	mu     sync.Mutex
	orders = map[string]map[string]any{
		"ORD-1001": {"orderId": "ORD-1001", "status": "shipped", "amount": 299.0, "items": []string{"无线鼠标", "机械键盘"}},
		"ORD-1002": {"orderId": "ORD-1002", "status": "pending", "amount": 1599.0, "items": []string{"27寸显示器"}},
		"ORD-1003": {"orderId": "ORD-1003", "status": "delivered", "amount": 89.0, "items": []string{"蓝牙耳机"}},
		"ORD-1004": {"orderId": "ORD-1004", "status": "shipped", "amount": 4599.0, "items": []string{"笔记本电脑"}},
	}
	refunds = map[string]string{} // orderId -> 退款状态

	// productURL 是 product 服务地址（query_product 工具调用目标）。
	productURL = "http://paas-shop-product:8081"
)

func main() {
	if v := os.Getenv("PRODUCT_SERVICE_URL"); v != "" {
		productURL = v
	}
	http.HandleFunc("/mcp", handleMCP)
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	log.Println("MCP server listening :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
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
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]string{"name": "paas-example-mcp", "version": "1.0"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			},
		})
	case "tools/list":
		writeJSON(w, rpcResp{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{
				"tools": []map[string]any{
					{
						"name":        "query_order",
						"description": "查询订单状态（按订单号返回订单详情：状态/金额/商品）",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"orderId": map[string]any{"type": "string", "description": "订单号，如 ORD-1001"},
							},
							"required": []string{"orderId"},
						},
					},
					{
						"name":        "refund_order",
						"description": "对指定订单发起退款（演示：标记为退款处理中）",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"orderId": map[string]any{"type": "string", "description": "订单号"},
								"reason":  map[string]any{"type": "string", "description": "退款原因"},
							},
							"required": []string{"orderId", "reason"},
						},
					},
					{
						"name":        "query_product",
						"description": "查询商品详情（按商品 ID 返回名称/价格/库存）",
						"inputSchema": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"productId": map[string]any{"type": "string", "description": "商品 ID，如 1"},
							},
							"required": []string{"productId"},
						},
					},
				},
			},
		})
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(req.Params, &p)
		text := callTool(p.Name, p.Arguments)
		writeJSON(w, rpcResp{
			JSONRPC: "2.0", ID: req.ID,
			Result: map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
			},
		})
	default:
		writeJSON(w, rpcResp{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}})
	}
}

func callTool(name string, args map[string]any) string {
	// query_product 调外部 product 服务，不持 orders/refunds 锁。
	if name == "query_product" {
		return queryProduct(args)
	}
	mu.Lock()
	defer mu.Unlock()
	switch name {
	case "query_order":
		oid, _ := args["orderId"].(string)
		o, ok := orders[oid]
		if !ok {
			return fmt.Sprintf("订单 %s 不存在", oid)
		}
		if status, ok := refunds[oid]; ok {
			o["refundStatus"] = status
		}
		b, _ := json.Marshal(o)
		return "订单详情: " + string(b)
	case "refund_order":
		oid, _ := args["orderId"].(string)
		reason, _ := args["reason"].(string)
		if _, ok := orders[oid]; !ok {
			return fmt.Sprintf("订单 %s 不存在，无法退款", oid)
		}
		refunds[oid] = "refunding"
		return fmt.Sprintf("订单 %s 已发起退款（原因：%s），状态：处理中", oid, reason)
	default:
		return "未知工具: " + name
	}
}

// queryProduct 调 product 服务查商品详情（GET /products/{id}）。
func queryProduct(args map[string]any) string {
	pid, _ := args["productId"].(string)
	if pid == "" {
		return "参数 productId 缺失"
	}
	resp, err := http.Get(productURL + "/products/" + pid)
	if err != nil {
		return fmt.Sprintf("查询商品失败: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("商品服务返回 %d: %s", resp.StatusCode, string(body))
	}
	return "商品详情: " + string(body)
}

func writeJSON(w http.ResponseWriter, v any) {
	_ = json.NewEncoder(w).Encode(v)
}
