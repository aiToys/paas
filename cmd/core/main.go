// Package main 是 Platform Core 的启动入口。
// 职责：组装依赖、注册插件、按拓扑顺序 Init+Run 插件、暴露探针端点。
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/aitoys/paas/internal/core/health"
	coreplugin "github.com/aitoys/paas/internal/core/plugin"
	"github.com/aitoys/paas/pkg/plugin"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// 本 plan 阶段：无业务插件；MaaS 插件在 Plan 3 注册进来。
	if err := run(ctx, nil); err != nil {
		log.Fatalf("core 启动失败: %v", err)
	}
}

// run 启动 HTTP 探针并引导插件；返回非 nil 则进程以 1 退出。
func run(ctx context.Context, plugins []plugin.Plugin) error {
	go serveHealth()
	ran, err := bootstrapCore(ctx, plugins)
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
func bootstrapCore(ctx context.Context, plugins []plugin.Plugin) (map[string]bool, error) {
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

	// 本 plan 阶段 CoreDeps 仍是占位；Plan 2 注入 DB/EventBus/OTel。
	deps := noopCoreDeps{}
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

// noopCoreDeps 是本 plan 的 CoreDeps 占位实现；Plan 2 替换为真实依赖。
type noopCoreDeps struct{}

func (noopCoreDeps) Logger() interface{} { return nil }

func serveHealth() {
	mux := http.NewServeMux()
	mux.Handle("/livez", health.NewHandler())
	srv := &http.Server{Addr: ":8080", Handler: mux}
	if err := srv.ListenAndServe(); err != nil {
		log.Printf("health 服务退出: %v", err)
	}
}
