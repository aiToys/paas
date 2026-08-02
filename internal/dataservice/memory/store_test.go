package memory

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/pkg/tenant"
)

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }

// seedAll 经 Create 自建跨两租户的 5 个数据服务（去 mock seed 后测试自建真源）。
// 返回按逻辑名索引的 DataService（含 store 生成的 ID），供测试引用。
func seedAll(t *testing.T, s *Store) map[string]dataservice.DataService {
	t.Helper()
	out := map[string]dataservice.DataService{}
	mk := func(tid, kind, name, env, status string, spec map[string]string) dataservice.DataService {
		ctx := tenant.WithTenant(context.Background(), tid)
		d, err := s.Create(ctx, dataservice.DataService{
			Kind: kind, Name: name, Spec: spec, Status: status, EnvID: env,
		})
		if err != nil {
			t.Fatalf("seed %s 失败: %v", name, err)
		}
		return d
	}
	// 顺序创建：db -> cache -> mq（mq 最后=最新，倒序应在首位）
	out["acme-db"] = mk("t-acme", dataservice.KindDB, "acme-orders-db", "env-acme-test",
		dataservice.StatusRunning, map[string]string{"engine": "postgres", "version": "15", "size_gb": "100"})
	out["acme-cache"] = mk("t-acme", dataservice.KindCache, "acme-session-cache", "env-acme-test",
		dataservice.StatusRunning, map[string]string{"engine": "redis", "mode": "cluster", "maxmemory_mb": "2048"})
	out["acme-mq"] = mk("t-acme", dataservice.KindMQ, "acme-events-mq", "env-acme-prod-bj",
		dataservice.StatusRunning, map[string]string{"engine": "nats", "partitions": "6"})
	out["globex-db"] = mk("t-globex", dataservice.KindDB, "globex-main-db", "env-globex-prod",
		dataservice.StatusRunning, map[string]string{"engine": "mysql", "version": "8", "size_gb": "200"})
	out["globex-vector"] = mk("t-globex", dataservice.KindVector, "globex-embedding", "env-globex-test",
		dataservice.StatusStopped, map[string]string{"engine": "milvus", "dimension": "1536"})
	return out
}

// TestListByKind 验证按 kind 过滤 + 倒序。
func TestListByKind(t *testing.T) {
	s := NewStore()
	seedAll(t, s)
	all, _ := s.List(acmeCtx(), "")
	if len(all) != 3 {
		t.Fatalf("acme 应 3 个数据服务，got %d", len(all))
	}
	dbs, _ := s.List(acmeCtx(), dataservice.KindDB)
	if len(dbs) != 1 || dbs[0].Name != "acme-orders-db" {
		t.Fatalf("acme db 应 1 个，got %+v", dbs)
	}
	// 倒序：最后创建的 mq 应在最前
	if all[0].Kind != dataservice.KindMQ {
		t.Fatalf("倒序后首个应为 mq，got %s", all[0].Kind)
	}
}

// TestCreateDefaultRunning 验证 status 空时补 running。
func TestCreateDefaultRunning(t *testing.T) {
	s := NewStore()
	d, err := s.Create(acmeCtx(), dataservice.DataService{
		Kind: dataservice.KindCache, Name: "new-cache",
		Spec:  map[string]string{"engine": "redis", "mode": "standalone", "maxmemory_mb": "512"},
		EnvID: "env-acme-test",
	})
	if err != nil {
		t.Fatalf("创建失败: %v", err)
	}
	if d.Status != dataservice.StatusRunning {
		t.Fatalf("status 应默认 running，got %s", d.Status)
	}
	if d.TenantID != "t-acme" {
		t.Fatalf("TenantID 应 ctx 注入，got %s", d.TenantID)
	}
}

// TestCreateDedup 验证租户内名称唯一。
func TestCreateDedup(t *testing.T) {
	s := NewStore()
	_, err := s.Create(acmeCtx(), dataservice.DataService{
		Kind: dataservice.KindDB, Name: "acme-orders-db",
		Spec: map[string]string{"engine": "postgres", "version": "15", "size_gb": "10"},
	})
	if err == nil {
		t.Fatal("同名应冲突")
	}
}

// TestValidateRequiredSpec 验证必填 spec 字段（storage.bucket 默认空=必填）。
func TestValidateRequiredSpec(t *testing.T) {
	s := NewStore()
	_, err := s.Create(acmeCtx(), dataservice.DataService{
		Kind: dataservice.KindStorage, Name: "no-bucket",
		Spec: map[string]string{"redundancy": "standard"}, // 缺 bucket
	})
	if err == nil {
		t.Fatal("缺 bucket 应校验失败")
	}
}

// TestUpdateStatus 验证状态变更。
func TestUpdateStatus(t *testing.T) {
	s := NewStore()
	seeded := seedAll(t, s)
	v := seeded["globex-vector"]
	d, err := s.Update(globexCtx(), dataservice.DataService{
		ID: v.ID, Status: dataservice.StatusRunning,
	})
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if d.Status != dataservice.StatusRunning {
		t.Fatalf("应改为 running，got %s", d.Status)
	}
}

// TestTenantIsolation 验证跨租户不可见 + Get 不泄漏。
func TestTenantIsolation(t *testing.T) {
	s := NewStore()
	seeded := seedAll(t, s)
	acme, _ := s.List(acmeCtx(), "")
	globex, _ := s.List(globexCtx(), "")
	if len(acme) != 3 || len(globex) != 2 {
		t.Fatalf("acme 3 / globex 2，got %d/%d", len(acme), len(globex))
	}
	// globex 跨租户 Get acme 资源应 not found
	if _, err := s.Get(globexCtx(), seeded["acme-db"].ID); err == nil {
		t.Fatal("globex 不应见到 acme 资源")
	}
}

// TestDelete 验证删除 + 跨租户拒绝。
func TestDelete(t *testing.T) {
	s := NewStore()
	seeded := seedAll(t, s)
	acmeDBID := seeded["acme-db"].ID
	if err := s.Delete(globexCtx(), acmeDBID); err == nil {
		t.Fatal("跨租户删除应拒绝")
	}
	if err := s.Delete(acmeCtx(), acmeDBID); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	all, _ := s.List(acmeCtx(), "")
	if len(all) != 2 {
		t.Fatalf("删除后应 2 个，got %d", len(all))
	}
}

// TestMissingTenant 验证缺失租户上下文即拒。
func TestMissingTenant(t *testing.T) {
	s := NewStore()
	if _, err := s.List(context.Background(), ""); err == nil {
		t.Fatal("缺失租户上下文应拒绝")
	}
}
