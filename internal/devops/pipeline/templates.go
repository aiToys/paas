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
			Version:     2, // v2: smoke 默认路径 /livez -> /healthz（K8s 业界惯例 + dogfood app 实际值）
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
					"path": "/healthz", // K8s 业界惯例（kubelet probe 通用）；应用可经 paramOverrides 覆盖
				}},
			},
		},
		{
			ID:          "tpl-cd",
			Name:        "上线发布流水线",
			Kind:        KindCD,
			Builtin:     true,
			Version:     1, // 破坏性改动 +1（存量经 migration 0025 回填为 1）
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

// SeedTemplates 幂等灌入平台预置模板，并按 Version 升级已部署的 builtin 模板。
//
// 升级语义：已存在同 ID builtin 模板时，若代码 Version > DB Version，覆盖 stages/name/description/version
// （绕过 builtin 拒改保护，平台级发版升级路径）。同 Version 或 DB 更高（用户手动？不可能，builtin 拒改）
// 不覆盖。这解决了「改 BuiltinTemplates() 代码后已部署 PG 仍是旧记录」的 dogfooding 痛点
// （此前每次改 builtin 要手写 migration UPDATE 补救，如 0020）。
//
// 不门控 demo seed（生产也需预置模板，与演示凭证门控独立）。
func SeedTemplates(ctx context.Context, repo TemplateRepository) error {
	for _, tpl := range BuiltinTemplates() {
		_, err := repo.CreateTemplate(ctx, tpl)
		if err == nil {
			continue // 新建成功
		}
		if !errors.Is(err, ErrTemplateExists) {
			return err // 其他错误返出
		}
		// 已存在：比对 Version，代码更高则覆盖升级
		current, getErr := repo.GetTemplate(ctx, tpl.ID)
		if getErr != nil {
			// 平台预置模板 tenant_id=NULL 跨租户可见，GetTemplate 不会因 tenant 拒；
			// 真不存在的极端情况（并发删）跳过本次，下次启动再升。
			continue
		}
		if tpl.Version > current.Version {
			if replaceErr := repo.ReplaceBuiltinTemplate(ctx, tpl); replaceErr != nil {
				return replaceErr
			}
		}
	}
	return nil
}
