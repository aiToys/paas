package main

import (
	"context"
	"log"

	"github.com/aitoys/paas/internal/appconfig"
	"github.com/aitoys/paas/internal/configcenter"
	"github.com/aitoys/paas/internal/core/application"
	"github.com/aitoys/paas/internal/workload"
)

// appCascadeDeleter 桥接 application.CascadeDeleter：删应用前清理关联的工作负载 + 应用配置 + 应用维度动态配置。
// admin + 租户侧共用（best-effort，失败记日志不阻断删除）。
// devops 历史记录（仓库/构建/镜像/发布）保留作历史归档，不随应用删除。
type appCascadeDeleter struct {
	wl      workload.Repository
	cfg     appconfig.Repository
	cc      configcenter.Repository                    // 应用派生命名空间（scope=app）级联删（best-effort）
	members application.MemberRepository               // 级联清应用成员（best-effort；PG 侧 CASCADE 已兜底）
	wlQuota func(ctx context.Context, delta int) error // 工作负载删除成功后回收配额（best-effort）
}

// CascadeDelete 删除指定应用下的全部工作负载与应用配置。
// 工作负载删除成功后回收 workload 维度配额（与 workload handler Delete 对齐）。
func (c appCascadeDeleter) CascadeDelete(ctx context.Context, appID string) error {
	wls, lErr := c.wl.List(ctx, "", appID, "", "", "")
	if lErr != nil {
		return lErr
	}
	for _, w := range wls {
		if err := c.wl.Delete(ctx, w.ID); err != nil {
			log.Printf("级联删工作负载失败（best-effort）: app=%s wl=%s: %v", appID, w.ID, err) //nolint:gosec // G706 误报
			continue
		}
		if c.wlQuota != nil {
			_ = c.wlQuota(ctx, -1) // 删除成功后回收 workload 配额（best-effort）
		}
	}
	cfgs, cErr := c.cfg.List(ctx, appID, "")
	if cErr != nil {
		return cErr
	}
	for _, cf := range cfgs {
		if err := c.cfg.Delete(ctx, cf.ID); err != nil {
			log.Printf("级联删应用配置失败（best-effort）: app=%s cfg=%s: %v", appID, cf.ID, err) //nolint:gosec // G706 误报
		}
	}
	// 级联清应用成员（内存路径必须；PG 侧 FK CASCADE 已兜底，重复调用幂等）。
	if c.members != nil {
		if err := c.members.RemoveAppMembers(ctx, appID); err != nil {
			log.Printf("级联删应用成员失败（best-effort）: app=%s: %v", appID, err) //nolint:gosec // G706 误报
		}
	}
	// 级联删应用派生命名空间（scope=app 动态配置；PG 侧无 FK 关联应用，必须显式删）。
	if c.cc != nil {
		if ns, ok, err := c.cc.FindAppNamespace(ctx, appID); err == nil && ok {
			if err := c.cc.DeleteNamespace(ctx, ns.ID); err != nil {
				log.Printf("级联删应用动态配置失败（best-effort）: app=%s: %v", appID, err) //nolint:gosec // G706 误报
			}
		}
	}
	return nil
}
