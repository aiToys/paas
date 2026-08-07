package main

import (
	"context"
	"log"

	"github.com/aitoys/paas/internal/appconfig"
	"github.com/aitoys/paas/internal/workload"
)

// appCascadeDeleter 桥接 application.CascadeDeleter：删应用前清理关联的工作负载 + 应用配置。
// 与 appHandler.CascadeDelete 闭包同款（best-effort，失败记日志不阻断删除）。
// devops 历史记录（仓库/构建/镜像/发布）保留作历史归档，不随应用删除。
type appCascadeDeleter struct {
	wl  workload.Repository
	cfg appconfig.Repository
}

// CascadeDelete 删除指定应用下的全部工作负载与应用配置。
func (c appCascadeDeleter) CascadeDelete(ctx context.Context, appID string) error {
	wls, lErr := c.wl.List(ctx, "", appID, "")
	if lErr != nil {
		return lErr
	}
	for _, w := range wls {
		if err := c.wl.Delete(ctx, w.ID); err != nil {
			log.Printf("admin 级联删工作负载失败（best-effort）: app=%s wl=%s: %v", appID, w.ID, err) //nolint:gosec // G706 误报
		}
	}
	cfgs, cErr := c.cfg.List(ctx, appID, "")
	if cErr != nil {
		return cErr
	}
	for _, cf := range cfgs {
		if err := c.cfg.Delete(ctx, cf.ID); err != nil {
			log.Printf("admin 级联删应用配置失败（best-effort）: app=%s cfg=%s: %v", appID, cf.ID, err) //nolint:gosec // G706 误报
		}
	}
	return nil
}
