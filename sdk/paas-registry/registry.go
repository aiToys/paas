package paasregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/go-zeus/zeus/registry"
	"github.com/go-zeus/zeus/types"
)

// paasRegistry 实现 zeus registry 三接口（Registrar + Discovery + Watcher）。
// 数据源 = PaaS /dp/ API（真源 K8s Endpoints）。仿 zeus examples/20-full-demo gwdisc 的轮询发现模式。
type paasRegistry struct {
	base   string       // 数据面 API 根（如 http://paas-core.paas.svc/dp）
	token  string       // dp token（API Key，Authorization: Bearer）
	client *http.Client
}

// 编译期断言：paasRegistry 同时实现 Registrar + Discovery + Watcher。
var (
	_ registry.Registrar = (*paasRegistry)(nil)
	_ registry.Discovery = (*paasRegistry)(nil)
	_ registry.Watcher   = (*paasRegistry)(nil)
)

// Register 调 POST /dp/register 声明服务元信息（幂等）。
func (r *paasRegistry) Register(ctx context.Context, ins *types.Instance) error {
	body, _ := json.Marshal(dpInstance{
		ID: ins.ID, Name: ins.Name, Cluster: nonEmpty(ins.Cluster, "default"),
		Protocol: nonEmpty(ins.Protocol, "http"), IP: ins.IP, Port: ins.Port,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.base+"/register", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	r.auth(req)
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("paas register: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("paas register: %s", resp.Status)
	}
	return nil
}

// Deregister 调 DELETE /dp/register?id=<id> 反注册（仅删 governance 元信息，K8s Endpoints 自管）。
func (r *paasRegistry) Deregister(ctx context.Context, ins *types.Instance) error {
	u := r.base + "/register?id=" + url.QueryEscape(ins.ID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return err
	}
	r.auth(req)
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("paas deregister: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("paas deregister: %s", resp.Status)
	}
	return nil
}

// GetService 调 GET /dp/instances?service=<name>，解析为 zeus ServiceEntry。
// 返回的实例仅含 ready（PaaS 侧从 K8s Endpoints Addresses 过滤）。
func (r *paasRegistry) GetService(ctx context.Context, name string) (*types.ServiceEntry, error) {
	u := r.base + "/instances?service=" + url.QueryEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	r.auth(req)
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("paas getservice: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("paas getservice: %s", resp.Status)
	}
	var body struct {
		Service   string       `json:"service"`
		Instances []dpInstance `json:"instances"`
		Signature string       `json:"signature"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("paas getservice decode: %w", err)
	}
	se := types.NewServiceEntry()
	if body.Service != "" {
		se.Name = body.Service
	} else {
		se.Name = name
	}
	for _, in := range body.Instances {
		_ = se.AddInstance(in.toZeus())
	}
	return se, nil
}

// Watch 2s 轮询 GetService，签名变化时向 channel 发信号（仿 gwdisc）。
// channel 缓冲 1，消费方据此触发重新 GetService 拉最新实例。
func (r *paasRegistry) Watch(ctx context.Context, name string) (<-chan struct{}, error) {
	ch := make(chan struct{}, 1)
	go func() {
		defer close(ch)
		var lastSig string
		// 首次立即探测一次（不等首个 tick），让发现延迟最小。
		if se, err := r.GetService(ctx, name); err == nil {
			lastSig = entrySignature(se)
			select {
			case ch <- struct{}{}:
			default:
			}
		}
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				se, err := r.GetService(ctx, name)
				if err != nil {
					continue
				}
				sig := entrySignature(se)
				if sig != lastSig {
					lastSig = sig
					select {
					case ch <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	return ch, nil
}

func (r *paasRegistry) auth(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
}

// dpInstance 对齐 PaaS dataplane.Instance（数据面 API 响应格式）。
type dpInstance struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Cluster  string            `json:"cluster"`
	Protocol string            `json:"protocol"`
	IP       string            `json:"ip"`
	Port     int               `json:"port"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// toZeus 转 zeus types.Instance（Port 类型 int 一致）。
func (d dpInstance) toZeus() *types.Instance {
	return &types.Instance{
		ID:       d.ID,
		Name:     d.Name,
		Cluster:  nonEmpty(d.Cluster, "default"),
		Protocol: nonEmpty(d.Protocol, "http"),
		IP:       d.IP,
		Port:     d.Port,
	}
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// entrySignature 对 ServiceEntry 的实例做确定性签名（排序后 sha256，供 Watch 对比变化）。
func entrySignature(se *types.ServiceEntry) string {
	snap := se.Snapshot()
	keys := make([]string, 0, len(snap.Instances))
	for _, in := range snap.Instances {
		keys = append(keys, fmt.Sprintf("%s:%s:%s:%d", in.ID, in.Cluster, in.IP, in.Port))
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "|")))
	return hex.EncodeToString(sum[:])
}
