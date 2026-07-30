package maas

import (
	"context"
	"fmt"

	"github.com/aitoys/paas/pkg/plugin"
	"github.com/aitoys/paas/pkg/provider"
)

// MaaSPlugin 实现 plugin.Plugin。
// 在 Init 阶段把 echo provider 注册到 Core 注入的 Gateway，验证插件契约。
type MaaSPlugin struct {
	gw provider.GatewayRegistrar
}

func (m *MaaSPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{Name: "maas", Version: "v0.1.0"}
}

func (m *MaaSPlugin) Routes() []plugin.RouteSpec {
	return []plugin.RouteSpec{
		{Path: "POST /v1/chat/completions", Require: "maas:infer"},
		{Path: "GET /v1/models", Require: "maas:read"},
	}
}

func (m *MaaSPlugin) Schemas() []plugin.CRDSchema { return nil }

func (m *MaaSPlugin) Meters() []plugin.MeterSpec {
	return []plugin.MeterSpec{{Name: "tokens", Unit: "count"}}
}

func (m *MaaSPlugin) Init(_ context.Context, deps plugin.CoreDeps) error {
	m.gw = deps.Gateway()
	if m.gw == nil {
		return fmt.Errorf("gateway registrar 未注入")
	}
	// 加载 seed 模型目录（真实第三方通道 + mock 演示）注册到 Gateway。
	// resolver 用于真实通道运行时解析平台级凭证；nil 时真实通道调用返回 ErrCredentialMissing。
	resolver := deps.SecretResolver()
	for _, model := range catalog(resolver) {
		if err := m.gw.RegisterModel(model); err != nil {
			return fmt.Errorf("注册模型 %s 失败: %w", model.ID, err)
		}
	}
	return nil
}

func (m *MaaSPlugin) Run(_ context.Context) error { return nil }
