package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeServer 可动态切换响应的假平台（模拟 version 变化 / 下线 / 宕机）。
type fakeServer struct {
	srv      *httptest.Server
	payload  atomic.Value // string：当前 JSON 响应体；空串 = 模拟宕机（连接拒绝）
	status   atomic.Int64
	requests atomic.Int64
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{}
	f.status.Store(200)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.requests.Add(1)
		if p, _ := f.payload.Load().(string); p == "" {
			// 模拟宕机：劫持连接
			panicClose(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(int(f.status.Load()))
		_, _ = w.Write([]byte(f.payload.Load().(string)))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// panicClose 用 hijack 断开连接模拟网络失败（httptest 无法直接拒连）。
func panicClose(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if conn, _, err := hj.Hijack(); err == nil {
		_ = conn.Close()
	}
}

func newTestConfig(coreURL string) *dynConfig {
	return &dynConfig{
		coreURL:  coreURL,
		appName:  "chatbot",
		apiKey:   "sk-test",
		client:   &http.Client{Timeout: 2 * time.Second},
		interval: 50 * time.Millisecond,
		cfg:      map[string]string{},
	}
}

// 首次拉取未发布：保持默认值（空快照 -> Get 缺失走 default）。
func TestDynConfigUnpublishedUsesDefaults(t *testing.T) {
	f := newFakeServer(t)
	f.payload.Store(`{"published":false,"version":0,"snapshot":{}}`)
	d := newTestConfig(f.srv.URL)
	d.refresh(context.Background())
	if _, ok := d.Get(keyWelcome); ok {
		t.Fatal("未发布不应有 welcome_message")
	}
	if got := d.Welcome(); got != defWelcome {
		t.Fatalf("Welcome = %q, want default %q", got, defWelcome)
	}
	if got := d.TopK(); got != defTopK {
		t.Fatalf("TopK = %d, want default %d", got, defTopK)
	}
}

// version 变化后热替换快照；welcome / topk 均生效。
func TestDynConfigHotSwapOnVersionChange(t *testing.T) {
	f := newFakeServer(t)
	f.payload.Store(`{"published":true,"version":1,"snapshot":{"welcome_message":"你好","recommend_topk":"5"}}`)
	d := newTestConfig(f.srv.URL)
	d.refresh(context.Background())
	if got := d.Welcome(); got != "你好" {
		t.Fatalf("Welcome = %q, want 你好", got)
	}
	if got := d.TopK(); got != 5 {
		t.Fatalf("TopK = %d, want 5", got)
	}

	f.payload.Store(`{"published":true,"version":2,"snapshot":{"welcome_message":"欢迎光临","recommend_topk":"1"}}`)
	d.refresh(context.Background())
	if got := d.Welcome(); got != "欢迎光临" {
		t.Fatalf("Welcome = %q, want 欢迎光临（热更新未生效）", got)
	}
	if got := d.TopK(); got != 1 {
		t.Fatalf("TopK = %d, want 1", got)
	}
}

// 同 version 重复拉取不重复替换（幂等）。
func TestDynConfigSameVersionNoSwap(t *testing.T) {
	f := newFakeServer(t)
	f.payload.Store(`{"published":true,"version":1,"snapshot":{"welcome_message":"v1"}}`)
	d := newTestConfig(f.srv.URL)
	d.refresh(context.Background())
	d.refresh(context.Background()) // 同 version
	if got := d.Welcome(); got != "v1" {
		t.Fatalf("Welcome = %q, want v1", got)
	}
}

// 服务宕机：保持旧值不 panic，不刷屏（连续失败只告警一次）；恢复后继续热更新。
func TestDynConfigFailureKeepsOldValue(t *testing.T) {
	f := newFakeServer(t)
	f.payload.Store(`{"published":true,"version":7,"snapshot":{"welcome_message":"旧值","recommend_topk":"2"}}`)
	d := newTestConfig(f.srv.URL)
	d.refresh(context.Background())

	f.payload.Store("") // 模拟宕机
	d.refresh(context.Background())
	d.refresh(context.Background())
	if got := d.Welcome(); got != "旧值" {
		t.Fatalf("宕机后 Welcome = %q, want 旧值（旧值被丢失）", got)
	}
	if got := d.TopK(); got != 2 {
		t.Fatalf("宕机后 TopK = %d, want 2", got)
	}

	// 恢复：version 更高则继续热替换
	f.payload.Store(`{"published":true,"version":8,"snapshot":{"welcome_message":"新值"}}`)
	d.refresh(context.Background())
	if got := d.Welcome(); got != "新值" {
		t.Fatalf("恢复后 Welcome = %q, want 新值", got)
	}
}

// TopK 非法值（非数字/<=0）回默认。
func TestDynConfigTopKInvalid(t *testing.T) {
	f := newFakeServer(t)
	f.payload.Store(`{"published":true,"version":1,"snapshot":{"recommend_topk":"abc"}}`)
	d := newTestConfig(f.srv.URL)
	d.refresh(context.Background())
	if got := d.TopK(); got != defTopK {
		t.Fatalf("TopK = %d, want default %d", got, defTopK)
	}
	f.payload.Store(`{"published":true,"version":2,"snapshot":{"recommend_topk":"-3"}}`)
	d.refresh(context.Background())
	if got := d.TopK(); got != defTopK {
		t.Fatalf("TopK = %d, want default %d", got, defTopK)
	}
}

// Start 轮询：间隔内检测到 version 变化即热更新；ctx 取消退出。
func TestDynConfigStartPolls(t *testing.T) {
	f := newFakeServer(t)
	f.payload.Store(`{"published":true,"version":1,"snapshot":{"welcome_message":"第一版"}}`)
	d := newTestConfig(f.srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { d.Start(ctx); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && d.Welcome() != "第一版" {
		time.Sleep(10 * time.Millisecond)
	}
	if d.Welcome() != "第一版" {
		t.Fatalf("Start 未完成首次拉取，Welcome = %q", d.Welcome())
	}

	f.payload.Store(`{"published":true,"version":2,"snapshot":{"welcome_message":"第二版"}}`)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && d.Welcome() != "第二版" {
		time.Sleep(20 * time.Millisecond)
	}
	if d.Welcome() != "第二版" {
		t.Fatalf("轮询未热更新，Welcome = %q", d.Welcome())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 Start 未退出")
	}
}

// env/lane 注入后：发现 URL 带 ?env=&lane=（泳道配置 merge 链路）。
func TestDynConfigDiscoveryURLCarriesEnvLane(t *testing.T) {
	f := newFakeServer(t)
	var gotURL atomic.Value
	f.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL.Store(r.URL.String())
		f.requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"published":true,"version":1,"snapshot":{}}`))
	})
	d := newTestConfig(f.srv.URL)
	d.envID = "env-t"
	d.laneID = "feature-x"
	d.refresh(context.Background())
	u, _ := gotURL.Load().(string)
	if u != "/api/configcenter/apps/chatbot/published?env=env-t&lane=feature-x" {
		t.Fatalf("发现 URL = %q, want env/lane query", u)
	}
}

// overrideHash 变化（version 不变）也热替换——lane 覆盖即时生效的关键。
func TestDynConfigHotSwapOnOverrideHashChange(t *testing.T) {
	f := newFakeServer(t)
	f.payload.Store(`{"published":true,"version":1,"snapshot":{"recommend_topk":"3"}}`)
	d := newTestConfig(f.srv.URL)
	d.refresh(context.Background())
	if got := d.TopK(); got != 3 {
		t.Fatalf("TopK = %d, want 3", got)
	}

	// lane 覆盖生效：version 不变、snapshot merge 后变化、overrideHash 出现
	f.payload.Store(`{"published":true,"version":1,"snapshot":{"recommend_topk":"5"},"overrideHash":"ab12cd34"}`)
	d.refresh(context.Background())
	if got := d.TopK(); got != 5 {
		t.Fatalf("overrideHash 变化后 TopK = %d, want 5（lane 覆盖未热替换）", got)
	}

	// 同 version 同 hash：不重复替换（幂等）
	d.refresh(context.Background())
	if got := d.TopK(); got != 5 {
		t.Fatalf("幂等拉取后 TopK = %d, want 5", got)
	}

	// 覆盖消失（回收泳道）：hash 回空，回落基线
	f.payload.Store(`{"published":true,"version":1,"snapshot":{"recommend_topk":"3"}}`)
	d.refresh(context.Background())
	if got := d.TopK(); got != 3 {
		t.Fatalf("覆盖消失后 TopK = %d, want 3（回落基线）", got)
	}
}
