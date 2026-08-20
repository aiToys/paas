// Package ccpull 提供 paas-shop 的配置中心动态拉取能力（平台 configcenter published 端点）。
//
// 与 appconfig（env 注入，重启生效）正交：configcenter 是运行时动态配置，版本化 + 热更新。
// 轮询 published 版本号，变更时回调应用（onUpdate），无需重启。
//
// 端点：{PAAS_API_BASE}/api/configcenter/namespaces/{ns}/published（返回 {published,version,snapshot}）。
// PAAS_CONFIGCENTER_NS 未配 -> 不启动（最小部署不崩）。
package ccpull

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"
)

// Puller 轮询配置中心 published 快照，版本变化时回调。
type Puller struct {
	apiBase string // 平台 core 根地址（http://paas-core.paas.svc...）
	ns      string // namespace（如 shop-runtime）
	apiKey  string // 平台 API Key（governance:read）
	http    *http.Client

	version  string              // 当前版本（""=首次拉取也回调）
	snapshot map[string]string   // 当前快照
	updated  time.Time
}

// New 从 env 构造：PAAS_API_BASE/PAAS_CONFIGCENTER_NS/PAAS_API_KEY。ns 空 -> nil（不启用）。
func New() *Puller {
	ns := os.Getenv("PAAS_CONFIGCENTER_NS")
	if ns == "" {
		return nil
	}
	return &Puller{
		apiBase: envOr("PAAS_API_BASE", "http://paas-core.paas.svc.cluster.local"),
		ns:      ns,
		apiKey:  os.Getenv("PAAS_API_KEY"),
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// Start 启动轮询 goroutine（间隔 10s；变更时调 onUpdate(新快照)）。
// ctx 取消退出（进程 shutdown 时不泄漏）。首次拉取成功即回调（含启动配置）。
func (p *Puller) Start(ctx context.Context, interval time.Duration, onUpdate func(map[string]string)) {
	if p == nil {
		return
	}
	if interval <= 0 {
		interval = 10 * time.Second
	}
	go func() {
		tk := time.NewTicker(interval)
		defer tk.Stop()
		for {
			if snap, ver, ok := p.fetchOnce(ctx); ok {
				if ver != p.version {
					slog.Info("配置中心变更生效", "ns", p.ns, "version", ver, "keys", len(snap))
					p.version, p.snapshot, p.updated = ver, snap, time.Now()
					if onUpdate != nil {
						onUpdate(snap)
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
			}
		}
	}()
}

// fetchOnce 拉一次 published；未发布/失败返 ok=false（保持现值，下轮重试）。
func (p *Puller) fetchOnce(ctx context.Context) (map[string]string, string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		p.apiBase+"/api/configcenter/namespaces/"+p.ns+"/published", nil)
	if err != nil {
		return nil, "", false
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", false // 404=未发布，正常静默
	}
	var out struct {
		Published bool              `json:"published"`
		Version   int               `json:"version"`
		Snapshot  map[string]string `json:"snapshot"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, "", false
	}
	if !out.Published {
		return nil, "", false
	}
	return out.Snapshot, fmt.Sprintf("%d", out.Version), true
}

// Snapshot 当前生效快照（只读副本，未拉到返 nil）。
func (p *Puller) Snapshot() map[string]string {
	if p == nil || p.snapshot == nil {
		return nil
	}
	out := make(map[string]string, len(p.snapshot))
	for k, v := range p.snapshot {
		out[k] = v
	}
	return out
}

// Version 当前版本号。
func (p *Puller) Version() string {
	if p == nil {
		return ""
	}
	return p.version
}
