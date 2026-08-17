package change

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/pkg/tenant"
)

func acmeCtx() context.Context { return tenant.WithTenant(context.Background(), "t-acme") }

func TestChangeValidate(t *testing.T) {
	c := Change{Title: "用户导出", Type: ChangeFeat, Branch: "feat/user-export", RepoID: "repo-1", AppID: "app-1"}
	if err := c.Validate(); err != nil {
		t.Fatalf("合法变更不应报错: %v", err)
	}
	c.Type = "perf" // 非法类型
	if err := c.Validate(); err == nil {
		t.Fatal("type=perf 应被拒（仅 feat|hotfix）")
	}
	c2 := Change{Type: ChangeHotfix, Branch: ""} // 无分支且未引用
	if err := c2.Validate(); err == nil {
		t.Fatal("branch 空应被拒")
	}
}

func TestMemoryCRUD(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtx()
	created, err := s.CreateChange(ctx, Change{AppID: "app-1", RepoID: "r1", Title: "t", Type: ChangeFeat, Branch: "feat/a"})
	if err != nil {
		t.Fatalf("CreateChange: %v", err)
	}
	if created.Status != ChangeOpen {
		t.Fatalf("初始状态应 open, got %s", created.Status)
	}
	got, _ := s.GetChange(ctx, created.ID)
	if got.TenantID != "t-acme" {
		t.Fatalf("TenantID 以 ctx 为准, got %s", got.TenantID)
	}
	list, _ := s.ListChanges(ctx, "app-1", "")
	if len(list) != 1 {
		t.Fatalf("应 1 条, got %d", len(list))
	}
	filtered, _ := s.ListChanges(ctx, "app-1", ChangeOpen)
	if len(filtered) != 1 {
		t.Fatalf("状态过滤应命中")
	}
	// 跨租户 not found
	gctx := tenant.WithTenant(context.Background(), "t-globex")
	if _, err := s.GetChange(gctx, created.ID); err != ErrChangeNotFound {
		t.Fatalf("跨租户应 ErrChangeNotFound, got %v", err)
	}
	// 缺租户拒
	if _, err := s.CreateChange(context.Background(), Change{AppID: "a", Branch: "b", Type: ChangeFeat}); err != ErrNoTenant {
		t.Fatalf("缺租户应 ErrNoTenant, got %v", err)
	}
	// 分支租户内唯一（同 repo）
	if _, err := s.CreateChange(ctx, Change{AppID: "app-1", RepoID: "r1", Title: "dup", Type: ChangeFeat, Branch: "feat/a"}); err != ErrChangeExists {
		t.Fatalf("同分支应 ErrChangeExists, got %v", err)
	}
}

func TestBatchCRUD(t *testing.T) {
	s := NewMemoryStore()
	ctx := acmeCtx()
	b, err := s.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "r1", Title: "8月集成", Branch: "integration/20260815-1"})
	if err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	if b.Status != BatchCollecting {
		t.Fatalf("初始 collecting, got %s", b.Status)
	}
	b.Status = BatchTesting
	b.ChangeIDs = []string{"chg-1", "chg-2"}
	upd, err := s.UpdateBatch(ctx, b)
	if err != nil {
		t.Fatalf("UpdateBatch: %v", err)
	}
	got, _ := s.GetBatch(ctx, upd.ID)
	if len(got.ChangeIDs) != 2 || got.ChangeIDs[0] != "chg-1" {
		t.Fatalf("ChangeIDs 有序持久化失败: %v", got.ChangeIDs)
	}
	if _, err := s.GetBatch(context.Background(), upd.ID); err != ErrNoTenant {
		t.Fatalf("缺租户应拒, got %v", err)
	}
}

func TestDeepCopyIsolation(t *testing.T) {
	// 读返回深拷贝：改返回值不影响 store（race 防御，与全仓 memory 模式一致）
	s := NewMemoryStore()
	ctx := acmeCtx()
	b, _ := s.CreateBatch(ctx, IntegrationBatch{AppID: "app-1", RepoID: "r1", Title: "t", Branch: "integration/x"})
	got, _ := s.GetBatch(ctx, b.ID)
	got.ChangeIDs = append(got.ChangeIDs, "chg-hack")
	again, _ := s.GetBatch(ctx, b.ID)
	if len(again.ChangeIDs) != 0 {
		t.Fatal("ChangeIDs 应深拷贝隔离")
	}
}

// TestChangeJSONCamelCase 回归（final review C1）：API 序列化必须 camelCase，
// 前端 api/change.ts 按 camelCase 消费；无标签时 Go 默认 PascalCase 致前端全 undefined。
func TestChangeJSONCamelCase(t *testing.T) {
	b, err := json.Marshal(Change{AppID: "app-1", BatchID: "b1", ConflictWith: "c2", CreatedAt: time.Unix(0, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, key := range []string{`"appId"`, `"batchId"`, `"conflictWith"`, `"createdAt"`, `"branchCreated"`, `"baseBranch"`} {
		if !strings.Contains(s, key) {
			t.Fatalf("Change JSON 缺 %s: %s", key, s)
		}
	}
	b2, _ := json.Marshal(IntegrationBatch{ChangeIDs: []string{"a"}, PipelineID: "p", RunID: "r", ReleaseIDs: []string{"x"}})
	for _, key := range []string{`"changeIds"`, `"pipelineId"`, `"runId"`, `"releaseIds"`} {
		if !strings.Contains(string(b2), key) {
			t.Fatalf("IntegrationBatch JSON 缺 %s: %s", key, string(b2))
		}
	}
}
