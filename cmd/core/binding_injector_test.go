package main

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/appconfig"
	appcfgmemory "github.com/aitoys/paas/internal/appconfig/memory"
	"github.com/aitoys/paas/internal/dataservice"
	dsmemory "github.com/aitoys/paas/internal/dataservice/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// TestInjectKeys 验证各 Kind 映射到正确的 appconfig key/value（value 取自 ds.Connection）。
//
//nolint:gosec // G101 误报：测试 mock 凭据字段名（accessKey/secretKey/uri），非真实凭据
func TestInjectKeys(t *testing.T) {
	ds := dataservice.DataService{Kind: dataservice.KindDB, Connection: map[string]string{"uri": "mysql://u:p@h:3306/db"}} //nolint:gosec // G101 误报：测试 mock URL，非真实凭据
	kv := injectKeys(ds)
	if len(kv) != 1 || kv[0].Key != "DATABASE_URL" || kv[0].Value != "mysql://u:p@h:3306/db" {
		t.Fatalf("db injectKeys 错误: %v", kv)
	}
	dsS := dataservice.DataService{Kind: dataservice.KindStorage, Connection: map[string]string{"endpoint": "http://h:9000", "accessKey": "ak", "secretKey": "sk"}} //nolint:gosec // G101 误报：测试 mock 值
	kvS := injectKeys(dsS)
	if len(kvS) != 3 {
		t.Fatalf("storage 应 3 个 key，got %d", len(kvS))
	}
	want := map[string]string{"MINIO_ENDPOINT": "http://h:9000", "MINIO_ACCESS_KEY": "ak", "MINIO_SECRET_KEY": "sk"}
	for _, k := range kvS {
		if want[k.Key] != k.Value {
			t.Fatalf("storage key %s value 错误: got %s want %s", k.Key, k.Value, want[k.Key])
		}
	}
}

// TestDSBindingInjectorOnBindUnbind 端到端：绑定->appconfig 出现连接 key；解绑->清除。
func TestDSBindingInjectorOnBindUnbind(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	dsRepo := dsmemory.NewStore()
	cfgRepo := appcfgmemory.NewStore()
	inj := dsBindingInjector{dsRepo: dsRepo, cfgRepo: cfgRepo}

	// 建一个 mysql 数据服务（Create 自动生成 Connection.uri）
	created, err := dsRepo.Create(ctx, dataservice.DataService{
		Kind: dataservice.KindDB, Name: "bind-db", EnvID: "env-acme-test",
		Spec: map[string]string{"engine": "mysql", "version": "8", "size_gb": "20"},
	})
	if err != nil {
		t.Fatalf("建 ds 失败: %v", err)
	}
	if created.Connection["uri"] == "" {
		t.Fatalf("Create 应生成 uri")
	}

	// OnBind -> appconfig 出现 DATABASE_URL（btype 用具体 kind "db"，与前端 TypeKey 对齐）
	if err := inj.OnBind(ctx, "app-1", "db", created.ID); err != nil {
		t.Fatalf("OnBind 失败: %v", err)
	}
	items, err := cfgRepo.List(ctx, "app-1", "env-acme-test")
	if err != nil {
		t.Fatalf("List appconfig 失败: %v", err)
	}
	found := false
	for _, it := range items {
		if it.Key == "DATABASE_URL" {
			found = true
			if it.Type != appconfig.TypeSecret {
				t.Fatalf("DATABASE_URL 应 TypeSecret")
			}
		}
	}
	if !found {
		t.Fatalf("OnBind 后应注入 DATABASE_URL")
	}

	// 非 dataservice 类型应无副作用
	if err := inj.OnBind(ctx, "app-1", "workload", "wl-x"); err != nil {
		t.Fatalf("OnBind 非 dataservice 应无错: %v", err)
	}

	// OnUnbind -> DATABASE_URL 清除
	if err := inj.OnUnbind(ctx, "app-1", "db", created.ID); err != nil {
		t.Fatalf("OnUnbind 失败: %v", err)
	}
	items2, _ := cfgRepo.List(ctx, "app-1", "env-acme-test")
	for _, it := range items2 {
		if it.Key == "DATABASE_URL" {
			t.Fatalf("OnUnbind 后 DATABASE_URL 应已清除")
		}
	}
}
