package governance

import "context"

// Repository 是服务治理持久化接口（服务 + 实例 + 路由 + 熔断器）。
// 方法名带实体前缀（ListServices/CreateService/...）以避免单 Store 实现时的重名冲突。
// 全方法从 ctx 取租户强制过滤；跨租户访问统一 not found（不泄漏存在性）。
type Repository interface {
	ServiceStore
	InstanceStore
	RouteStore
	BreakerStore
}

// ServiceStore 服务定义仓储。
type ServiceStore interface {
	ListServices(ctx context.Context, envID, appID string) ([]Service, error)
	GetService(ctx context.Context, id string) (Service, error)
	CreateService(ctx context.Context, s Service) (Service, error)
	DeleteService(ctx context.Context, id string) error
}

// InstanceStore 服务实例仓储。
type InstanceStore interface {
	ListInstances(ctx context.Context, serviceID string) ([]Instance, error)
	RegisterInstance(ctx context.Context, i Instance) (Instance, error)
	DeregisterInstance(ctx context.Context, id string) error
	// Heartbeat 更新实例 UpdatedAt（消费方据此判断存活；本期不过期剔除）。
	Heartbeat(ctx context.Context, id string) (Instance, error)
	// InstanceServiceID 返回实例所属服务 ID（用于注销时校验生产权限）。
	InstanceServiceID(ctx context.Context, id string) (string, error)
}

// RouteStore API 网关路由仓储（治理四件套之 API 网关）。
type RouteStore interface {
	// ListRoutes 路由列表（按 serviceID 过滤，空=全部），按 UpdatedAt 倒序。
	ListRoutes(ctx context.Context, serviceID string) ([]Route, error)
	// GetRoute 读取单条（跨租户 not found）。
	GetRoute(ctx context.Context, id string) (Route, error)
	// CreateRoute 创建（租户内 Name 唯一）。
	CreateRoute(ctx context.Context, r Route) (Route, error)
	// UpdateRoute 更新（path/methods/enabled/stripPath/serviceId）。
	UpdateRoute(ctx context.Context, r Route) (Route, error)
	// DeleteRoute 删除（跨租户 not found）。
	DeleteRoute(ctx context.Context, id string) error
}

// BreakerStore 熔断器仓储（治理四件套之熔断）。
// State 不持久化——由 handler 在读取后调用 EvaluateBreaker 即时填充。
type BreakerStore interface {
	// ListBreakers 熔断器列表（按 serviceID 过滤，空=全部），按 UpdatedAt 倒序。
	ListBreakers(ctx context.Context, serviceID string) ([]CircuitBreaker, error)
	// GetBreaker 读取单条（跨租户 not found）。
	GetBreaker(ctx context.Context, id string) (CircuitBreaker, error)
	// CreateBreaker 创建（租户内 Name 唯一）。
	CreateBreaker(ctx context.Context, b CircuitBreaker) (CircuitBreaker, error)
	// UpdateBreaker 更新（strategy/threshold/minRequests/windowSecs/enabled/serviceId）。
	UpdateBreaker(ctx context.Context, b CircuitBreaker) (CircuitBreaker, error)
	// DeleteBreaker 删除（跨租户 not found）。
	DeleteBreaker(ctx context.Context, id string) error
}
