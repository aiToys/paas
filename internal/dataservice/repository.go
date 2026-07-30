package dataservice

import "context"

// Repository 是数据服务持久化接口。
// 全方法从 ctx 取租户强制过滤；跨租户访问统一 not found（不泄漏存在性）。
type Repository interface {
	// List 按 kind 过滤（kind 空表示全部），按 CreatedAt 倒序。
	List(ctx context.Context, kind string) ([]DataService, error)
	// Get 读取单条（跨租户 not found）。
	Get(ctx context.Context, id string) (DataService, error)
	// Create 创建（status 空时补 running）；返回创建后的实例。
	Create(ctx context.Context, d DataService) (DataService, error)
	// Update 更新 spec/status（生产写权限由 handler 校验）。
	Update(ctx context.Context, d DataService) (DataService, error)
	// Delete 删除（跨租户 not found）。
	Delete(ctx context.Context, id string) error
}
