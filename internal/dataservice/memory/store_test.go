package memory

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/pkg/tenant"
)

func acmeCtx() context.Context   { return tenant.WithTenant(context.Background(), "t-acme") }
func globexCtx() context.Context { return tenant.WithTenant(context.Background(), "t-globex") }

// TestListByKind 验证按 kind 过滤 + 倒序。
func TestListByKind(t *testing.T) {
	s := NewStore()
	all, _ := s.List(acmeCtx(), "")
	if len(all) != 3 {
		t.Fatalf("acme 应 3 个数据服务，got %d", len(all))
	}
	dbs, _ := s.List(acmeCtx(), dataservice.KindDB)
	if len(dbs) != 1 || dbs[0].Name != "acme-orders-db" {
		t.Fatalf("acme db 应 1 个，got %+v", dbs)
	}
	// 倒序：最新（mq, -24h）应在最前
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
	d, err := s.Update(globexCtx(), dataservice.DataService{
		ID: "ds-globex-vector", Status: dataservice.StatusRunning,
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
	acme, _ := s.List(acmeCtx(), "")
	globex, _ := s.List(globexCtx(), "")
	if len(acme) != 3 || len(globex) != 2 {
		t.Fatalf("acme 3 / globex 2，got %d/%d", len(acme), len(globex))
	}
	// globex 跨租户 Get acme 资源应 not found
	if _, err := s.Get(globexCtx(), "ds-acme-db"); err == nil {
		t.Fatal("globex 不应见到 acme 资源")
	}
}

// TestDelete 验证删除 + 跨租户拒绝。
func TestDelete(t *testing.T) {
	s := NewStore()
	if err := s.Delete(globexCtx(), "ds-acme-db"); err == nil {
		t.Fatal("跨租户删除应拒绝")
	}
	if err := s.Delete(acmeCtx(), "ds-acme-db"); err != nil {
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
