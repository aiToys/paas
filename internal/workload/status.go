package workload

import "context"

// StatusReader 从数据面（K8s）回填工作负载的实际运行状态（Ready/Status）。
//
// 背景：reconciler 把实际状态回写到 CRD status（K8s 资源），但前端 /api/workloads
// 读的是 Repository（PG/memory store）-- store 的 Ready/Status 是创建时写入的静态值，
// 永不与 K8s 同步，导致前端展示假态（如 seed 写 Ready=2/running，实际 Pod 可能未起）。
// StatusReader 在 handler 读路径（List）批量回填真实状态，解耦 store（期望状态真源）
// 与 K8s（实际状态真源）。
//
// 依赖倒置：workload 不直接依赖 K8s，由 cmd/core 注入 controller.K8sStatusReader。
// 未注入（nil）时 handler 透传 store 原值（降级：纯内存模式无真实状态，保持演示态）。
type StatusReader interface {
	// FillStatus 批量回填 Ready/Status：按工作负载 ID 匹配 K8s 资源实际状态原地修改。
	// 无 K8s / 无租户上下文 / 无匹配资源时保持原值（不报错，降级）。
	FillStatus(ctx context.Context, wls []Workload) error
}
