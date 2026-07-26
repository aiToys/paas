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
	"syscall"

	"github.com/aitoys/paas/internal/core/gateway"
	"github.com/aitoys/paas/internal/core/health"
	coreplugin "github.com/aitoys/paas/internal/core/plugin"
	"github.com/aitoys/paas/internal/maas"
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
	go serveHTTP(gw, meter)
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

// resolveAPIKey 解析 API Key：环境变量 PAAS_API_KEY 优先，否则用开发默认值。
func resolveAPIKey() string {
	if k := os.Getenv("PAAS_API_KEY"); k != "" {
		return k
	}
	return "sk-paas-dev-key"
}

// serveHTTP 挂载 OpenAI 兼容端点（鉴权）与存活探针。
func serveHTTP(gw *gateway.Gateway, meter *gateway.Meter) {
	apiKey := resolveAPIKey()
	mux := http.NewServeMux()
	auth := gateway.APIKeyAuth(apiKey)
	mux.Handle("/v1/chat/completions", auth(gateway.ChatCompletions(gw, meter)))
	mux.Handle("/v1/models", auth(gateway.ListModels(gw)))
	mux.Handle("/livez", health.NewHandler())

	srv := &http.Server{Addr: ":8080", Handler: mux}
	log.Printf("HTTP 监听 :8080（API Key: %s）", apiKey)
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("HTTP 服务退出: %v", err)
	}
}
