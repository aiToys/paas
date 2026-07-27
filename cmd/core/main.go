// Package main 是 Platform Core 的启动入口。
// 职责：组装依赖、注册插件、按拓扑顺序 Init+Run 插件、暴露 Gateway 与探针端点。
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/aitoys/paas/internal/core/application"
	appmemory "github.com/aitoys/paas/internal/core/application/memory"
	"github.com/aitoys/paas/internal/core/gateway"
	"github.com/aitoys/paas/internal/core/health"
	"github.com/aitoys/paas/internal/core/identity"
	idmemory "github.com/aitoys/paas/internal/core/identity/memory"
	coreplugin "github.com/aitoys/paas/internal/core/plugin"
	"github.com/aitoys/paas/internal/environment"
	envmemory "github.com/aitoys/paas/internal/environment/memory"
	"github.com/aitoys/paas/internal/maas"
	"github.com/aitoys/paas/internal/workload"
	wlmemory "github.com/aitoys/paas/internal/workload/memory"
	"github.com/aitoys/paas/pkg/plugin"
	"github.com/aitoys/paas/pkg/provider"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	gw := gateway.New()
	meter := &gateway.Meter{}
	deps := realCoreDeps{gw: gw}
	// 本切片注册 MaaS 插件（接入 echo provider）
	plugins := []plugin.Plugin{&maas.MaaSPlugin{}}

	if err := run(ctx, plugins, deps, gw, meter); err != nil {
		log.Fatalf("core 启动失败: %v", err)
	}
}

// run 启动 HTTP 服务并引导插件；返回非 nil 则进程以 1 退出。
func run(ctx context.Context, plugins []plugin.Plugin, deps plugin.CoreDeps, gw *gateway.Gateway, meter *gateway.Meter) error {
	idb := idmemory.NewStore()
	seedIdentity(idb, resolveAPIKey())
	go serveHTTP(gw, meter, idb)
	ran, err := bootstrapCore(ctx, plugins, deps)
	if err != nil {
		return err
	}
	log.Printf("core 启动完成，已运行插件: %v", ran)
	<-ctx.Done()
	log.Println("core 收到退出信号，停止")
	return nil
}

// bootstrapCore 把插件注册到 Registry，按拓扑顺序 Init + Run。
// 返回 map[插件名]是否成功运行。任一插件 Init/Run 失败则中止并返回错误。
func bootstrapCore(ctx context.Context, plugins []plugin.Plugin, deps plugin.CoreDeps) (map[string]bool, error) {
	registry := coreplugin.NewRegistry()
	for _, p := range plugins {
		if err := registry.Register(p); err != nil {
			return nil, err
		}
	}
	ordered, err := registry.LoadOrder()
	if err != nil {
		return nil, fmt.Errorf("插件加载顺序解析失败: %w", err)
	}

	ran := map[string]bool{}
	for _, p := range ordered {
		if err := p.Init(ctx, deps); err != nil {
			return ran, fmt.Errorf("插件 %s 初始化失败: %w", p.Manifest().Name, err)
		}
		if err := p.Run(ctx); err != nil {
			return ran, fmt.Errorf("插件 %s 运行失败: %w", p.Manifest().Name, err)
		}
		ran[p.Manifest().Name] = true
	}
	return ran, nil
}

// realCoreDeps 是 CoreDeps 的真实实现，向插件注入 Gateway。
type realCoreDeps struct {
	gw *gateway.Gateway
}

func (d realCoreDeps) Logger() interface{}                { return nil }
func (d realCoreDeps) Gateway() provider.GatewayRegistrar { return d.gw }

// resolveAPIKey 解析 API Key：环境变量 PAAS_API_KEY 优先，否则用开发默认值（sk-acme-admin）。
// 返回值用于 seed 兼容（自定义 Key 追加为 t-acme admin）与日志展示。
func resolveAPIKey() string {
	if k := os.Getenv("PAAS_API_KEY"); k != "" {
		return k
	}
	return "sk-acme-admin"
}

// serveHTTP 挂载 OpenAI 兼容端点（鉴权 + RBAC）与存活探针。
// idb 提供 API Key → (租户, 角色) 解析；模型目录平台共享，应用按租户隔离。
func serveHTTP(gw *gateway.Gateway, meter *gateway.Meter, idb identity.Repository) {
	apiKey := resolveAPIKey()
	mux := http.NewServeMux()
	auth := gateway.APIKeyAuth(idb)
	mux.Handle("/v1/chat/completions", auth(gateway.Require("model:infer")(gateway.ChatCompletions(gw, meter))))
	mux.Handle("/v1/models", auth(gateway.Require("model:read")(gateway.ListModels(gw))))
	mux.Handle("/api/models", auth(gateway.Require("model:read")(gateway.CatalogModels(gw))))
	// 应用为主线 REST API：方法级权限由 handler 内部按 application:*/binding:write 校验
	appHandler := application.NewHandler(appmemory.NewStore())
	appHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// 工作负载（应用运行形态）：方法级权限 workload:read/write。
	wlHandler := workload.NewHandler(wlmemory.NewStore())
	wlHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }

	// composite：/api/applications/{id}/workloads 段交工作负载 handler，其余交应用 handler。
	composite := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/workloads") {
			wlHandler.ServeHTTP(w, r)
			return
		}
		appHandler.ServeHTTP(w, r)
	})
	mux.Handle("/api/applications", auth(composite))
	mux.Handle("/api/applications/", auth(composite))
	mux.Handle("/api/workloads", auth(wlHandler))
	mux.Handle("/api/workloads/", auth(wlHandler))

	// 环境（物理隔离单元 prod|test）：方法级权限 environment:read/write。
	envHandler := environment.NewHandler(envmemory.NewStore())
	envHandler.Authorize = func(r *http.Request, perm string) bool { return gateway.RequestAllowed(r, perm) }
	mux.Handle("/api/environments", auth(envHandler))
	mux.Handle("/api/environments/", auth(envHandler))

	mux.Handle("/livez", health.NewHandler())

	srv := &http.Server{Addr: ":8080", Handler: mux}
	log.Printf("HTTP 监听 :8080（默认 API Key: %s）", apiKey)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("HTTP 服务退出: %v", err)
	}
}
