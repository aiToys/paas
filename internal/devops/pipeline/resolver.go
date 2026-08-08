// resolver.go 流水线模板参数解析器。
//
// 模板 stages 的 param 值可含占位符，触发 run 时由 ResolveStages 解析为实际值：
//   - {{app.env.test}} -> app 租户内 type=test 的环境 ID（多个取 promoteOrder 最小）
//   - {{app.env.prod}} -> type=prod 的环境 ID
//   - {{app.repo}}     -> app 绑定的 internal CodeRepo ID（build stage 用）
//
// Pipeline.ParamOverrides 覆盖模板默认（key 格式 "<stageIdx>.<paramKey>"，扁平）。
// 设计：模板定义 + 实例引用 + 参数化（参考 Argo WorkflowTemplate / Tekton Pipeline），
// 应用零操作即有可用流水线（占位符自动解析），需定制才覆盖参数。
package pipeline

import (
	"context"
	"fmt"
	"strings"
)

// ParamResolver 解析占位符所需的 app 上下文查询（依赖倒置，cmd/core 桥接 environment/codeRepo）。
type ParamResolver interface {
	// EnvByType 返回 app 租户内指定 type 的环境 ID（多个取 promoteOrder 最小；无返 ""）。
	EnvByType(ctx context.Context, appID, envType string) (string, error)
	// InternalRepoID 返回 app 绑定的 internal CodeRepo ID（build stage 用）。
	InternalRepoID(ctx context.Context, appID string) (string, error)
}

// ResolveStages 解析模板 stages：占位符替换 + ParamOverrides 覆盖。
// tplStages 来自模板定义；overrides 来自 Pipeline.ParamOverrides；appID 用于占位符解析。
// 返回 resolved stages（实例化快照，供 PipelineRun.StageRuns.Input 填充）。
func ResolveStages(ctx context.Context, tplStages []StageDef, overrides map[string]any, resolver ParamResolver, appID string) ([]StageDef, error) {
	if resolver == nil {
		// 无 resolver（测试/降级）：占位符原样返回，仅应用 overrides。
		return applyOverridesToStages(tplStages, overrides), nil
	}
	out := make([]StageDef, len(tplStages))
	for i, s := range tplStages {
		params := make(map[string]any, len(s.Params))
		for k, v := range s.Params {
			rv, err := resolveValue(ctx, v, resolver, appID)
			if err != nil {
				return nil, fmt.Errorf("stage %d (%s) param %s: %w", i, s.Name, k, err)
			}
			params[k] = rv
		}
		applyOverrides(params, overrides, i)
		out[i] = StageDef{Name: s.Name, Type: s.Type, Params: params}
	}
	return out, nil
}

// resolveValue 解析单个 param 值：字符串占位符查 resolver，其他原样返回。
func resolveValue(ctx context.Context, v any, resolver ParamResolver, appID string) (any, error) {
	s, ok := v.(string)
	if !ok {
		return v, nil
	}
	switch s {
	case "{{app.env.test}}":
		return resolver.EnvByType(ctx, appID, "test")
	case "{{app.env.prod}}":
		return resolver.EnvByType(ctx, appID, "prod")
	case "{{app.repo}}":
		return resolver.InternalRepoID(ctx, appID)
	default:
		return s, nil
	}
}

// applyOverrides 应用 ParamOverrides 到 stage params（key 格式 "<stageIdx>.<paramKey>"）。
func applyOverrides(params map[string]any, overrides map[string]any, stageIdx int) {
	idxPrefix := fmt.Sprintf("%d.", stageIdx)
	for k, v := range overrides {
		if strings.HasPrefix(k, idxPrefix) {
			params[strings.TrimPrefix(k, idxPrefix)] = v
		}
	}
}

// applyOverridesToStages 测试/降级场景（无 resolver）：仅应用 overrides，占位符原样保留。
func applyOverridesToStages(tplStages []StageDef, overrides map[string]any) []StageDef {
	out := make([]StageDef, len(tplStages))
	for i, s := range tplStages {
		params := make(map[string]any, len(s.Params))
		for k, v := range s.Params {
			params[k] = v
		}
		applyOverrides(params, overrides, i)
		out[i] = StageDef{Name: s.Name, Type: s.Type, Params: params}
	}
	return out
}
