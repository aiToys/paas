// Package dpdisc 提供 paas-shop 的数据面服务发现能力（平台 /dp/instances 端点）。
//
// 平台 dataplane 从 K8s Endpoints 读 ready 实例（readiness probe 驱动），
// 支持泳道参数（lane=feature-x 优先返泳道实例，缺失降级 default 基线——L2 联调核心）。
// bff 经本包发现下游 product/recommend/chatbot，替代硬编码 Service URL，
// 演示「控制面只存期望态、数据面读真实运行态」的解耦。
//
// 端点：{PAAS_DP_ENDPOINT}/instances?service=<name>&lane=<lane>（Bearer dp token = API Key）。
// /dp/* 是平台裸 JSON 契约例外（无 {data} 包裹；instance 字段为 ip/port 而非 addr）。
// PAAS_DP_ENDPOINT 未配 -> 不启用（调用方用静态 URL 兜底）。
package dpdisc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Instance 一个 ready 实例（/dp/instances 裸 JSON 字段：ip/port/laneId）。
type Instance struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	IP   string `json:"ip"`
	Port int    `json:"port"`
	Lane string `json:"laneId"`
}

// addr 拼访问地址（ip:port）。
func (i Instance) addr() string {
	if i.IP == "" || i.Port == 0 {
		return ""
	}
	return net.JoinHostPort(i.IP, strconv.Itoa(i.Port))
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

// New 从 env 构造：PAAS_DP_ENDPOINT（平台 controller 注入，如 http://paas-core.paas.svc.cluster.local/dp）
// + PAAS_DP_TOKEN（API Key）。endpoint 空 -> nil（不启用）。
func New() *Discoverer {
	u := os.Getenv("PAAS_DP_ENDPOINT")
	if u == "" {
		u = os.Getenv("PAAS_DP_URL") // 兼容旧名（本地 dev 直连）
	}
	if u == "" {
		return nil
	}
	return &Discoverer{
		base:    strings.TrimRight(u, "/"),
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
// 平台侧仅返回 ready 实例，此处直接取首个有地址的。
func (d *Discoverer) Addr(ctx context.Context, service, lane string) string {
	for _, i := range d.Instances(ctx, service, lane) {
		if a := i.addr(); a != "" {
			return "http://" + a
		}
	}
	return ""
}

func (d *Discoverer) fetch(ctx context.Context, service, lane string) []Instance {
	u := fmt.Sprintf("%s/instances?service=%s", d.base, service)
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
	// /dp/* 裸 JSON 契约（平台例外，无 {data} 包裹）。
	var out struct {
		Instances []Instance `json:"instances"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil
	}
	return out.Instances
}
