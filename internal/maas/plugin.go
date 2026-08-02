// Package maas 是 MaaS 插件实现。
package maas

import (
	"context"
	"errors"
	"fmt"

	"github.com/aitoys/paas/pkg/plugin"
	"github.com/aitoys/paas/pkg/provider"
)

// MaaSPlugin 实现 plugin.Plugin。
// Init 阶段从 Repository 加载模型目录（DB 驱动），BuildProvider 重建通道 impl 后注册到 Gateway。
// repo 为 nil 时 fallback 到内置 catalog（兼容直接构造 &MaaSPlugin{} 的旧用法/测试）。
type MaaSPlugin struct {
	gw   provider.GatewayRegistrar
	repo Repository
}

// NewMaaSPlugin 用注入的 Repository 构造插件（cmd/core 装配时调用，store 已 seed）。
func NewMaaSPlugin(repo Repository) *MaaSPlugin {
	return &MaaSPlugin{repo: repo}
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

// loadModels 返回要注册的模型目录：repo 非 nil 从 store 加载（DB 驱动），nil fallback catalog。
func (m *MaaSPlugin) loadModels(ctx context.Context, resolver provider.CredentialResolver) ([]*provider.Model, error) {
	if m.repo != nil {
		return m.repo.ListModels(ctx)
	}
	return catalog(resolver), nil
}

// Init 加载模型目录并注册到 Gateway。
// 每个通道经 BuildProvider 按 Type 重建运行时 Provider 并 SetImpl（impl 不持久化，加载时重建）；
// resolver 用于真实通道运行时解析平台级凭证，nil 时真实通道调用返回 ErrCredentialMissing。
func (m *MaaSPlugin) Init(ctx context.Context, deps plugin.CoreDeps) error {
	m.gw = deps.Gateway()
	if m.gw == nil {
		return fmt.Errorf("gateway registrar 未注入")
	}
	resolver := deps.SecretResolver()
	models, err := m.loadModels(ctx, resolver)
	if err != nil {
		return fmt.Errorf("加载模型目录失败: %w", err)
	}
	for _, model := range models {
		for _, c := range model.Channels {
			c.SetImpl(BuildProvider(c, resolver))
		}
		if err := m.gw.RegisterModel(model); err != nil {
			return fmt.Errorf("注册模型 %s 失败: %w", model.ID, err)
		}
	}
	return nil
}

func (m *MaaSPlugin) Run(_ context.Context) error { return nil }

// SeedCatalog 把 catalog() 模型目录灌入仓储（幂等：已存在跳过）。
// demo 模式（PAAS_DISABLE_DEMO_SEED != true）由 cmd/core 调用；生产空目录由 admin 手动配。
// resolver 仅用于 catalog() 构造真实通道 Provider（impl 不入库，加载时 BuildProvider 重建）。
func SeedCatalog(ctx context.Context, repo Repository, resolver provider.CredentialResolver) error {
	for _, m := range catalog(resolver) {
		if err := repo.CreateModel(ctx, m); err != nil && !errors.Is(err, ErrModelExists) {
			return fmt.Errorf("seed 模型 %s 失败: %w", m.ID, err)
		}
		for _, c := range m.Channels {
			if err := repo.CreateChannel(ctx, m.ID, c); err != nil && !errors.Is(err, ErrChannelExists) {
				return fmt.Errorf("seed 通道 %s 失败: %w", c.ID, err)
			}
		}
	}
	return nil
}
