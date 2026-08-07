package main

import (
	"context"

	"github.com/aitoys/paas/internal/appconfig"
	"github.com/aitoys/paas/internal/controller"
	"github.com/aitoys/paas/pkg/tenant"
)

// appConfigLookup 桥接 appconfig.Repository → controller.AppConfigLookup（依赖倒置）。
// 聚合工作负载 EnvID + DefaultEnv（跨环境桶）两路配置，环境级优先、DefaultEnv 兜底，
// 让"绑定资源"（数据服务连接 + 模型 LLM 凭证）真正生效到 Pod env。
type appConfigLookup struct {
	repo appconfig.Repository
}

// Items 返回工作负载应注入的 env 配置项（明文）。
// 用 ListPlain（非 List）：List 返掩码致注入 Pod 的是 •••••• 而非真实密码/Key，
// 工作负载拿到掩码无法连接数据服务/调 API（曾致绑定注入形同虚设）。ListPlain 仅供
// reconciler 内部注入，不暴露 API。reconciler ctx 无 PaaS tenant，用 CRD spec 的 TenantID 注入。
func (a appConfigLookup) Items(ctx context.Context, tenantID, appID, envID string) ([]controller.AppConfigItem, error) {
	if a.repo == nil || appID == "" {
		return nil, nil
	}
	ctx = tenant.WithTenant(ctx, tenantID)
	// 环境级 + DefaultEnv（跨环境桶，模型 LLM Key 注入于此）；同 key 环境级覆盖 DefaultEnv。
	merged := map[string]string{}
	order := make([]string, 0)
	for _, env := range []string{appconfig.DefaultEnv, envID} {
		if env == "" {
			continue
		}
		items, err := a.repo.ListPlain(ctx, appID, env)
		if err != nil {
			continue
		}
		for _, it := range items {
			if _, ok := merged[it.Key]; !ok {
				order = append(order, it.Key)
			}
			merged[it.Key] = it.Value
		}
	}
	out := make([]controller.AppConfigItem, 0, len(order))
	for _, k := range order {
		out = append(out, controller.AppConfigItem{Name: k, Value: merged[k]})
	}
	return out, nil
}
