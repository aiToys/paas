// templates.go 平台预置流水线模板（ci/cd）+ 幂等 seed。
//
// 模板 tenant_id=""（平台预置，全租户共享），builtin=true（不可删，UI 标记）。
// cmd/core 启动时 SeedTemplates 灌入（不门控 demo seed，生产也需预置模板）。
// CreateTemplate 对 tenant_id="" 不要求 ctx 租户（admin 平台级 seed）。
package pipeline

import (
	"context"
	"errors"
)

// BuiltinTemplates 平台预置流水线模板（固定 ID，幂等 seed 不重建）。
//
// 语义对齐 plan 2026-08-09（deploy release lane 重构）：
//   - ci 模板（测试联调流水线）：变更验证用，构建 -> 部署到测试环境的分支泳道（lane=run.branch）
//     -> 冒烟联调。不打版本、不合并主干（CI 只验证，不上线）。
//   - cd 模板（上线发布流水线）：正式上线用，审批 -> 部署到生产基线（lane=default）
//     -> 打版本号（release stage）-> 合并主干。
//
// deploy.envId 用占位符 {{app.env.test}}/{{app.env.prod}}，触发时自动解析 app 租户环境（零操作）。
// release stage 独立承担"打版本号"（git tag + Image.version），与 baseline（合并主干）解耦；
// cd baseline.mainBranch="main" 表示上线后合并主干，ci 无 baseline（CI 不动主干）。
func BuiltinTemplates() []PipelineTemplate {
	return []PipelineTemplate{
		{
			ID:          "tpl-ci",
			Name:        "测试联调流水线",
			Kind:        KindCI,
			Builtin:     true,
			Description: "变更验证：构建 -> 部署到测试环境的分支泳道 -> 冒烟联调（不打版本、不合并主干）",
			Stages: []StageDef{
				{Name: "构建", Type: StageBuild},
				{Name: "部署到测试泳道", Type: StageDeploy, Params: map[string]any{
					"envId":       "{{app.env.test}}", // 占位符：触发时解析 app 租户的 test 环境 ID
					"lane":        "{{run.branch}}",   // 占位符：触发时取运行分支名作泳道（联调隔离）
					"imageSource": ImagePriorBuild,
					"strategy":    "rolling",
				}},
				{Name: "冒烟联调", Type: StageTest, Params: map[string]any{
					"mode": TestSmoke,
					"path": "/livez",
				}},
			},
		},
		{
			ID:          "tpl-cd",
			Name:        "上线发布流水线",
			Kind:        KindCD,
			Builtin:     true,
			Description: "正式上线：部署到生产基线 -> 打版本号 -> 合并主干",
			Stages: []StageDef{
				{Name: "上线审批", Type: StageApprove, Params: map[string]any{
					"message": "确认发布到生产环境",
				}},
				{Name: "部署到生产", Type: StageDeploy, Params: map[string]any{
					"envId":       "{{app.env.prod}}", // 占位符：触发时解析 app 租户的 prod 环境 ID
					"lane":        LaneDefault,        // 生产基线（无泳道）
					"imageSource": ImageLatestReady,   // CD 消费 CI 产物（app 最新 ready Image）
					"strategy":    "rolling",
				}},
				{Name: "发布版本", Type: StageRelease, Params: map[string]any{
					"versionStrategy": "auto-increment", // git tag + Image.version，不部署
				}},
				{Name: "合并主干", Type: StageBaseline, Params: map[string]any{
					"mainBranch": "main",
					"mergeMode":  "squash",
				}},
			},
		},
	}
}

// SeedTemplates 幂等灌入平台预置模板。
// 已存在（同 ID 或同 (tenant,name)）-> ErrTemplateExists 跳过；其他错误返出。
// 不门控 demo seed（生产也需预置模板，与演示凭证门控独立）。
func SeedTemplates(ctx context.Context, repo TemplateRepository) error {
	for _, tpl := range BuiltinTemplates() {
		if _, err := repo.CreateTemplate(ctx, tpl); err != nil {
			if errors.Is(err, ErrTemplateExists) {
				continue
			}
			return err
		}
	}
	return nil
}
