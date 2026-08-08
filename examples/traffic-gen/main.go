// Command traffic-gen 是平台流量生成示例：定期调用微服务链 + AI Agent，保障调用链一直有流量。
//
// 两种运行模式：
//   - 默认（无参）：常驻 Deployment，并行跑「微服务链循环」+「AI Agent 循环（带会话记忆）」
//   - once：单次调用微服务链后退出（CronJob 用，每次 Pod 新建，无状态）
//
// AI Agent 记忆：常驻模式内存维护 sessions[sessionId] -> []message，每次调用带上历史消息，
// Agent 可引用上文（如「总结我刚才的咨询」）。Pod 重启历史丢失（演示可接受；生产用 redis 持久化）。
//
// 环境变量：
//   CORE_URL        core API 地址（如 http://paas-core.paas.svc，Service port=80）
//   API_KEY         平台 API Key（程序化调用，绑 developer 角色）
//   AGENT_MODEL     Agent 虚拟模型（如 agent:xxx）
//   SHOP_BFF_URL    paas-shop bff 根 URL（配了调 /api/products + /api/recommend 多端点，产全链路流量）
//   REC_SVC_URL     推荐服务 URL（兼容单端点；SHOP_BFF_URL 优先）
//   MICRO_INTERVAL  微服务调用间隔秒（默认 300）
//   AGENT_INTERVAL  Agent 调用间隔秒（默认 600）
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

var (
	mu       sync.Mutex
	sessions = map[string][]message{} // sessionId -> 会话历史（常驻模式记忆）
)

// 对话脚本：轮转提问，验证 Agent 记忆（后续问题引用上文）。
var prompts = []string{
	"帮我查一下订单 ORD-1001 的状态",
	"这个订单能退款吗？原因：商品损坏",
	"ORD-1003 发货了吗？",
	"总结一下我刚才的咨询历史",
}

func main() {
	mode := ""
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "once":
		callMicroOnce() // CronJob 单次调用
	default:
		runLoops() // 常驻 Deployment
	}
}

// runLoops 常驻：并行跑微服务循环 + AI Agent 循环。
func runLoops() {
	recSvcURL := env("REC_SVC_URL", "")
	shopBFF := env("SHOP_BFF_URL", "") // paas-shop bff 根 URL（配了调多端点，产全链路流量）
	coreURL := env("CORE_URL", "http://paas-core.paas.svc.cluster.local")
	apiKey := env("API_KEY", "")
	agentModel := env("AGENT_MODEL", "")
	microInterval := envInt("MICRO_INTERVAL", 300)
	agentInterval := envInt("AGENT_INTERVAL", 600)

	log.Printf("[traffic-gen] 启动常驻模式：micro=%ds agent=%ds shopBFF=%q rec=%q agent=%q", microInterval, agentInterval, shopBFF, recSvcURL, agentModel)

	if shopBFF != "" || recSvcURL != "" {
		go loop("micro", microInterval, func() { callMicro(shopBFF, recSvcURL) })
	} else {
		log.Println("[traffic-gen] SHOP_BFF_URL/REC_SVC_URL 均未设置，跳过微服务流量")
	}
	if agentModel != "" && apiKey != "" {
		go loop("agent", agentInterval, func() { callAgent(coreURL, apiKey, agentModel) })
	} else {
		log.Println("[traffic-gen] AGENT_MODEL/API_KEY 未设置，跳过 AI 流量")
	}

	// 阻塞主 goroutine 等 SIGTERM/SIGINT 优雅退出（K8s Pod 终止发 SIGTERM）。
	// 不用 select{}：若两 loop 均未启动，无 goroutine 可调度，Go runtime 判定 deadlock 致进程退出。
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("[traffic-gen] 收到退出信号，终止")
}

func loop(name string, intervalSec int, fn func()) {
	// 启动立即跑一次，之后按间隔循环。
	fn()
	for {
		time.Sleep(time.Duration(intervalSec) * time.Second)
		fn()
	}
}

// callMicroOnce 单次调用微服务链（CronJob 模式）。
func callMicroOnce() {
	shopBFF := env("SHOP_BFF_URL", "")
	recSvcURL := env("REC_SVC_URL", "")
	if shopBFF == "" && recSvcURL == "" {
		log.Println("[traffic-gen:once] SHOP_BFF_URL/REC_SVC_URL 均未设置，退出")
		return
	}
	callMicro(shopBFF, recSvcURL)
}

// callMicro 调微服务链路产流量。优先 paas-shop bff 多端点（/api/products + /api/recommend），
// 否则单端点 GET（REC_SVC_URL 兼容）。每次循环调全部端点，保障调用链持续有流量。
func callMicro(shopBFF, recSvcURL string) {
	client := &http.Client{Timeout: 10 * time.Second}
	var endpoints []string
	if shopBFF != "" {
		endpoints = []string{shopBFF + "/api/products", shopBFF + "/api/recommend"}
	} else if recSvcURL != "" {
		endpoints = []string{recSvcURL}
	}
	for _, ep := range endpoints {
		resp, err := client.Get(ep)
		if err != nil {
			log.Printf("[micro] 调用失败 %s: %v", ep, err)
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		log.Printf("[micro] %s -> %d", ep, resp.StatusCode)
	}
}

// callAgent 调 AI Agent（虚拟模型 agent:xxx），带会话历史保记忆。
func callAgent(coreURL, apiKey, model string) {
	sessionID := "demo-session-001"
	mu.Lock()
	hist := make([]message, len(sessions[sessionID]))
	copy(hist, sessions[sessionID])
	mu.Unlock()

	// 轮转提问（按历史轮次推进），验证记忆：后续问题引用上文。
	idx := len(hist) / 2
	prompt := prompts[idx%len(prompts)]

	msgs := append(hist, message{Role: "user", Content: prompt})
	body, _ := json.Marshal(map[string]any{
		"model":    model,
		"messages": msgs,
		"stream":   true, // Agent 虚拟模型总返 SSE（gateway ServeSSE）；声明流式 + 解析 SSE
	})
	req, err := http.NewRequest(http.MethodPost, coreURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("[agent] 调用失败: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		log.Printf("[agent] HTTP %d: %s", resp.StatusCode, truncate(string(b), 200))
		return
	}
	// 解析 SSE 流：累积 delta.content（跳过 reasoning_content 思考过程）。
	var reply strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var d struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		_ = json.Unmarshal([]byte(payload), &d)
		if len(d.Choices) > 0 {
			reply.WriteString(d.Choices[0].Delta.Content)
		}
	}
	r := reply.String()
	log.Printf("[agent] session=%s 轮次=%d Q:%s | A:%s", sessionID, idx+1, prompt, truncate(r, 120))

	// 更新历史（user + assistant），保留最近 20 条（10 轮）防无限增长。
	mu.Lock()
	sessions[sessionID] = append(hist,
		message{Role: "user", Content: prompt},
		message{Role: "assistant", Content: r},
	)
	if len(sessions[sessionID]) > 20 {
		sessions[sessionID] = sessions[sessionID][len(sessions[sessionID])-20:]
	}
	mu.Unlock()
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func envInt(k string, d int) int {
	if v := os.Getenv(k); v != "" {
		var n int
		_, _ = fmt.Sscanf(v, "%d", &n)
		if n > 0 {
			return n
		}
	}
	return d
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
