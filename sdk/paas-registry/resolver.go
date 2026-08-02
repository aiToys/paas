// Package paasregistry 是 zeus registry 的 PaaS 适配插件（paas:// scheme）。
//
// 把 PaaS 数据面 API（/dp/，由 paas-core 暴露）适配为 zeus 的服务发现后端：
// zeus 应用经此插件发现同租户、同命名空间下其他 PaaS 工作负载的 ready 实例
// （真源 = K8s Endpoints，readiness probe 驱动）。
//
// 启用方式：
//
//	import _ "github.com/aitoys/paas/sdk/paas-registry"
//	// ZEUS_REGISTRY=paas://paas-core.paas.svc/dp?token=<dp-token>
//
// URL 格式：paas://<host>[:port]/dp?token=<api-key>
//   - host：paas-core 的 K8s Service DNS 名（如 paas-core.paas.svc）。
//   - /dp：数据面 API 前缀。
//   - token：数据面 API Key（绑 tenant，由 PaaS 工作负载 controller 注入 Pod env PAAS_DP_TOKEN）。
//
// 仿 zeus plugins/registry/etcd 的 resolver 模式：init() 注册 scheme，resolveFromURL 解析 URL。
// 返回的 *paasRegistry 同时实现 Registrar + Discovery + Watcher（zeus app 类型断言取各接口）。
package paasregistry

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-zeus/zeus/app"
	"github.com/go-zeus/zeus/registry"
)

func init() {
	app.RegisterRegistryResolver("paas", resolveFromURL)
}

// resolveFromURL 把 "paas://<host>/dp?token=<>" 解析为 paasRegistry 实例。
func resolveFromURL(rawURL string) (registry.Registrar, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("paas registry: invalid URL %q: %w", rawURL, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("paas registry: URL 缺少 host: %q", rawURL)
	}
	// scheme paas:// 固定映射 http://（集群内 core Service；https 留后续）。
	base := "http://" + u.Host + u.Path
	return &paasRegistry{
		base:   strings.TrimRight(base, "/"),
		token:  u.Query().Get("token"),
		client: &http.Client{Timeout: 10 * time.Second},
	}, nil
}
