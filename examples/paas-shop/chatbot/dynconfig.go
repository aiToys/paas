// 动态配置消费客户端：按应用名发现 + 拉取平台配置中心已发布快照（60s 轮询热更新）。
//
// 平台能力：配置中心应用维度端点 GET /api/configcenter/apps/{appName}/published
// （Bearer API Key 鉴权；未发布/未知应用统一 {"published":false} 不泄漏）。
//
// 降级语义：拉取失败（网络/未发布）保持旧值或默认值继续服务，绝不 panic——
// 配置中心不可用不能拖死业务；连续失败只告警一次，恢复成功后重置。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

const (
	pollInterval = 60 * time.Second
	// 消费的配置 key 与默认值。
	keyWelcome  = "welcome_message"
	defWelcome  = "你好，我是小店智能助手"
	keyTopK     = "recommend_topk"
	defTopK     = 3
	fetchTimout = 5 * time.Second
)

// dynConfig 是配置中心的进程内只读视图（version 比对 + 原子替换）。
type dynConfig struct {
	coreURL string // 平台 core 地址（PAAS_CORE_URL）
	appName string // 应用名（PAAS_CONFIG_APP）
	apiKey  string

	client   *http.Client
	interval time.Duration // 轮询间隔（测试可注入缩短）

	mu      sync.RWMutex
	cfg     map[string]string // 最近一次已发布快照（未发布/未拉到为空 map）
	version int
	// 告警限频：仅成功->失败转变时打一次 Warn，恢复后重置
	failing bool
}

// newDynConfig 从 env 构建配置客户端（不发起网络请求）。
func newDynConfig() *dynConfig {
	coreURL := os.Getenv("PAAS_CORE_URL")
	if coreURL == "" {
		coreURL = "http://paas-core.paas.svc.cluster.local" // 与 gateway 兜底同址（Service port=80）
	}
	appName := os.Getenv("PAAS_CONFIG_APP")
	if appName == "" {
		appName = "chatbot"
	}
	return &dynConfig{
		coreURL: coreURL,
		appName: appName,
		apiKey:  apiKey,
		client:  &http.Client{Timeout: fetchTimout},
		cfg:     map[string]string{},
	}
}

// publishedResp 对齐平台端点裸 JSON 契约。
type publishedResp struct {
	Published bool              `json:"published"`
	Version   int               `json:"version"`
	Snapshot  map[string]string `json:"snapshot"`
}

// refresh 拉取一次已发布快照：version 变化才原子替换；失败/未发布保持旧值。
func (d *dynConfig) refresh(ctx context.Context) {
	url := fmt.Sprintf("%s/api/configcenter/apps/%s/published", d.coreURL, d.appName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		d.warnOnce("构建请求失败", err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+d.apiKey)
	resp, err := d.client.Do(req)
	if err != nil {
		d.warnOnce("拉取动态配置失败，保持旧值", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		d.warnOnce("拉取动态配置非 200，保持旧值", fmt.Errorf("status=%d body=%s", resp.StatusCode, b))
		return
	}
	var pr publishedResp
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&pr); err != nil {
		d.warnOnce("解析动态配置失败，保持旧值", err)
		return
	}
	if !pr.Published {
		// 未发布：回默认值（空 map，Get 走 default）
		d.swap(pr.Version, map[string]string{})
		d.ok()
		return
	}
	d.mu.RLock()
	same := pr.Version == d.version
	d.mu.RUnlock()
	if same {
		d.ok()
		return // version 未变，跳过替换
	}
	snap := pr.Snapshot
	if snap == nil {
		snap = map[string]string{}
	}
	d.swap(pr.Version, snap)
	d.ok()
	slog.Info("动态配置已更新", "app", d.appName, "version", pr.Version, "keys", len(snap))
}

// swap 原子替换快照（RWMutex 保护，读侧无锁竞争）。
func (d *dynConfig) swap(version int, snap map[string]string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cfg = snap
	d.version = version
}

// warnOnce 连续失败只告警一次，恢复成功后重置（不刷屏）。
func (d *dynConfig) warnOnce(msg string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failing {
		return
	}
	d.failing = true
	slog.Warn(msg, "app", d.appName, "err", err)
}

func (d *dynConfig) ok() {
	d.mu.Lock()
	d.failing = false
	d.mu.Unlock()
}

// Get 读取配置值，缺失返回 false（调用方用默认值）。
func (d *dynConfig) Get(key string) (string, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	v, ok := d.cfg[key]
	return v, ok
}

// Welcome 返回欢迎语（未配置用默认）。
func (d *dynConfig) Welcome() string {
	if v, ok := d.Get(keyWelcome); ok && v != "" {
		return v
	}
	return defWelcome
}

// TopK 返回推荐条数（未配置/非法用默认 3）。
func (d *dynConfig) TopK() int {
	if v, ok := d.Get(keyTopK); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defTopK
}

// Start 启动轮询：立即拉一次，之后每 interval（默认 60s）拉取（ctx 取消退出）。不阻塞调用方。
func (d *dynConfig) Start(ctx context.Context) {
	if d.interval <= 0 {
		d.interval = pollInterval
	}
	d.refresh(ctx)
	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.refresh(ctx)
		}
	}
}
