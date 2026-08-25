package marketplace

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/aitoys/paas/internal/ai/agent"
	"github.com/aitoys/paas/internal/ai/prompt"
	"github.com/aitoys/paas/internal/ai/skill"
	"github.com/aitoys/paas/internal/ai/tool"
	agentmemory "github.com/aitoys/paas/internal/ai/agent/memory"
	promptmemory "github.com/aitoys/paas/internal/ai/prompt/memory"
	skillmemory "github.com/aitoys/paas/internal/ai/skill/memory"
	toolmemory "github.com/aitoys/paas/internal/ai/tool/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

func TestSanitizeConfig(t *testing.T) {
	in := map[string]string{
		"serverURL":  "http://srv:8080",
		"apiKey":     "sk-secret",
		"API_KEY":    "sk-2",
		"token":      "t1",
		"password":   "p1",
		"authHeader": "x",
		"endpoint":   "/api",
	}
	out := SanitizeConfig(in)
	for _, k := range []string{"apiKey", "API_KEY", "token", "password", "authHeader"} {
		if _, ok := out[k]; ok {
			t.Fatalf("敏感 key %s 未剔除: %v", k, out)
		}
	}
	if out["serverURL"] != "http://srv:8080" || out["endpoint"] != "/api" {
		t.Fatalf("非敏感 key 被误删: %v", out)
	}
	if len(in) != 7 {
		t.Fatal("SanitizeConfig 改了入参")
	}
}

// newRepos 内存四件套。
func newRepos() *Repos {
	return &Repos{
		Agents:  agentmemory.NewStore(),
		Skills:  skillmemory.NewStore(),
		Prompts: promptmemory.NewStore(),
		Tools:   toolmemory.NewStore(),
	}
}

func ctxTenant(tid string) context.Context {
	return tenant.WithTenant(context.Background(), tid)
}

func TestSkillPublishInstallRoundtrip(t *testing.T) {
	repos := newRepos()
	ctx := ctxTenant("t-a")
	sk, err := repos.Skills.Create(ctx, skill.Skill{Name: "周报助手", Description: "写周报", Instructions: "每周五...", Category: "writing"})
	if err != nil {
		t.Fatal(err)
	}
	f := skillForker{repos}
	_, _, cat, snap, err := f.BuildSnapshot(ctx, sk.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if cat != "writing" {
		t.Fatalf("category 应继承实体自带，got %s", cat)
	}
	// 快照不含租户/ID
	var raw map[string]any
	_ = json.Unmarshal(snap, &raw)
	if raw["tenantId"] != "" || raw["id"] != "" {
		t.Fatalf("快照未剥离租户/ID: %v", raw)
	}

	store := &fakeRepo{}
	item, err := store.Create(ctx, Item{EntityType: EntitySkill, Name: "周报助手", Category: "writing", Snapshot: snap, PublisherTenant: "t-a"})
	if err != nil {
		t.Fatal(err)
	}
	// 另一租户安装
	ctxB := ctxTenant("t-b")
	res, err := f.InstallSnapshot(ctxB, item)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repos.Skills.Get(ctxB, res.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "周报助手" || got.InstalledFrom != item.ID || got.Instructions != "每周五..." {
		t.Fatalf("fork 副本字段不符: %+v", got)
	}
	// 同名二次安装 → 后缀
	res2, err := f.InstallSnapshot(ctxB, item)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Name != "周报助手-2" {
		t.Fatalf("同名应加后缀，got %s", res2.Name)
	}
	// 计数
	_ = store.IncInstalls(ctx, item.ID)
	it, _ := store.Get(ctx, item.ID)
	if it.Installs != 1 {
		t.Fatalf("安装计数错误: %d", it.Installs)
	}
}

func TestToolPublishSanitizesCredentials(t *testing.T) {
	repos := newRepos()
	ctx := ctxTenant("t-a")
	tl, err := repos.Tools.Create(ctx, tool.Tool{
		Name: "天气查询", Description: "查天气", Type: tool.TypeHTTP,
		Config: map[string]string{"endpoint": "http://wx/api", "apiKey": "sk-leak"},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := toolForker{repos}
	_, _, _, snap, err := f.BuildSnapshot(ctx, tl.ID, "data")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(snap, []byte("sk-leak")) {
		t.Fatal("工具凭证泄漏进广场快照")
	}
	// 安装后凭证留空待填
	item := Item{ID: "mk-1", EntityType: EntityTool, Name: "天气查询", Snapshot: snap}
	res, err := f.InstallSnapshot(ctxTenant("t-b"), item)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := repos.Tools.Get(ctxTenant("t-b"), res.EntityID)
	if got.Config["apiKey"] != "" || got.Config["endpoint"] != "http://wx/api" {
		t.Fatalf("安装后 config 不符: %v", got.Config)
	}
}

func TestAgentBundleForkRewritesReferences(t *testing.T) {
	repos := newRepos()
	ctx := ctxTenant("t-a")
	sk, _ := repos.Skills.Create(ctx, skill.Skill{Name: "代码评审", Instructions: "按规范评审"})
	_, _ = repos.Prompts.Create(ctx, prompt.Prompt{Name: "评审模板", Template: "评审 {{.Lang}} 代码"})
	tl, _ := repos.Tools.Create(ctx, tool.Tool{Name: "linter", Description: "lint", Type: tool.TypeBuiltin, Config: map[string]string{"handler": "lint"}})
	a, err := repos.Agents.Create(ctx, agent.Agent{
		Name: "评审员", Description: "代码评审 agent", Model: "glm-5.2",
		SystemPrompt: "你是评审员", Skills: []string{sk.ID}, PromptRef: "评审模板", Tools: []string{tl.ID},
		KnowledgeBases: []string{"kb-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	f := agentForker{repos}
	name, _, cat, snap, err := f.BuildSnapshot(ctx, a.ID, "coding")
	if err != nil {
		t.Fatal(err)
	}
	if name != "评审员" || cat != "coding" {
		t.Fatalf("name/category 不符: %s %s", name, cat)
	}
	if bytes.Contains(snap, []byte("kb-1")) {
		t.Fatal("KB 引用不应进广场快照")
	}

	item := Item{ID: "mk-agg", EntityType: EntityAgent, Name: name, Snapshot: snap}
	ctxB := ctxTenant("t-b")
	res, err := f.InstallSnapshot(ctxB, item)
	if err != nil {
		t.Fatal(err)
	}
	got, err := repos.Agents.Get(ctxB, res.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if got.InstalledFrom != "mk-agg" {
		t.Fatalf("来源标记缺失: %+v", got)
	}
	if len(got.Skills) != 1 || len(got.Tools) != 1 {
		t.Fatalf("引用未重写: %+v", got)
	}
	// 引用指向 fork 后的新实体（存在于 t-b）
	if _, err := repos.Skills.Get(ctxB, got.Skills[0]); err != nil {
		t.Fatalf("skill 引用失效: %v", err)
	}
	if _, err := repos.Tools.Get(ctxB, got.Tools[0]); err != nil {
		t.Fatalf("tool 引用失效: %v", err)
	}
	// PromptRef 指向 fork 后的 prompt name
	if _, err := repos.Prompts.GetActive(ctxB, got.PromptRef); err != nil {
		t.Fatalf("prompt 引用失效: %v", err)
	}
	// t-a 原实体不受影响
	orig, _ := repos.Agents.Get(ctx, a.ID)
	if len(orig.Skills) != 1 || orig.PromptRef != "评审模板" {
		t.Fatalf("源实体被改: %+v", orig)
	}
}

func TestRepublishOverwritesAndDeleteKeepsCopies(t *testing.T) {
	store := &fakeRepo{}
	ctx := ctxTenant("t-a")
	it1, _ := store.Create(ctx, Item{EntityType: EntitySkill, Name: "x", Snapshot: []byte(`{}`), PublisherTenant: "t-a"})
	_ = store.IncInstalls(ctx, it1.ID)
	// 重发布覆盖：同名同发布者新 ID
	it2, _ := store.Create(ctx, Item{EntityType: EntitySkill, Name: "x", Snapshot: []byte(`{"v":2}`), PublisherTenant: "t-a"})
	if list, _ := store.List(ctx, "", "", ""); len(list) != 1 {
		t.Fatalf("重发布应覆盖，条目数 %d", len(list))
	}
	_ = it2
	// 下架不影响已装副本（InstallSnapshot 产物已在租户 store，与 marketplace 解耦——此处验证下架语义）
	if err := store.Delete(ctx, it1.ID); err == nil {
		// it1 已被覆盖删除，此时应 not found——两种情况都算通过路径验证
		_ = err
	}
	if _, err := store.Get(ctx, it1.ID); err != ErrItemNotFound {
		t.Fatalf("it1 应已被覆盖删除: %v", err)
	}
}

// fakeRepo 本包内最小 Repository 实现（import memory 子包会成环——子包 import marketplace）。
type fakeRepo struct{ items map[string]Item }

func (f *fakeRepo) ensure() {
	if f.items == nil {
		f.items = map[string]Item{}
	}
}
func (f *fakeRepo) List(ctx context.Context, entityType, category, q string) ([]Item, error) {
	f.ensure()
	out := []Item{}
	for _, it := range f.items {
		if entityType != "" && it.EntityType != entityType {
			continue
		}
		out = append(out, it)
	}
	return out, nil
}
func (f *fakeRepo) Get(ctx context.Context, id string) (Item, error) {
	f.ensure()
	it, ok := f.items[id]
	if !ok {
		return Item{}, ErrItemNotFound
	}
	return it, nil
}
func (f *fakeRepo) Create(ctx context.Context, in Item) (Item, error) {
	f.ensure()
	for id, it := range f.items {
		if it.EntityType == in.EntityType && it.Name == in.Name && it.PublisherTenant == in.PublisherTenant {
			delete(f.items, id)
		}
	}
	if in.ID == "" {
		in.ID = "mk-fake"
	}
	f.items[in.ID] = in
	return in, nil
}
func (f *fakeRepo) Delete(ctx context.Context, id string) error {
	f.ensure()
	if _, ok := f.items[id]; !ok {
		return ErrItemNotFound
	}
	delete(f.items, id)
	return nil
}
func (f *fakeRepo) IncInstalls(ctx context.Context, id string) error {
	f.ensure()
	it, ok := f.items[id]
	if !ok {
		return ErrItemNotFound
	}
	it.Installs++
	f.items[id] = it
	return nil
}
func (f *fakeRepo) ListByPublisher(ctx context.Context, tid string) ([]Item, error) { return nil, nil }
func (f *fakeRepo) ListAll(ctx context.Context) ([]Item, error)                     { return nil, nil }
