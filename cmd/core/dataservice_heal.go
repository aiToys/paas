package main

// 数据服务幽灵自愈周期对账（2026-10-29 立项）：平台 DB 记录在而 K8s CR/STS 全没了时，
// 此前无任何自动恢复路径（遗留债务两度发作：2026-10-19 九例、2026-10-29 九例——
// 全部依赖手工 GET+PUT 触发重 Apply 才恢复，且依赖服务因连接串指向不存在的 FQDN 连锁 crash）。
// 编排放装配层（与 backfillGovernanceServices / OrphanGC 同风格）；CR 重建判定在
// DataServiceK8sApplier.EnsureCR（存在即跳过，防周期 Apply 引发 STS 滚动）。

import (
	"context"
	"log"
	"time"

	"github.com/aitoys/paas/internal/controller"
	"github.com/aitoys/paas/internal/dataservice"
	"github.com/aitoys/paas/internal/safego"
)

// dsHealInterval 自愈对账周期。取 5min：远小于人工发现故障的时延，又不会与
// OrphanGC（周期 3m20s、只删平台不存在的 CR）形成建删互搏——两者判定同源
// （平台记录），heal 只建平台有的，OrphanGC 只删平台没有的。
const dsHealInterval = 5 * time.Minute

// startDataserviceHeal 启动数据服务幽灵自愈循环（ctx cancel 退出）。applier 非
// DataServiceK8sApplier（本地 dev 无 K8s）时不启动。ListAll 失败本轮跳过（宁可晚愈）。
func startDataserviceHeal(ctx context.Context, repo dataservice.Repository, a dataservice.Applier) {
	ensurer, ok := a.(*controller.DataServiceK8sApplier)
	if !ok || ensurer == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(dsHealInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				safego.Loop("ds-heal:reconcile", func() { healDataservices(ctx, repo, ensurer) })
			}
		}
	}()
}

// healDataservices 单轮：跨租户全量对账，CR 缺失的托管实例重投影。
func healDataservices(ctx context.Context, repo dataservice.Repository, ensurer *controller.DataServiceK8sApplier) {
	all, err := repo.ListAll(ctx)
	if err != nil {
		log.Printf("[ds-heal] 列平台数据服务失败（跳过本轮）: %v", err)
		return
	}
	for _, d := range all {
		// 占位 Kind（EngineOf 解析不出引擎、也无自定义镜像）不拉起——reconciler 对
		// 无镜像 CR 会 markFailed，周期重投影只会制造日志噪声（语义与创建路径一致）。
		if dataservice.EngineOf(d) == "" && d.Image == "" {
			continue
		}
		healed, err := ensurer.EnsureCR(ctx, d)
		if err != nil {
			log.Printf("[ds-heal] 重投影失败 id=%s ns=%s: %v", d.ID, d.TenantID, err)
			continue
		}
		if healed {
			log.Printf("[ds-heal] 平台记录存在但 CR 缺失，已重投影 id=%s ns=%s（reconciler 将重建 STS/Svc）", d.ID, d.TenantID)
		}
	}
}
