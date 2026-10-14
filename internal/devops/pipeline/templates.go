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
			Version:     3, // v3: 金丝雀提前到 prod deploy 前（经典序：canary 观察 -> promote 自带基线放量/terminate 零风险退出；v2 的 deploy->canary 序使 terminate 时基线已换新镜像，与「零风险退出」语义矛盾——状态机审计 I7）
			Description: "正式上线：审批 -> 金丝雀验证（并行观察，确认放量=基线滚动+打版本，或零风险终止）-> 打版本号 -> 合并主干",
			Stages: []StageDef{
				{Name: "上线审批", Type: StageApprove, Params: map[string]any{
					"message": "确认发布到生产环境",
				}},
				{Name: "金丝雀验证", Type: StageCanary, Params: map[string]any{
					"envId":       "{{app.env.prod}}", // 占位符：触发时解析 app 租户的 prod 环境 ID
					"imageSource": ImageLatestReady,   // CD 消费 CI 产物（app 最新 ready Image）——canary 提前到 deploy 前无前序输出可依
					// 并行验证式金丝雀（spec D3=A）：canary-<runID> 泳道 1 副本观察，验证地址见 stage 日志/前端面板；
					// 「确认放量」= 基线滚动到同一镜像后删 canary；「终止」= 仅删 canary 零风险退出（基线从未被动过）。
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
		// AI 研发一条龙（agentrun 执行域接入，spec 2026-10-11；2026-10-13 v2 全链）：
		// 需求→AI编码→AI审查→构建→测试泳道→冒烟→上线审批→金丝雀→发布→合主干。
		// 编排权归 paas：多个 agent stage 在 paas 侧串联（审查经 branchSource=priorAgent
		// 在 AI 产出分支上工作，非 agentrun AgentPipeline 嵌套）。
		// 段裁剪（agent-ship/run 端点）：AI 开发恒留，review/ship/release 三段按需勾选；
		// 勾选段映射 agentShipSegments（改名需同步）。lane 固定 agent-ship（单实例串行下
		// 复用=「AI 交付验证泳道」语义，不随 run 变）。v1 的「合入审批」去掉：测试部署
		// 无需卡点，release 段自带上线审批保命（门禁语义不降级）。
		{
			ID: "tpl-agent-ship", Name: "AI 研发交付流水线", Kind: KindAgent, Builtin: true, Version: 2,
			Description: "需求→AI编码→AI审查→构建→测试泳道部署→冒烟→上线审批→金丝雀→发布→合主干（一段龙按需勾选，产出分支自动注册 Change）",
			Stages: []StageDef{
				{Name: "AI 开发", Type: StageAgent, Params: map[string]any{
					"request": "在此填写需求", "runtime": "codex", "registerChange": "true", "deadlineSeconds": 1800,
				}},
				{Name: "AI 代码审查", Type: StageAgent, Params: map[string]any{
					"stage": "review", "runtime": "codex", "branchSource": "priorAgent", "deadlineSeconds": 900,
					"request": "审查上一阶段 AI 产出的变更：正确性、风险、测试覆盖，给出结论与建议",
				}},
				{Name: "构建", Type: StageBuild, Params: map[string]any{"branchSource": "priorAgent"}},
				{Name: "部署到测试泳道", Type: StageDeploy, Params: map[string]any{
					"envId": "{{app.env.test}}", "lane": "agent-ship", "imageSource": ImagePriorBuild, "strategy": "rolling",
				}},
				{Name: "冒烟联调", Type: StageTest, Params: map[string]any{"mode": TestSmoke, "path": "/healthz"}},
				{Name: "上线审批", Type: StageApprove, Params: map[string]any{"message": "确认 AI 产出发布上线（参考 AI 审查结论与测试泳道验证结果）"}},
				{Name: "金丝雀验证", Type: StageCanary, Params: map[string]any{
					"envId": "{{app.env.prod}}", "imageSource": ImageLatestReady,
				}},
				{Name: "发布版本", Type: StageRelease, Params: map[string]any{
					"versionStrategy": "auto-increment", // git tag + Image.version，不部署
				}},
				{Name: "合并主干", Type: StageBaseline, Params: map[string]any{
					"mainBranch": "main", "mergeFrom": "priorAgent", "mergeMode": "squash",
				}},
			},
		},
		{
			ID: "tpl-agent-review", Name: "AI 代码审查", Kind: KindAgent, Builtin: true, Version: 1,
			Description: "对当前分支发起 AI 审查（产出结论与建议，不部署）",
			Stages: []StageDef{
				{Name: "AI 审查", Type: StageAgent, Params: map[string]any{
					"stage": "review", "runtime": "codex", "request": "审查当前分支变更", "deadlineSeconds": 900,
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
