// paas-shop AI 客服服务：演示「MaaS 推理 + 平台知识库 RAG + Function Calling 工具」。
//
// 平台能力组合：
//   - MaaS：调平台 /v1/chat/completions（airouter 真实推理，流式 SSE）
//   - 知识库：调平台 /api/knowledgebases/{id}/retrieve（真向量检索 + score 排序，airouter embedding）
//   - 工具：function calling 定义 get_product，LLM 决策调用 -> 调 product 服务
//
// 调用链路：bff -> chatbot -> MaaS(平台 airouter) + 平台 KB(检索) + product(工具)。
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aitoys/paas-examples/paas-shop/internal/observ"
)

var (
	gatewayURL   string // 平台 /v1/chat/completions + /api/knowledgebases 入口（paas-core）
	apiKey        string // 平台 API Key
	kbID         string // 平台知识库 ID（PAAS_KB_ID），空则 RAG 降级
	productURL   string // product 服务（工具调用目标）
	httpClient   = observ.NewClient()
	streamClient = observ.NewStreamingClient() // 调平台 gateway 用（airouter 长 reasoning 超 10s）
	model        string
)

// FAQ 是本地兜底种子（仅当平台 KB 未配时作 memory 上下文，不再直连 qdrant）。
type FAQ struct {
	Question string   `json:"question"`
	Answer   string   `json:"answer"`
	Keywords []string `json:"keywords"`
}

// fallbackFAQ 是平台 KB 不可用时的降级上下文（演示连续性，非真 RAG）。
var fallbackFAQ = []FAQ{
	{Question: "退货政策是什么", Answer: "支持 7 天无理由退货，商品需保持完好。", Keywords: []string{"退货", "退", "return"}},
	{Question: "发货时间", Answer: "下单后 24 小时内发货，顺丰包邮。", Keywords: []string{"发货", "物流", "快递"}},
	{Question: "保修政策", Answer: "整机保修 1 年，外设保修 6 个月。", Keywords: []string{"保修", "维修", "售后"}},
	{Question: "支付方式", Answer: "支持微信、支付宝、银行卡，支持花呗分期。", Keywords: []string{"支付", "付款", "花呗"}},
	{Question: "发票", Answer: "支持电子发票和纸质发票，下单时可备注。", Keywords: []string{"发票", "开票"}},
}

func main() {
	shutdown := observ.Init("paas-shop-chatbot")
	defer shutdown()

	gatewayURL = os.Getenv("PAAS_GATEWAY_URL")
	if gatewayURL == "" {
		// 模型绑定注入的 PAAS_LLM_BASE_URL（可能含 /v1 后缀，规整去尾，chatbot 统一调 gatewayURL+"/v1/..."）
		gatewayURL = strings.TrimSuffix(os.Getenv("PAAS_LLM_BASE_URL"), "/v1")
		gatewayURL = strings.TrimSuffix(gatewayURL, "/")
	}
	if gatewayURL == "" {
		// paas-core Service port=80（非 Pod 直连 :8080）；集群内访问用默认 :80。
		gatewayURL = "http://paas-core.paas.svc.cluster.local"
	}
	// 优先应用级 Key（模型绑定注入，gateway meter 据此把 token 用量归因到应用）；
	// PAAS_API_KEY（租户级）仅兜底，归因不到具体应用。
	apiKey = os.Getenv("PAAS_LLM_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("PAAS_API_KEY")
	}
	kbID = os.Getenv("PAAS_KB_ID") // 平台知识库 ID（空则 RAG 降级为 fallback 上下文）
	productURL = os.Getenv("PRODUCT_SERVICE_URL")
	if productURL == "" {
		productURL = "http://paas-shop-product:8081"
	}
	model = os.Getenv("PAAS_MODEL")
	if model == "" {
		model = "glm-5.2"
	}
	if apiKey == "" {
		slog.Error("PAAS_API_KEY 未设置（chatbot 需平台 API Key 调 MaaS）")
		os.Exit(1)
	}

	if err := ensureKB(context.Background()); err != nil {
		slog.Error("知识库初始化失败", "err", err)
		os.Exit(1)
	}
	slog.Info("chatbot 服务就绪", "gateway", gatewayURL, "model", model, "kbId", kbID, "product", productURL)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", healthz)
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		observ.MetricsHandler().ServeHTTP(w, r)
	})
	mux.HandleFunc("/chat", chatHandler) // POST {message,userId} -> SSE 流式

	h := observ.Recover(observ.Handler("chatbot", mux))
	srv := &http.Server{Addr: ":8083", Handler: h, ReadHeaderTimeout: 30 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("server 退出", "err", err)
	}
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// chatHandler 处理客服对话：RAG 检索 -> 第一次 LLM 决策（含 tools）-> 工具执行 -> 第二次流式透传。
func chatHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Message string `json:"message"`
		UserID  string `json:"userId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		http.Error(w, "message 必填", http.StatusBadRequest)
		return
	}
	if req.UserID == "" {
		req.UserID = "anon"
	}

	// 1. RAG 检索平台知识库（真向量检索；未配 KB 降级为 fallback 上下文）
	kbContext := retrieveKB(r.Context(), req.Message)

	// 2. 构造 messages（system 含客服人设 + 知识库上下文）
	system := "你是 PaasShop 智能客服，友好专业。只回答商品、订单、售后相关问题。" +
		"可调用 get_product 工具查询商品详情。回答简洁。\n\n【知识库参考】\n" + kbContext
	messages := []map[string]any{
		{"role": "system", "content": system},
		{"role": "user", "content": req.Message},
	}

	// 3. 第一次 non-stream 决策（带 tools）
	resp1, err := callLLM(r.Context(), messages, true, false)
	if err != nil {
		slog.Error("第一次 LLM 调用失败", "err", err)
		http.Error(w, "MaaS 不可用", http.StatusServiceUnavailable)
		return
	}
	// 兼容文本式 function calling：GLM 等模型不返回标准 tool_calls 结构，而在 content 里
	// 输出 <tool_call>{...}</tool_call> 标签。无标准 tool_calls 时从 content 解析补充。
	if len(resp1.ToolCalls) == 0 {
		resp1.ToolCalls = parseToolCallsFromContent(resp1.Content)
	}

	// 4. 处理 tool_calls（function calling）
	if len(resp1.ToolCalls) > 0 {
		// assistant 消息只保留 tool_calls（OpenAI 规范：tool_calls 时 content 置空），
		// 避免第一轮 content 里的思考碎片污染第二轮上下文。
		messages = append(messages, map[string]any{
			"role":       "assistant",
			"content":    "",
			"tool_calls": resp1.ToolCalls,
		})
		for _, tc := range resp1.ToolCalls {
			result := executeTool(r.Context(), tc)
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": tc.ID,
				"content":      result,
			})
			slog.Info("工具调用", "tool", tc.Function.Name, "args", tc.Function.Arguments, "result_len", len(result))
		}
		// 5. 第二次流式（透传 SSE）
		streamLLM(w, r.Context(), messages)
		return
	}

	// 无 tool_calls：直接把第一次的 content 流式输出（模拟打字效果）
	streamContent(w, resp1.Content)
}

// callLLM 调平台 /v1/chat/completions。withTools 决定是否带 function 定义，stream 决定流式。
func callLLM(ctx context.Context, messages []map[string]any, withTools, stream bool) (*LLMResp, error) {
	body := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   stream,
	}
	if withTools {
		body["tools"] = []map[string]any{
			{"type": "function", "function": map[string]any{
				"name":        "get_product",
				"description": "查询商品详情（价格、库存、分类、描述）",
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{"type": "integer", "description": "商品 ID"},
					},
					"required": []string{"id"},
				},
			}},
		}
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL+"/v1/chat/completions", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("LLM status %d: %s", resp.StatusCode, string(b))
	}
	// 平台 gateway 即便 stream:false 也以 SSE 返回（见 core gateway openai.go：Stream 硬编码 true），
	// 非 stream 路径同样按 SSE 解析：逐行读 `data: <json>`，累积 delta.content + delta.tool_calls 分片，
	// 组装成 LLMResp。tool_calls 流式按 index 分片（首帧含 id/name，后续帧拼 arguments 增量）。
	var out LLMResp
	var content strings.Builder
	byIdx := map[int]*ToolCall{}
	var order []int
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // 跳过非 JSON 帧（心跳/异常），不中断累积
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		d := chunk.Choices[0].Delta
		if d.Content != "" {
			content.WriteString(d.Content)
		}
		for _, dtc := range d.ToolCalls {
			tc, ok := byIdx[dtc.Index]
			if !ok {
				tc = &ToolCall{}
				byIdx[dtc.Index] = tc
				order = append(order, dtc.Index)
			}
			if dtc.ID != "" {
				tc.ID = dtc.ID
			}
			if dtc.Type != "" {
				tc.Type = dtc.Type
			}
			if dtc.Function.Name != "" {
				tc.Function.Name = dtc.Function.Name
			}
			tc.Function.Arguments += dtc.Function.Arguments
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read sse: %w", err)
	}
	out.Content = content.String()
	out.ToolCalls = make([]ToolCall, 0, len(order))
	for _, i := range order {
		out.ToolCalls = append(out.ToolCalls, *byIdx[i])
	}
	return &out, nil
}

// streamLLM 流式调平台，透传 SSE chunks 到前端。
func streamLLM(w http.ResponseWriter, ctx context.Context, messages []map[string]any) {
	body := map[string]any{"model": model, "messages": messages, "stream": true}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL+"/v1/chat/completions", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := streamClient.Do(req)
	if err != nil {
		http.Error(w, "MaaS 不可用", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := line[6:]
		if payload == "[DONE]" {
			fmt.Fprint(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		// 透传 chunk（含 content + reasoning_content）
		fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// streamContent 模拟流式输出非流式 content（打字效果）。
func streamContent(w http.ResponseWriter, content string) {
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	// 包装成 OpenAI SSE chunk 格式（前端复用解析逻辑）
	for _, r := range content {
		chunk := map[string]any{
			"choices": []map[string]any{{
				"delta": map[string]any{"content": string(r)},
				"index":  0,
			}},
		}
		b, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", string(b))
		if flusher != nil {
			flusher.Flush()
		}
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// executeTool 执行 function calling 工具（get_product -> 调 product 服务）。
func executeTool(ctx context.Context, tc ToolCall) string {
	if tc.Function.Name != "get_product" {
		return `{"error":"unknown tool"}`
	}
	// 兼容模型实际输出的参数名（schema 定义 id，GLM 常输出 product_id）+ int/string 型数字。
	// json.Number 兼容 {"id": 1}（int）与 {"id": "1"}（string），避免 int→string 类型不匹配报错。
	var args struct {
		ID        json.Number `json:"id"`
		ProductID json.Number `json:"product_id"`
	}
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
		return `{"error":"invalid args"}`
	}
	pidStr := args.ID.String()
	if pidStr == "" {
		pidStr = args.ProductID.String()
	}
	pid, _ := strconv.Atoi(pidStr)
	if pid == 0 {
		return `{"error":"invalid product id"}`
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/products/%d", productURL, pid), nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Sprintf(`{"error":"product unavailable: %v"}`, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// toolCallRe 匹配 GLM 等模型文本式 function calling 的 <tool_call>{...}</tool_call> 标签。
var toolCallRe = regexp.MustCompile(`(?s)<tool_call>\s*(\{.*?\})\s*</tool_call>`)

// parseToolCallsFromContent 从 content 解析文本式 tool_call 标签为 ToolCall 切片。
// GLM 等模型不返回 OpenAI 标准 tool_calls 结构，而在 content 里输出标签；本函数把标签里的
// {name, arguments} 归一化为 ToolCall（arguments 转字符串，与标准 tool_calls 一致）。
func parseToolCallsFromContent(content string) []ToolCall {
	matches := toolCallRe.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	var out []ToolCall
	for i, m := range matches {
		var raw struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(m[1]), &raw); err != nil || raw.Name == "" {
			continue
		}
		tc := ToolCall{ID: fmt.Sprintf("call_text_%d", i), Type: "function"}
		tc.Function.Name = raw.Name
		tc.Function.Arguments = strings.TrimSpace(string(raw.Arguments))
		out = append(out, tc)
	}
	return out
}

// --- 知识库（平台 KB retrieve API）---

// ensureKB 校验 KB 配置。kbID 空则降级（fallback 上下文），非空记日志。
func ensureKB(ctx context.Context) error {
	_ = ctx
	if kbID == "" {
		slog.Warn("PAAS_KB_ID 未设置，RAG 降级为 fallback 上下文（非真向量检索）")
		return nil
	}
	slog.Info("KB RAG 就绪", "kbId", kbID, "retrieve", gatewayURL+"/api/knowledgebases/"+kbID+"/retrieve")
	return nil
}

// retrieveKB 调平台 /api/knowledgebases/{id}/retrieve（真向量检索 + score 排序）。
// 失败/未配 KB 降级为 fallback 上下文（演示连续性，chatbot 仍可推理）。
func retrieveKB(ctx context.Context, query string) string {
	if kbID == "" {
		return buildFallbackContext(query)
	}
	body, _ := json.Marshal(map[string]string{"query": query})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL+"/api/knowledgebases/"+kbID+"/retrieve", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := httpClient.Do(req)
	if err != nil {
		slog.Warn("KB retrieve 失败，降级 fallback", "err", err)
		return buildFallbackContext(query)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Warn("KB retrieve 非 200，降级 fallback", "status", resp.StatusCode)
		return buildFallbackContext(query)
	}
	var out struct {
		Data []struct {
			Chunk struct {
				Content string `json:"content"`
			} `json:"chunk"`
			Score float32 `json:"score"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return buildFallbackContext(query)
	}
	var b strings.Builder
	for _, h := range out.Data {
		if strings.TrimSpace(h.Chunk.Content) != "" {
			fmt.Fprintf(&b, "- %s\n", h.Chunk.Content)
		}
	}
	if b.Len() == 0 {
		return "（无匹配知识库条目）"
	}
	return b.String()
}

// buildFallbackContext 在平台 KB 不可用时用本地 fallback FAQ 关键词匹配作降级上下文。
func buildFallbackContext(query string) string {
	q := strings.ToLower(query)
	var b strings.Builder
	matched := false
	for _, f := range fallbackFAQ {
		kws := append([]string{strings.ToLower(f.Question)}, f.Keywords...)
		hit := false
		for _, k := range kws {
			if k != "" && strings.Contains(q, strings.ToLower(k)) {
				hit = true
				break
			}
		}
		if hit {
			fmt.Fprintf(&b, "Q: %s\nA: %s\n\n", f.Question, f.Answer)
			matched = true
		}
	}
	if !matched {
		return "（无匹配知识库条目）"
	}
	return b.String()
}

// --- LLM 响应解析 ---

type LLMResp struct {
	Choices []struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Content   string
	ToolCalls []ToolCall
}

func (r *LLMResp) Message() map[string]any {
	return map[string]any{
		"role":       "assistant",
		"content":    r.Content,
		"tool_calls": r.ToolCalls,
	}
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
