package pipeline

import (
	"context"
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
	if len(first) != 2 {
		t.Fatalf("首次 want 2 模板，got %d", len(first))
	}

	// 幂等：再 seed 不报错不翻倍
	if err := SeedTemplates(seedCtx, s); err != nil {
		t.Fatalf("二次 seed 失败: %v", err)
	}
	second, _ := s.ListTemplates(viewCtx)
	if len(second) != 2 {
		t.Fatalf("幂等 want 2，got %d", len(second))
	}
}

// TestBuiltinTemplatesContent 验证预置模板的 stages 类型序列与 Kind。
func TestBuiltinTemplatesContent(t *testing.T) {
	tpls := BuiltinTemplates()
	if len(tpls) != 2 {
		t.Fatalf("want 2 预置模板，got %d", len(tpls))
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
		t.Fatalf("cd want 4 stages（approve/deploy/release/baseline），got %d", len(cd.Stages))
	}
	wantCDTypes := []string{StageApprove, StageDeploy, StageRelease, StageBaseline}
	for i, s := range cd.Stages {
		if s.Type != wantCDTypes[i] {
			t.Fatalf("cd stage %d want %s，got %s", i, wantCDTypes[i], s.Type)
		}
	}
	// cd deploy 用 latestReady（CD 消费 CI 产物），ci deploy 用 priorBuild（CI 自产自销）
	if cd.Stages[1].Params["imageSource"] != ImageLatestReady {
		t.Fatalf("cd deploy imageSource 期望 %s，got %v", ImageLatestReady, cd.Stages[1].Params["imageSource"])
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
//   - cd 含 release（上线打版本）；cd 的 deploy 用 lane=default（生产基线）
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
	// cd 的 deploy lane=default
	cdDep := findStage(cd, StageDeploy)
	if cdDep.Params["lane"] != LaneDefault {
		t.Errorf("cd deploy lane 应为 default，得 %v", cdDep.Params["lane"])
	}
}

// TestSeedTemplatesCrossTenantVisible 平台预置模板对所有租户可见。
func TestSeedTemplatesCrossTenantVisible(t *testing.T) {
	s := NewMemoryStore()
	if err := SeedTemplates(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	acme, _ := s.ListTemplates(tenant.WithTenant(context.Background(), "t-acme"))
	globex, _ := s.ListTemplates(tenant.WithTenant(context.Background(), "t-globex"))
	if len(acme) != 2 || len(globex) != 2 {
		t.Fatalf("平台预置应跨租户可见，acme=%d globex=%d", len(acme), len(globex))
	}
	// 都是平台预置（TenantID=""）
	if acme[0].TenantID != "" || globex[0].TenantID != "" {
		t.Fatalf("平台预置 TenantID 应空，acme=%q globex=%q", acme[0].TenantID, globex[0].TenantID)
	}
}
