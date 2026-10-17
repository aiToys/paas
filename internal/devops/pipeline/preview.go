// Package pipeline —— 执行计划预演（spec 2026-10-17 §6/§7：preview + planHash）。
//
// preview 只读计算一次运行「将要发生什么」：模板/覆盖/run 级参数的最终解析结果、
// 触发时将锁定的候选产物、缺配置项。planHash 与 trigger 共用算法——前端把预览返回的
// hash 原样带回 trigger，服务端锁定产物后重算比对，防「预览到执行之间目标漂移」。
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// StagePreview 单阶段预览项。
type StagePreview struct {
	Index    int            `json:"index"`
	Type     string         `json:"type"`
	Name     string         `json:"name"`
	Params   map[string]any `json:"params,omitempty"`
	Resolved string         `json:"resolved"` // 中文一句话（实际目标，占位符已解析）
}

// RunPreview 执行计划预演结果。
type RunPreview struct {
	PipelineID      string           `json:"pipelineId"`
	TemplateID      string           `json:"templateId"`
	TemplateVersion int              `json:"templateVersion"`
	Branch          string           `json:"branch"`
	Stages          []StagePreview   `json:"stages"`
	Artifact        *ArtifactBinding `json:"artifact,omitempty"` // 触发时将锁定的候选产物（latestReady 预解析）
	Missing         []string         `json:"missing,omitempty"`  // 缺配置项（可触发但需知晓）
	PlanHash        string           `json:"planHash"`
}

// ImageMeta 镜像元数据摘要（产物锁定时取 digest/commit/buildRunId）。
type ImageMeta struct {
	ID         string
	Digest     string
	Commit     string
	BuildRunID string
}

// ImageMetaResolver 按镜像 ID 取元数据（ctx 带租户；cmd/core 桥接 devops Image+BuildRun；
// nil/未命中仅锁 imageId，ArtifactBinding.Commit 退 run 触发 commit）。
type ImageMetaResolver func(ctx context.Context, appID, imageID string) (ImageMeta, bool)

// PlanHash 执行计划指纹：模板身份 + 分支 + 规范化 stages。
// encoding/json 对 map key 排序序列化，天然稳定；lockedImageId 计入 hash——
// 预览到执行之间若有新构建完成改变候选镜像，hash 变化 → trigger 409 重新预览。
func PlanHash(templateID string, templateVersion int, branch string, stages []StageDef) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s|%d|%s|", templateID, templateVersion, branch)
	if err := json.NewEncoder(h).Encode(stages); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// BuildPreview 由锁定产物后的 resolved stages 构造预览（trigger 与 preview 共用）。
func BuildPreview(p Pipeline, tpl PipelineTemplate, resolved []StageDef, branch string, artifact *ArtifactBinding, missing []string) RunPreview {
	stages := make([]StagePreview, len(resolved))
	for i, s := range resolved {
		stages[i] = StagePreview{Index: i, Type: s.Type, Name: s.Name, Params: s.Params, Resolved: describeStage(s)}
	}
	return RunPreview{
		PipelineID: p.ID, TemplateID: tpl.ID, TemplateVersion: tpl.Version,
		Branch: branch, Stages: stages, Artifact: artifact, Missing: missing,
		PlanHash: PlanHash(tpl.ID, tpl.Version, branch, resolved),
	}
}

// describeStage 生成阶段中文说明（实际目标；params 已是最终解析值）。
func describeStage(s StageDef) string {
	env := strOr(s.Params, "envId", "")
	lane := strOr(s.Params, "lane", "")
	target := ""
	if env != "" {
		target = "环境 " + env
		if lane != "" {
			target += " · 泳道 " + lane
		}
	}
	img := describeImage(s)
	switch s.Type {
	case StageBuild:
		if svc := strOr(s.Params, "service", ""); svc != "" {
			return fmt.Sprintf("构建 %s 分支镜像%s", strOr(s.Params, "branchSource", "当前"), "（服务 "+svc+"）")
		}
		return "构建分支镜像"
	case StageDeploy:
		return "部署" + img + " → " + target
	case StageTest:
		if strOr(s.Params, "mode", TestSmoke) == TestManual {
			return "人工确认测试结果（暂停等待）"
		}
		return "HTTP 探活 " + strOr(s.Params, "path", "/livez") + "（目标为前序部署地址）"
	case StageApprove:
		return strOr(s.Params, "message", "等待审批") + "（暂停等待）"
	case StageCanary:
		return "金丝雀并行验证" + img + " → " + target + "（暂停等待放量/终止决策）"
	case StageRelease:
		return "打版本里程碑" + img
	case StagePromote:
		return "晋升部署到下一阶环境" + img
	case StageBaseline:
		return "合并分支到主干"
	case StageAgent:
		return "AI 研发：需求 → 代码分支（产出分支衔接后续构建）"
	}
	return s.Name
}

// describeImage 镜像来源中文描述。
func describeImage(s StageDef) string {
	if id := strOr(s.Params, "lockedImageId", ""); id != "" {
		return "锁定镜像 " + id
	}
	switch strOr(s.Params, "imageSource", ImagePriorBuild) {
	case ImageSelected:
		return "指定镜像 " + strOr(s.Params, "imageId", "?")
	case ImageLatestReady:
		return "最新就绪镜像（触发时锁定）"
	default:
		return "（本流水线前序构建产物）"
	}
}

// missingConfigItems 扫描 resolved stages 的缺配置项（可触发但影响预期的提示级）。
func missingConfigItems(stages []StageDef) []string {
	var missing []string
	for _, s := range stages {
		if (s.Type == StageDeploy || s.Type == StageCanary) && strOr(s.Params, "port", "") == "" {
			if name := s.Name; name != "" {
				missing = append(missing, "阶段「"+name+"」未配置端口（不建 Service，探活降级走集群内地址）")
			}
		}
	}
	return missing
}

// deployLaneOf 从最终解析 stages 提取部署泳道（run.Lane 真源，触发时固化）：
// 首个 deploy 段的非 default lane；全 default/无 deploy 返 ""。canary 段泳道运行时
// 派生（canary-<runID>）不入此字段——关闭保护关心的是用户显式指定的联调泳道。
func deployLaneOf(stages []StageDef) string {
	for _, s := range stages {
		if s.Type != StageDeploy {
			continue
		}
		if lane := strOr(s.Params, "lane", ""); lane != "" && lane != LaneDefault {
			return lane
		}
	}
	return ""
}
