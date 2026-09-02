package controller

import (
	"context"
	"log"
	"time"

	"github.com/aitoys/paas/api/core/v1alpha1"
)

// PlatformWorkloadLookup 平台侧 workload 查找接口（依赖倒置，破除 controller→workload 业务包依赖，
// 与 AppConfigLookup 同款装配模式，桥接在 cmd/core）。
type PlatformWorkloadLookup interface {
	// Exists 判断平台侧是否存在指定 ID 的 workload（PG/memory 透明）。
	Exists(ctx context.Context, id string) (bool, error)
}

// orphanGracePeriod 孤儿 CR 回收宽限期：CR 创建/平台删除事件可能早于仓库可查
// （如 core 启动竞态、PG 短暂不可用），立即删除会误杀刚建的工作负载。
// 宽限期内不删，超期仍未在平台侧注册才回收。
const orphanGracePeriod = 10 * time.Minute

// collectOrphans 全命名空间扫描 Workload CR，回收平台侧已不存在的孤儿。
// 每个周期重新全量拉平台 ID 集合（ID 集 O(千)，内存开销可忽略），
// 避免维护增量状态（KISS：无需精确追踪"何时从平台删除"）。
//
// 回收语义：删 CR 本体，ownerRef 让 K8s GC 级联清 Deployment/Job/CronJob/Service/Ingress。
// 宽限期判定用 CR 的 creationTimestamp 近似「平台删除时间」——不精确但方向保守：
// 刚建的 CR 一定不会立刻被回收，存活超宽限期且平台查无此 ID 的 CR 才是孤儿。
//
// Platform nil（本地 dev 无 stores）时直接跳过，不误删。
func (r *WorkloadReconciler) collectOrphans(ctx context.Context) {
	if r.Platform == nil {
		return
	}
	var list v1alpha1.WorkloadList
	if err := r.List(ctx, &list); err != nil {
		log.Printf("[orphan-gc] 列 Workload CR 失败（跳过本轮）: %v", err)
		return
	}
	if len(list.Items) == 0 {
		return
	}
	var orphans []*v1alpha1.Workload
	for i := range list.Items {
		w := &list.Items[i]
		exists, err := r.Platform.Exists(ctx, w.Name)
		if err != nil {
			// 查询失败（如 PG 抖动）：本轮跳过该 CR，宁可晚删不可误删。
			log.Printf("[orphan-gc] 查平台 workload 失败（跳过）id=%s: %v", w.Name, err)
			continue
		}
		if !exists && time.Since(w.CreationTimestamp.Time) > orphanGracePeriod {
			orphans = append(orphans, w)
		}
	}
	for _, w := range orphans {
		if err := r.Delete(ctx, w); err != nil { //nolint:staticcheck // r.Delete 嵌入 client.Client 的 Delete(ctx, obj)
			log.Printf("[orphan-gc] 删孤儿 CR 失败 id=%s ns=%s: %v", w.Name, w.Namespace, err)
			continue
		}
		log.Printf("[orphan-gc] 回收孤儿 workload CR id=%s ns=%s（平台侧不存在，级联清 Deployment/Service）", w.Name, w.Namespace)
	}
}

// StartOrphanGC 启动孤儿回收循环（周期全量扫描）。ctx cancel 时退出。
// 周期取宽限期的 1/3：最坏情况下孤儿多活一个周期，可接受。
// 由 cmd/core 在 Platform 注入后调用（导出方法，包外装配）。
func (r *WorkloadReconciler) StartOrphanGC(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(orphanGracePeriod / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.collectOrphans(ctx)
			}
		}
	}()
}
