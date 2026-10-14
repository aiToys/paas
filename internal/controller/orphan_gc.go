package controller

import (
	"context"
	"log"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/aitoys/paas/api/core/v1alpha1"
	"github.com/aitoys/paas/internal/safego"
)

// PlatformWorkloadLookup 平台侧 workload 查找接口（依赖倒置，破除 controller→workload 业务包依赖，
// 与 AppConfigLookup 同款装配模式，桥接在 cmd/core）。
type PlatformWorkloadLookup interface {
	// Exists 判断平台侧是否存在指定 ID 的 workload（PG/memory 透明）。
	Exists(ctx context.Context, id string) (bool, error)
}

// PlatformExistsFunc 平台侧存在性判定（依赖倒置）：孤儿 CR 回收用，workload/dataservice 各自
// 桥接（ListAll 遍历）。CR 的 name 即平台实体 ID（wl-<ts> / ds-<ts>-<n>）。
type PlatformExistsFunc func(ctx context.Context, id string) (bool, error)

// orphanGracePeriod 孤儿 CR 回收宽限期：CR 创建/平台删除事件可能早于仓库可查
// （如 core 启动竞态、PG 短暂不可用），立即删除会误杀刚建的工作负载。
// 宽限期内不删，超期仍未在平台侧注册才回收。
const orphanGracePeriod = 10 * time.Minute

// collectOrphanCRs 通用孤儿回收主体：全命名空间扫描 list 指定类型的 CR，回收平台侧
// 已不存在的孤儿。workload 与 dataservice 共用（2026-09-25 实测 9 个孤儿 ds CR/STS 残留后
// 从 workload 专用泛化而来）。tag 仅用于日志前缀。
//
// 每个周期重新全量拉平台 ID 集合（ID 集 O(千)，内存开销可忽略），
// 避免维护增量状态（KISS：无需精确追踪"何时从平台删除"）。
//
// 回收语义：删 CR 本体，ownerRef 让 K8s GC 级联清子资源（Deployment/Job/CronJob/Service/
// Ingress 或 StatefulSet/Service/Secret）。宽限期判定用 CR 的 creationTimestamp 近似
// 「平台删除时间」——不精确但方向保守：刚建的 CR 一定不会立刻被回收，存活超宽限期且
// 平台查无此 ID 的 CR 才是孤儿。
func collectOrphanCRs(ctx context.Context, c client.Client, list client.ObjectList, tag string, exists PlatformExistsFunc) {
	if err := c.List(ctx, list); err != nil {
		log.Printf("[orphan-gc] 列 %s CR 失败（跳过本轮）: %v", tag, err)
		return
	}
	_ = meta.EachListItem(list, func(obj runtime.Object) error {
		mo, ok := obj.(metav1.Object)
		co, okObj := obj.(client.Object)
		if !ok || !okObj {
			return nil
		}
		if time.Since(mo.GetCreationTimestamp().Time) <= orphanGracePeriod {
			return nil
		}
		found, err := exists(ctx, mo.GetName())
		if err != nil {
			// 查询失败（如 PG 抖动）：本轮跳过该 CR，宁可晚删不可误删。
			log.Printf("[orphan-gc] 查平台 %s 失败（跳过）id=%s: %v", tag, mo.GetName(), err)
			return nil
		}
		if found {
			return nil
		}
		if err := c.Delete(ctx, co); err != nil {
			log.Printf("[orphan-gc] 删孤儿 CR 失败 id=%s ns=%s: %v", mo.GetName(), mo.GetNamespace(), err)
			return nil
		}
		log.Printf("[orphan-gc] 回收孤儿 %s CR id=%s ns=%s（平台侧不存在，级联清子资源）", tag, mo.GetName(), mo.GetNamespace())
		return nil
	})
}

// collectOrphans 全命名空间扫描 Workload CR，回收平台侧已不存在的孤儿（薄壳，主体见 collectOrphanCRs）。
// Platform nil（本地 dev 无 stores）时直接跳过，不误删。
func (r *WorkloadReconciler) collectOrphans(ctx context.Context) {
	if r.Platform == nil {
		return
	}
	collectOrphanCRs(ctx, r.Client, &v1alpha1.WorkloadList{}, "workload", r.Platform.Exists)
}

// StartOrphanGC 启动孤儿回收循环（周期全量扫描）。ctx cancel 时退出。
// 周期取宽限期的 1/3：最坏情况下孤儿多活一个周期，可接受。
// 单轮扫描经 safego.Loop 防 panic（R5 审计 I-3）。由 cmd/core 在 Platform 注入后调用（导出方法，包外装配）。
func (r *WorkloadReconciler) StartOrphanGC(ctx context.Context) {
	startOrphanGCLoop(ctx, "orphan-gc:collect", func() { r.collectOrphans(ctx) })
}

// collectOrphans 全命名空间扫描 DataService CR，回收平台侧已不存在的孤儿（与 workload 同款；
// 2026-09-25 补齐——dataservice 曾无此机制，平台侧删除失败/重启窗口后 CR+STS+PVC 永久残留，
// 一次对账即发现 9 个孤儿实例白耗资源并污染监控数据）。
// PlatformExists nil（本地 dev 无 stores）时直接跳过，不误删。
func (r *DataServiceReconciler) collectOrphans(ctx context.Context) {
	if r.PlatformExists == nil {
		return
	}
	collectOrphanCRs(ctx, r.Client, &v1alpha1.DataServiceList{}, "dataservice", r.PlatformExists)
}

// StartOrphanGC 启动数据服务孤儿回收循环（与 WorkloadReconciler.StartOrphanGC 同款周期与防护）。
// 由 cmd/core 在 PlatformExists 注入后调用。
func (r *DataServiceReconciler) StartOrphanGC(ctx context.Context) {
	startOrphanGCLoop(ctx, "ds-orphan-gc:collect", func() { r.collectOrphans(ctx) })
}

// startOrphanGCLoop 通用回收循环骨架：周期取宽限期的 1/3，ctx cancel 退出，单轮 safego.Loop 防 panic。
func startOrphanGCLoop(ctx context.Context, name string, collect func()) {
	go func() {
		ticker := time.NewTicker(orphanGracePeriod / 3)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				safego.Loop(name, collect)
			}
		}
	}()
}
