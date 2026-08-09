package pipeline

import (
	"context"
	"testing"
)

// ---------- 测试 stub ----------

// stubEnvResolver 仅返回固定 envID，便于断言占位符替换。
type stubEnvResolver struct{ envID string }

func (s stubEnvResolver) EnvByType(_ context.Context, _, _ string) (string, error) {
	return s.envID, nil
}
func (s stubEnvResolver) InternalRepoID(_ context.Context, _ string) (string, error) {
	return "repo-stub", nil
}

// ---------- 测试 ----------

func TestResolveStagesRunBranch(t *testing.T) {
	ctx := context.Background()
	stages := []StageDef{
		{Type: StageDeploy, Params: map[string]any{"lane": "{{run.branch}}"}},
	}
	resolved, err := ResolveStages(ctx, stages, nil, stubEnvResolver{envID: "env-1"}, "app-1", "feature-x")
	if err != nil {
		t.Fatalf("ResolveStages: %v", err)
	}
	if got := resolved[0].Params["lane"]; got != "feature-x" {
		t.Errorf("{{run.branch}} 应解析为 feature-x，得 %v", got)
	}
}

func TestResolveStagesRunBranchEmpty(t *testing.T) {
	ctx := context.Background()
	stages := []StageDef{
		{Type: StageDeploy, Params: map[string]any{"lane": "{{run.branch}}"}},
	}
	resolved, err := ResolveStages(ctx, stages, nil, stubEnvResolver{envID: "env-1"}, "app-1", "")
	if err != nil {
		t.Fatalf("ResolveStages: %v", err)
	}
	// branch 为空时替换为空串（调用方负责传非空 branch）。
	if got := resolved[0].Params["lane"]; got != "" {
		t.Errorf("空 branch 应替换为空串，得 %v", got)
	}
}

func TestResolveStagesExistingPlaceholders(t *testing.T) {
	ctx := context.Background()
	stages := []StageDef{
		{
			Type: StageDeploy,
			Params: map[string]any{
				"envId":   "{{app.env.test}}",
				"repoId":  "{{app.repo}}",
				"lane":    "{{run.branch}}",
				"literal": "plain-value",
			},
		},
	}
	resolved, err := ResolveStages(ctx, stages, nil, stubEnvResolver{envID: "env-test"}, "app-1", "main")
	if err != nil {
		t.Fatalf("ResolveStages: %v", err)
	}
	p := resolved[0].Params
	if p["envId"] != "env-test" {
		t.Errorf("{{app.env.test}} 解析失败：得 %v", p["envId"])
	}
	if p["repoId"] != "repo-stub" {
		t.Errorf("{{app.repo}} 解析失败：得 %v", p["repoId"])
	}
	if p["lane"] != "main" {
		t.Errorf("{{run.branch}} 解析失败：得 %v", p["lane"])
	}
	if p["literal"] != "plain-value" {
		t.Errorf("字面量被错误改写：得 %v", p["literal"])
	}
}
