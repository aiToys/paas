package governance

import "context"

// AutoRegisterService 部署即注册：为 service 型工作负载自动注册治理服务。
// Name=工作负载名（== K8s Service 名 == 治理 Service 名，数据面发现靠此对齐，
// 见 CLAUDE.md 命名约定）；同应用下同名已存在则跳过（幂等，供创建 hook 与
// 启动自愈对账两处复用）。治理 Service 是纯注册元数据（实例真源=K8s Endpoints
// 自动发现），自动注册不下发任何 K8s 资源。port=0（无服务端口）不注册。
func AutoRegisterService(ctx context.Context, repo ServiceStore, s Service) error {
	if s.Port <= 0 {
		return nil
	}
	existing, err := repo.ListServices(ctx, s.EnvID, s.AppID)
	if err != nil {
		return err
	}
	for _, e := range existing {
		if e.Name == s.Name {
			return nil
		}
	}
	_, err = repo.CreateService(ctx, s)
	return err
}
