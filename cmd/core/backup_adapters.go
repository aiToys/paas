package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/aitoys/paas/internal/backup"
	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/pkg/tenant"
)

// backupTargetResolver 桥接 dataservice repo → backup.TargetResolver：
// resourceID（ds ID）→ 数据面定位（ns/Service/Secret 名，全部从实体派生，
// 不含凭证明文——Job 经 secretKeyRef 引用 Secret）。
type backupTargetResolver struct{ repo dataservice.Repository }

func (r backupTargetResolver) ResolveTarget(ctx context.Context, tenantID, resourceID string) (backup.Target, error) {
	d, err := r.repo.Get(ctx, resourceID)
	if err != nil {
		if dataservice.IsExternal(d.Source) {
			return backup.Target{}, fmt.Errorf("外部实例不支持平台快照")
		}
		return backup.Target{}, fmt.Errorf("数据服务不存在: %w", err)
	}
	if tid, ok := tenant.TenantFrom(ctx); ok && d.TenantID != "" && d.TenantID != tid {
		return backup.Target{}, fmt.Errorf("数据服务不存在") // 跨租户统一不泄漏
	}
	return backup.Target{
		Namespace:   tenant.Namespace(tenantID),
		ServiceName: d.ID,             // K8s Service 名 = ds ID（FillConnection 同源约定）
		SecretName:  d.ID + "-secret", // reconciler 建 Secret 命名约定（dataservice_controller）
		Engine:      dataservice.EngineOf(d),
		Image:       d.Image,
	}, nil
}

// backupRestorer 桥接 dataservice repo → backup.Restorer：复制源实例规格创建恢复目标
// 新实例（走 ApplyRepo 装饰链——Create 后 applier 投影 CR，reconciler 拉 STS；
// 恢复 Job 内 pg_isready 循环等实例就绪）。
type backupRestorer struct {
	repo  dataservice.Repository
	quota dataservice.QuotaCheckFunc
}

func (r backupRestorer) CreateRestored(ctx context.Context, tenantID, sourceID, targetID, name string) (string, error) {
	if existing, err := r.repo.Get(ctx, targetID); err == nil {
		return existing.ID, nil
	} else if !errors.Is(err, dataservice.ErrNotFound) {
		return "", err
	}
	src, err := r.repo.Get(ctx, sourceID)
	if err != nil {
		return "", fmt.Errorf("源数据服务不存在: %w", err)
	}
	if tid, ok := tenant.TenantFrom(ctx); ok && src.TenantID != "" && src.TenantID != tid {
		return "", fmt.Errorf("源数据服务不存在")
	}
	if !backup.EngineSupported(dataservice.EngineOf(src)) || dataservice.IsExternal(src.Source) {
		return "", fmt.Errorf("源实例不再支持托管 PostgreSQL 恢复")
	}
	nd := src
	nd.ID = targetID    // store 生成新 ID
	nd.Name = name      // 恢复实例新名
	nd.Status = ""      // store 置 creating
	nd.Connection = nil // 新实例生成新凭证（FillConnection）
	replicas := 1
	nd.Replicas = &replicas // 恢复目标必须启动；避免复制已停止源的 0 副本
	if r.quota == nil {
		return "", fmt.Errorf("恢复配额检查未装配")
	}
	if err := r.quota(ctx, 1); err != nil {
		return "", fmt.Errorf("恢复实例创建被配额拒绝: %w", err)
	}
	saved, err := r.repo.Create(ctx, nd)
	if err != nil {
		_ = r.quota(ctx, -1)
		if existing, getErr := r.repo.Get(ctx, targetID); getErr == nil {
			return existing.ID, nil
		}
		return "", fmt.Errorf("创建恢复实例失败: %w", err)
	}
	return saved.ID, nil
}
