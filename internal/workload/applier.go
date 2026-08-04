package workload

import (
	"context"
	"log"
)

// Applier 把期望状态投影到数据面（K8s 启用时写 Workload CRD；nil 时 ApplyRepo 不调用）。
// 解耦控制面（PG/memory）与数据面（K8s CRD）：PG 作 API 查询源，CRD 作 reconcile 源。
type Applier interface {
	// Apply 把工作负载期望状态投影到数据面（CreateOrUpdate CRD）。
	Apply(ctx context.Context, w Workload) error
	// Delete 从数据面移除（级联清 K8s 资源）。
	Delete(ctx context.Context, id string) error
}

// ApplyRepo 装饰 Repository：Create/Update/UpdateImage/Delete 成功后投影数据面。
// 数据面投影失败不阻塞控制面写（控制面真源优先），但记日志留可观测痕迹，
// 避免「控制面显示创建成功但 K8s 未建」的静默不一致。
type ApplyRepo struct {
	Repository
	applier Applier
}

// NewApplyRepo 包装 inner，在其写操作后调用 applier（applier 为 nil 时透传，无副作用）。
func NewApplyRepo(inner Repository, a Applier) *ApplyRepo {
	return &ApplyRepo{Repository: inner, applier: a}
}

// applyLog 数据面投影失败时记日志（不阻断控制面写）。applier 为 nil 时透传。
func (r *ApplyRepo) applyLog(op, id string, err error) {
	if err != nil {
		log.Printf("[workload-applier] 数据面投影失败 id=%s op=%s err=%v", id, op, err)
	}
}

func (r *ApplyRepo) Create(ctx context.Context, w Workload) error {
	if err := r.Repository.Create(ctx, w); err != nil {
		return err
	}
	if r.applier != nil {
		r.applyLog("create", w.ID, r.applier.Apply(ctx, w))
	}
	return nil
}

func (r *ApplyRepo) Update(ctx context.Context, id string, replicas int, status string) (Workload, error) {
	saved, err := r.Repository.Update(ctx, id, replicas, status)
	if err != nil {
		return saved, err
	}
	if r.applier != nil {
		r.applyLog("update", saved.ID, r.applier.Apply(ctx, saved))
	}
	return saved, nil
}

func (r *ApplyRepo) UpdateImage(ctx context.Context, id, image, imageRef string) (Workload, error) {
	saved, err := r.Repository.UpdateImage(ctx, id, image, imageRef)
	if err != nil {
		return saved, err
	}
	if r.applier != nil {
		r.applyLog("update-image", saved.ID, r.applier.Apply(ctx, saved))
	}
	return saved, nil
}

func (r *ApplyRepo) Delete(ctx context.Context, id string) error {
	if err := r.Repository.Delete(ctx, id); err != nil {
		return err
	}
	if r.applier != nil {
		if err := r.applier.Delete(ctx, id); err != nil {
			log.Printf("[workload-applier] 数据面删除投影失败 id=%s op=delete err=%v", id, err)
		}
	}
	return nil
}
