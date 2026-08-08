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
// ci 模板（开发流水线）：git push 触发 -> 构建 -> 部署 dev -> 冒烟测试 -> 写基线（合并主干）。
// cd 模板（发布流水线）：手动触发 -> 审批 -> 部署 prod -> 写版本（不自动合并主干）。
//
// deploy.envId 留空，用户从模板创建 Pipeline 时填本租户环境 ID。
// cd 的 baseline.mainBranch="" 表示 prod 发布只打版本不自动 merge（与 ci 的"合并主干"区分）。
func BuiltinTemplates() []PipelineTemplate {
	return []PipelineTemplate{
		{
			ID:          "tpl-ci",
			Name:        "开发流水线",
			Kind:        KindCI,
			Builtin:     true,
			Description: "git push 触发：构建 -> 部署 dev -> 冒烟测试 -> 合并主干",
			Stages: []StageDef{
				{Name: "构建", Type: StageBuild},
				{Name: "部署到开发环境", Type: StageDeploy, Params: map[string]any{
					"envId":       "", // 用户创建时填本租户 dev 环境 ID
					"imageSource": ImagePriorBuild,
					"strategy":    "rolling",
				}},
				{Name: "冒烟测试", Type: StageTest, Params: map[string]any{
					"mode": TestSmoke,
					"path": "/livez",
				}},
				{Name: "写基线", Type: StageBaseline, Params: map[string]any{
					"mainBranch":       "main",
					"versionStrategy":  "auto-increment",
					"mergeMode":        "squash",
				}},
			},
		},
		{
			ID:          "tpl-cd",
			Name:        "发布流水线",
			Kind:        KindCD,
			Builtin:     true,
			Description: "手动触发：审批 -> 部署 prod -> 写版本",
			Stages: []StageDef{
				{Name: "上线审批", Type: StageApprove, Params: map[string]any{
					"message": "确认发布到生产环境",
				}},
				{Name: "部署到生产", Type: StageDeploy, Params: map[string]any{
					"envId":       "", // 用户创建时填本租户 prod 环境 ID
					"imageSource": ImageLatestReady,
					"strategy":    "rolling",
				}},
				{Name: "写版本", Type: StageBaseline, Params: map[string]any{
					"mainBranch":      "", // cd 不自动合并主干（prod 发布只打版本）
					"versionStrategy": "auto-increment",
					"mergeMode":       "ff",
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
