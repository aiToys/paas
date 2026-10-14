package pipeline

import (
	"context"
	"strings"
	"testing"

	"github.com/aitoys/paas/pkg/tenant"
)

// TestSeedTemplatesIdempotent 平台级 seed 灌入 2 个预置模板，二次 seed 不翻倍。
// seed 用无租户 ctx（平台级，CreateTemplate 对 tenant_id="" 特判放行）；
// 验证用租户视角 ctx（ListTemplates 强制租户，平台预置对租户可见）。
func TestSeedTemplatesIdempotent(t *testing.T) {
	s := NewMemoryStore()
	seedCtx := context.Background() // 平台级 seed

	if err := SeedTemplates(seedCtx, s); err != nil {
		t.Fatalf("首次 seed 失败: %v", err)
	}
	viewCtx := tenant.WithTenant(context.Background(), "t-acme")
	first, err := s.ListTemplates(viewCtx)
	if err != nil {
		t.Fatalf("ListTemplates 失败: %v", err)
	}
	if len(first) != 4 {
		t.Fatalf("首次 want 4 模板，got %d", len(first))
	}

	// 幂等：再 seed 不报错不翻倍
	if err := SeedTemplates(seedCtx, s); err != nil {
		t.Fatalf("二次 seed 失败: %v", err)
	}
	second, _ := s.ListTemplates(viewCtx)
	if len(second) != 4 {
		t.Fatalf("幂等 want 4，got %d", len(second))
	}
}

// TestSeedTemplatesUpgradesBuiltinOnVersionBump 验证 builtin 模板版本升级覆盖：
// 模拟已部署旧版本 builtin（Version=0 + 旧 stages），seed 后代码 Version=1 > DB Version=0，
// 应覆盖 stages/name/description/version。解决 dogfooding 痛点（改代码后 PG 旧记录不更新）。
func TestSeedTemplatesUpgradesBuiltinOnVersionBump(t *testing.T) {
	s := NewMemoryStore()
	seedCtx := context.Background() // 平台级 seed

	// 模拟已部署旧版本 builtin 模板（Version=0，stages 与代码不同）
	oldTpl := PipelineTemplate{
		ID:          "tpl-ci",
		Name:        "旧CI模板名",
		Kind:        KindCI,
		Builtin:     true,
		Version:     0,
		Description: "旧描述",
		Stages:      []StageDef{{Name: "旧阶段", Type: StageBuild}},
	}
	if _, err := s.CreateTemplate(seedCtx, oldTpl); err != nil {
		t.Fatalf("注入旧 builtin 模板失败: %v", err)
	}

	// seed：代码 Version（当前 v2）> DB Version=0，应触发覆盖
	if err := SeedTemplates(seedCtx, s); err != nil {
		t.Fatalf("seed 失败: %v", err)
	}

	got, err := s.GetTemplate(seedCtx, "tpl-ci")
	if err != nil {
		t.Fatalf("GetTemplate 失败: %v", err)
	}
	if got.Version != 2 {
		t.Errorf("Version 升级 want 2，got %d", got.Version)
	}
	// v2: smoke 默认路径 /livez -> /healthz
	smoke := got.Stages[2]
	if smoke.Params["path"] != "/healthz" {
		t.Errorf("smoke path want /healthz，got %v", smoke.Params["path"])
	}
	if got.Name != "测试联调流水线" {
		t.Errorf("Name 覆盖 want 测试联调流水线，got %s", got.Name)
	}
	if got.Description != "变更验证：构建 -> 部署到测试环境的分支泳道 -> 冒烟联调（不打版本、不合并主干）" {
		t.Errorf("Description 覆盖失败，got %s", got.Description)
	}
	if len(got.Stages) != 3 {
		t.Errorf("Stages 覆盖 want 3 阶段，got %d", len(got.Stages))
	}
}

// TestSeedTemplatesSameVersionNoOverwrite 验证同 Version 不覆盖：
// 已部署 builtin Version=1（与代码一致），seed 后保持用户/旧值不被误覆盖（幂等）。
func TestSeedTemplatesSameVersionNoOverwrite(t *testing.T) {
	s := NewMemoryStore()
	seedCtx := context.Background()

	// 先正常 seed 灌入代码版本（Version=1）
	if err := SeedTemplates(seedCtx, s); err != nil {
		t.Fatalf("首次 seed 失败: %v", err)
	}
	orig, _ := s.GetTemplate(seedCtx, "tpl-ci")

	// 二次 seed
	if err := SeedTemplates(seedCtx, s); err != nil {
		t.Fatalf("二次 seed 失败: %v", err)
	}
	got, _ := s.GetTemplate(seedCtx, "tpl-ci")

	if got.Version != orig.Version {
		t.Errorf("同 Version 不应改变，orig=%d got=%d", orig.Version, got.Version)
	}
	if len(got.Stages) != len(orig.Stages) {
		t.Errorf("同 Version stages 不应变，orig=%d got=%d", len(orig.Stages), len(got.Stages))
	}
}

// TestBuiltinTemplatesContent 验证预置模板的 stages 类型序列与 Kind。
func TestBuiltinTemplatesContent(t *testing.T) {
	tpls := BuiltinTemplates()
	if len(tpls) != 4 {
		t.Fatalf("want 4 预置模板，got %d", len(tpls))
	}

	ci := tpls[0]
	if ci.ID != "tpl-ci" || ci.Kind != KindCI || !ci.Builtin {
		t.Fatalf("ci 模板元数据错: %+v", ci)
	}
	if len(ci.Stages) != 3 {
		t.Fatalf("ci want 3 stages（build/deploy/test），got %d", len(ci.Stages))
	}
	wantCITypes := []string{StageBuild, StageDeploy, StageTest}
	for i, s := range ci.Stages {
		if s.Type != wantCITypes[i] {
			t.Fatalf("ci stage %d want %s，got %s", i, wantCITypes[i], s.Type)
		}
	}

	cd := tpls[1]
	if cd.ID != "tpl-cd" || cd.Kind != KindCD || !cd.Builtin {
		t.Fatalf("cd 模板元数据错: %+v", cd)
	}
	if len(cd.Stages) != 4 {
		t.Fatalf("cd want 4 stages（approve/canary/release/baseline），got %d", len(cd.Stages))
	}
	wantCDTypes := []string{StageApprove, StageCanary, StageRelease, StageBaseline}
	for i, s := range cd.Stages {
		if s.Type != wantCDTypes[i] {
			t.Fatalf("cd stage %d want %s，got %s", i, wantCDTypes[i], s.Type)
		}
	}
	// cd canary 用 latestReady（CD 消费 CI 产物；v3 起 canary 在 deploy 前无前序输出），
	// ci deploy 用 priorBuild（CI 自产自销）
	if cd.Stages[1].Params["imageSource"] != ImageLatestReady {
		t.Fatalf("cd canary imageSource 期望 %s，got %v", ImageLatestReady, cd.Stages[1].Params["imageSource"])
	}
	if ci.Stages[1].Params["imageSource"] != ImagePriorBuild {
		t.Fatalf("ci deploy imageSource 期望 %s，got %v", ImagePriorBuild, ci.Stages[1].Params["imageSource"])
	}
	// cd baseline mainBranch="main"（上线后合并主干），ci 无 baseline（CI 不动主干）
	if cd.Stages[3].Params["mainBranch"] != "main" {
		t.Fatalf("cd baseline mainBranch 期望 main，got %v", cd.Stages[3].Params["mainBranch"])
	}
}

// findTpl 在模板切片中按 ID 查找（测试 helper）。
func findTpl(tpls []PipelineTemplate, id string) PipelineTemplate {
	for _, t := range tpls {
		if t.ID == id {
			return t
		}
	}
	return PipelineTemplate{}
}

// findStage 在模板中按 StageType 查找首个匹配 stage（测试 helper）。
func findStage(tpl PipelineTemplate, stageType string) StageDef {
	for _, s := range tpl.Stages {
		if s.Type == stageType {
			return s
		}
	}
	return StageDef{}
}

// hasStage 判断模板是否含指定 StageType（测试 helper）。
func hasStage(tpl PipelineTemplate, stageType string) bool {
	for _, s := range tpl.Stages {
		if s.Type == stageType {
			return true
		}
	}
	return false
}

// TestBuiltinTemplatesSemantics 验证 ci/cd 在 release/lane 维度的语义区分：
//   - ci 不含 release（测试不打版本）；ci 的 deploy 用 lane={{run.branch}}（分支泳道联调）
//   - cd 含 release（上线打版本）；v3 起金丝雀提前（canary 在 deploy 前，promote 自带
//     基线放量），故 cd 无独立 deploy stage，canary 在 release 之前
func TestBuiltinTemplatesSemantics(t *testing.T) {
	tpls := BuiltinTemplates()
	ci := findTpl(tpls, "tpl-ci")
	cd := findTpl(tpls, "tpl-cd")
	// ci 不含 release stage
	if hasStage(ci, StageRelease) {
		t.Error("ci 模板不应含 release（测试不打版本）")
	}
	// ci 的 deploy 含 lane 占位符
	dep := findStage(ci, StageDeploy)
	if dep.Params["lane"] != "{{run.branch}}" {
		t.Errorf("ci deploy lane 应为 {{run.branch}}，得 %v", dep.Params["lane"])
	}
	// cd 含 release stage
	if !hasStage(cd, StageRelease) {
		t.Error("cd 模板应含 release（上线打版本）")
	}
	// v3：cd 无独立 prod deploy（promote 自带基线放量）；canary 在 release 之前
	if hasStage(cd, StageDeploy) {
		t.Error("cd v3 不应含独立 deploy stage（canary promote 自带基线放量）")
	}
	canary := findStage(cd, StageCanary)
	relIdx, canaryIdx := -1, -1
	for i, s := range cd.Stages {
		if s.Type == StageRelease {
			relIdx = i
		}
		if s.Type == StageCanary {
			canaryIdx = i
		}
	}
	if canaryIdx < 0 || relIdx < 0 || canaryIdx > relIdx {
		t.Errorf("cd canary(%d) 应在 release(%d) 之前", canaryIdx, relIdx)
	}
	_ = canary
}

// TestSeedTemplatesCrossTenantVisible 平台预置模板对所有租户可见。
func TestSeedTemplatesCrossTenantVisible(t *testing.T) {
	s := NewMemoryStore()
	if err := SeedTemplates(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	acme, _ := s.ListTemplates(tenant.WithTenant(context.Background(), "t-acme"))
	globex, _ := s.ListTemplates(tenant.WithTenant(context.Background(), "t-globex"))
	if len(acme) != 4 || len(globex) != 4 {
		t.Fatalf("平台预置应跨租户可见，acme=%d globex=%d", len(acme), len(globex))
	}
	// 都是平台预置（TenantID=""）
	if acme[0].TenantID != "" || globex[0].TenantID != "" {
		t.Fatalf("平台预置 TenantID 应空，acme=%q globex=%q", acme[0].TenantID, globex[0].TenantID)
	}
}

// TestBuiltinAgentTemplates AI 研发 builtin 模板（spec 2026-10-11 §4.4）：
// ship=agent→approve→build(priorAgent)→deploy(test 泳道)→smoke；review=单 agent(review) stage。
func TestBuiltinAgentTemplates(t *testing.T) {
	tpls := BuiltinTemplates()
	var ship, review *PipelineTemplate
	for i := range tpls {
		switch tpls[i].ID {
		case "tpl-agent-ship":
			ship = &tpls[i]
		case "tpl-agent-review":
			review = &tpls[i]
		}
	}
	if ship == nil || review == nil {
		t.Fatalf("缺 builtin agent 模板: ship=%v review=%v", ship, review)
	}
	if ship.Kind != KindAgent || review.Kind != KindAgent {
		t.Errorf("Kind 应为 agent: ship=%s review=%s", ship.Kind, review.Kind)
	}
	if ship.Stages[0].Type != StageAgent {
		t.Errorf("ship 首 stage 应为 agent, got %s", ship.Stages[0].Type)
	}
	// ship 关键参数：registerChange + build branchSource + deploy 占位符
	if ship.Stages[0].Params["registerChange"] != "true" {
		t.Errorf("ship agent stage 应 registerChange=true")
	}
	var build, deploy, smoke *StageDef
	for i := range ship.Stages {
		switch ship.Stages[i].Type {
		case StageBuild:
			build = &ship.Stages[i]
		case StageDeploy:
			deploy = &ship.Stages[i]
		case StageTest:
			smoke = &ship.Stages[i]
		}
	}
	if build == nil || build.Params["branchSource"] != "priorAgent" {
		t.Errorf("ship build stage 应 branchSource=priorAgent, got %+v", build)
	}
	if deploy == nil || deploy.Params["envId"] != "{{app.env.test}}" || deploy.Params["lane"] != "agent-ship" {
		t.Errorf("ship deploy stage 参数错误（v2: lane 固定 agent-ship）: %+v", deploy)
	}
	if smoke == nil || smoke.Params["mode"] != TestSmoke {
		t.Errorf("ship 应含 smoke stage: %+v", smoke)
	}
	// v2 全链（2026-10-13）：AI 审查在前序产出分支上工作；release 段四件套齐备
	var reviewStage, approve, canary, release, baseline *StageDef
	for i := range ship.Stages {
		st := &ship.Stages[i]
		switch st.Type {
		case StageAgent:
			if st.Params["stage"] == "review" {
				reviewStage = st
			}
		case StageApprove:
			approve = st
		case StageCanary:
			canary = st
		case StageRelease:
			release = st
		case StageBaseline:
			baseline = st
		}
	}
	if reviewStage == nil || reviewStage.Params["branchSource"] != "priorAgent" {
		t.Errorf("AI 审查 stage 应 branchSource=priorAgent（在 AI 产出分支上审查）: %+v", reviewStage)
	}
	if approve == nil || canary == nil || release == nil {
		t.Errorf("v2 release 段应含 上线审批/金丝雀/发布版本: approve=%v canary=%v release=%v", approve, canary, release)
	}
	if baseline == nil || baseline.Params["mergeFrom"] != "priorAgent" {
		t.Errorf("baseline 应 mergeFrom=priorAgent（AI 产物合主干）: %+v", baseline)
	}
	// review 模板：单 stage，stage=review
	if len(review.Stages) != 1 || review.Stages[0].Type != StageAgent || review.Stages[0].Params["stage"] != "review" {
		t.Errorf("review 模板应为单 agent(review) stage: %+v", review.Stages)
	}
}

// TestBuiltinAgentShipSegments 段裁剪映射锁（2026-10-13）：模板 stage 改名必须同步
// agentShipSegments，否则 agent-ship/run 端点裁剪漏段（stage 静默残留）。
func TestBuiltinAgentShipSegments(t *testing.T) {
	tpls := BuiltinTemplates()
	var ship *PipelineTemplate
	for i := range tpls {
		if tpls[i].ID == "tpl-agent-ship" {
			ship = &tpls[i]
		}
	}
	if ship == nil {
		t.Fatal("缺 tpl-agent-ship")
	}
	for i, st := range ship.Stages {
		if i == 0 {
			if _, ok := agentShipSegments[st.Name]; ok {
				t.Errorf("首 stage「AI 开发」不应在段映射（恒留）: %s", st.Name)
			}
			continue
		}
		if _, ok := agentShipSegments[st.Name]; !ok {
			t.Errorf("stage「%s」不在 agentShipSegments 段映射：改名后未同步，裁剪将漏段", st.Name)
		}
	}
}

// TestTrimAgentShipStages 裁剪语义：段关移除、AI 开发恒留、全勾=原序全量。
func TestTrimAgentShipStages(t *testing.T) {
	tpls := BuiltinTemplates()
	var stages []StageDef
	for _, tpl := range tpls {
		if tpl.ID == "tpl-agent-ship" {
			stages = tpl.Stages
		}
	}
	if len(stages) == 0 {
		t.Fatal("缺 tpl-agent-ship stages")
	}
	names := func(ss []StageDef) []string {
		out := make([]string, len(ss))
		for i, s := range ss {
			out[i] = s.Name
		}
		return out
	}
	// 全勾 = 原样
	if got := trimAgentShipStages(stages, true, true, true); len(got) != len(stages) {
		t.Errorf("全勾应保留全部 %d 段, got %d", len(stages), len(got))
	}
	// 仅 ship：AI 开发 + ship 三段（构建/部署/冒烟）
	got := names(trimAgentShipStages(stages, false, true, false))
	want := []string{"AI 开发", "构建", "部署到测试泳道", "冒烟联调"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("仅 ship 段裁剪错误: got %v want %v", got, want)
	}
	// 全不勾：只留 AI 开发
	got = names(trimAgentShipStages(stages, false, false, false))
	if len(got) != 1 || got[0] != "AI 开发" {
		t.Errorf("全不勾应只留 AI 开发, got %v", got)
	}
}
