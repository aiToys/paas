package dataservice

import "context"

// Applier 把数据服务期望状态投影到数据面（K8s 启用时写 DataService CRD；nil 时透传）。
// 与 workload.Applier 同构：解耦控制面（PG/memory）与数据面（K8s CRD / Operator）。
// P1-3 接口铺路：真实 K8sApplier + DataService CRD + Reconciler 归后续（复用 workload spec 4 模式）。
type Applier interface {
	Apply(ctx context.Context, d DataService) error
	Delete(ctx context.Context, id string) error
}

// ApplyRepo 装饰 Repository：Create/Update/Delete 成功后投影数据面。
// 数据面投影失败不阻塞控制面写（控制面真源优先，与 workload.ApplyRepo 一致）。
type ApplyRepo struct {
	Repository
	applier Applier
}

// NewApplyRepo 包装 inner；applier 为 nil 时透传无副作用。
func NewApplyRepo(inner Repository, a Applier) *ApplyRepo {
	return &ApplyRepo{Repository: inner, applier: a}
}

func (r *ApplyRepo) Create(ctx context.Context, d DataService) (DataService, error) {
	saved, err := r.Repository.Create(ctx, d)
	if err != nil {
		return saved, err
	}
	if r.applier != nil {
		_ = r.applier.Apply(ctx, saved)
	}
	return saved, nil
}

func (r *ApplyRepo) Update(ctx context.Context, d DataService) (DataService, error) {
	saved, err := r.Repository.Update(ctx, d)
	if err != nil {
		return saved, err
	}
	if r.applier != nil {
		_ = r.applier.Apply(ctx, saved)
	}
	return saved, nil
}

func (r *ApplyRepo) Delete(ctx context.Context, id string) error {
	if err := r.Repository.Delete(ctx, id); err != nil {
		return err
	}
	if r.applier != nil {
		_ = r.applier.Delete(ctx, id)
	}
	return nil
}
