package governance_test

import (
	"context"
	"testing"

	"github.com/aitoys/paas/internal/governance"
	"github.com/aitoys/paas/internal/governance/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// 部署即注册（autoreg.go）：幂等语义锁死——新建注册、同名跳过、port=0 不注册。
func TestAutoRegisterService(t *testing.T) {
	repo := memory.NewStore()
	ctx := tenant.WithTenant(context.Background(), "t-acme")
	spec := governance.Service{
		AppID: "app-1", EnvID: "env-1", Name: "app-1-web", Protocol: governance.ProtocolHTTP, Port: 80,
	}

	if err := governance.AutoRegisterService(ctx, repo, spec); err != nil {
		t.Fatalf("首次注册失败: %v", err)
	}
	svcs, err := repo.ListServices(ctx, "env-1", "app-1")
	if err != nil || len(svcs) != 1 {
		t.Fatalf("注册后应有 1 个服务: %v %d", err, len(svcs))
	}

	// 幂等：同名已存在跳过，不报「服务名已存在」
	if err := governance.AutoRegisterService(ctx, repo, spec); err != nil {
		t.Fatalf("重复注册应跳过不报错: %v", err)
	}
	if svcs, _ := repo.ListServices(ctx, "env-1", "app-1"); len(svcs) != 1 {
		t.Fatalf("重复注册后仍应只有 1 个服务，got %d", len(svcs))
	}

	// port=0（无服务端口）不注册
	if err := governance.AutoRegisterService(ctx, repo, governance.Service{
		AppID: "app-1", EnvID: "env-1", Name: "app-1-job", Protocol: governance.ProtocolHTTP,
	}); err != nil {
		t.Fatalf("port=0 应静默跳过: %v", err)
	}
	if svcs, _ := repo.ListServices(ctx, "env-1", "app-1"); len(svcs) != 1 {
		t.Fatalf("port=0 不应产生新服务，got %d", len(svcs))
	}
}
