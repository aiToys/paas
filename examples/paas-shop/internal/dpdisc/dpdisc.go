// Package dpdisc 提供 paas-shop 的数据面服务发现能力（平台 /dp/instances 端点）。
//
// 平台 dataplane 从 K8s Endpoints 读 ready 实例（readiness probe 驱动），
// 支持泳道参数（lane=feature-x 优先返泳道实例，缺失降级 default 基线——L2 联调核心）。
// bff 经本包发现下游 product/recommend/chatbot，替代硬编码 Service URL，
// 演示「控制面只存期望态、数据面读真实运行态」的解耦。
//
// 端点：{PAAS_API_BASE}/dp/instances?service=<name>&lane=<lane>（Bearer dp token = API Key）。
// PAAS_DP_URL 未配 -> 不启用（调用方用静态 URL 兜底）。
package dpdisc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"
)

// Instance 一个 ready 实例（Addr=ip:port）。
type Instance struct {
	Addr   string            `json:"addr"`
	Status string            `json:"status"`
	Meta   map[string]string `json:"meta,omitempty"`
}

// Discoverer 封装 /dp/instances 查询 + 短缓存（10s，防每请求打平台）。
type Discoverer struct {
	base  string // http://paas-core.paas.svc...
	token string
	http  *http.Client

	mu      sync.Mutex
	cache   map[string]cacheEnt // key=service|lane
	nowFunc func() time.Time    // 测试可注入
}

type cacheEnt struct {
	at        time.Time
	instances []Instance
}

// New 从 env 构造：PAAS_DP_URL（如 http://paas-core.paas.svc.cluster.local）+ PAAS_DP_TOKEN（API Key）。
// url 空 -> nil（不启用）。
func New() *Discoverer {
	u := os.Getenv("PAAS_DP_URL")
	if u == "" {
		return nil
	}
	return &Discoverer{
		base:    u,
		token:   os.Getenv("PAAS_DP_TOKEN"),
		http:    &http.Client{Timeout: 3 * time.Second},
		cache:   map[string]cacheEnt{},
		nowFunc: time.Now,
	}
}

// Enabled 是否启用（nil receiver 安全）。
func (d *Discoverer) Enabled() bool { return d != nil }

// Instances 查服务实例（10s 缓存）。不可用/失败/空 -> 调用方走静态 URL 兜底。
func (d *Discoverer) Instances(ctx context.Context, service, lane string) []Instance {
	if d == nil {
		return nil
	}
	key := service + "|" + lane
	d.mu.Lock()
	if e, ok := d.cache[key]; ok && d.nowFunc().Sub(e.at) < 10*time.Second {
		d.mu.Unlock()
		return e.instances
	}
	d.mu.Unlock()

	ins := d.fetch(ctx, service, lane)
	d.mu.Lock()
	d.cache[key] = cacheEnt{at: d.nowFunc(), instances: ins}
	d.mu.Unlock()
	return ins
}

// Addr 取一个可用地址（http://ip:port）。无实例返空（调用方兜底静态 URL）。
func (d *Discoverer) Addr(ctx context.Context, service, lane string) string {
	ins := d.Instances(ctx, service, lane)
	for _, i := range ins {
		if i.Status == "healthy" && i.Addr != "" {
			return "http://" + i.Addr
		}
	}
	if len(ins) > 0 && ins[0].Addr != "" { // 无 healthy 标记也取首个（平台侧均为 ready 才返回）
		return "http://" + ins[0].Addr
	}
	return ""
}

func (d *Discoverer) fetch(ctx context.Context, service, lane string) []Instance {
	u := fmt.Sprintf("%s/dp/instances?service=%s", d.base, service)
	if lane != "" {
		u += "&lane=" + lane
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil
	}
	if d.token != "" {
		req.Header.Set("Authorization", "Bearer "+d.token)
	}
	resp, err := d.http.Do(req)
	if err != nil {
		slog.Warn("dp 发现失败（兜底静态 URL）", "service", service, "err", err)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	// {data:T} 包裹契约（与平台 API 统一）。
	var out struct {
		Data struct {
			Instances []Instance `json:"instances"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil
	}
	return out.Data.Instances
}
