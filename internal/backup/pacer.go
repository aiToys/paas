package backup

import (
	"context"
	"log"
	"time"

	"github.com/aitoys/paas/pkg/tenant"
)

// Pacer 周期观察非终态真实备份/恢复作业并推进状态（workload OperationPacer 同款模式：
// 状态机全部落库，core 重启后 ListActive 扫描自动恢复观察，无内存状态）。
//
// 推进规则：
//   - pending（Job 未建/被 GC）→ Start 重建（幂等）；Start 失败标 failed
//   - running → 维持（仅当库内还是 accepted 时推进到 running）
//   - succeeded → completed + sizeBytes/checksum + verifiedAt/finishedAt
//     （「校验和已记录」≠「可恢复验证通过」——spec 专项05 §4B 语义边界）
//   - failed → failed + error + finishedAt
//   - 超时（默认 15min，含 Job 自身 10min deadline 后的观察窗口）→ failed「观察超时（结果未知，产物可能仍在）」
type Pacer struct {
	repo     Repository
	executor Executor
	targets  TargetResolver
	restorer Restorer

	Interval time.Duration // 扫描间隔；零值 = 10s（applyDefaults，sweep 直调也生效——零值超时坑教训）
	Timeout  time.Duration // 单作业观察上限；零值 = 15min
}

// NewPacer 创建观察推进器。executor/targets 为 nil 时不推进（无 K8s 数据面场景）。
func NewPacer(repo Repository, ex Executor, t TargetResolver) *Pacer {
	return &Pacer{repo: repo, executor: ex, targets: t}
}

func (p *Pacer) WithRestorer(r Restorer) *Pacer { p.restorer = r; return p }

func (p *Pacer) applyDefaults() {
	if p.Interval <= 0 {
		p.Interval = 10 * time.Second
	}
	if p.Timeout <= 0 {
		p.Timeout = 15 * time.Minute
	}
}

// Run 阻塞循环（cmd/core goroutine 调用，ctx 退出收尾）。
func (p *Pacer) Run(ctx context.Context) {
	p.applyDefaults()
	t := time.NewTicker(p.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Sweep(context.WithoutCancel(ctx)) // 推进用独立 ctx：ticker 触发瞬间退出不丢本轮
		}
	}
}

// Sweep 单轮扫描（导出供测试直调）。applyDefaults 必须在此也调——直调路径
// （测试/未来手动触发）零值 Timeout 会致 `time.Since > 0` 恒真全标超时
// （workload OperationPacer 同款坑，2026-10-19 又踩一次）。
func (p *Pacer) Sweep(ctx context.Context) {
	p.applyDefaults()
	if p.repo == nil || p.executor == nil || p.targets == nil {
		return
	}
	actives, err := p.repo.ListActive(ctx)
	if err != nil {
		log.Printf("[backup-pacer] ListActive 失败: %v", err)
		return
	}
	for _, b := range actives {
		p.advance(ctx, b)
	}
}

func (p *Pacer) advance(ctx context.Context, b Backup) {
	// sweep ctx 无租户（Run 的根 ctx），repo 按租户过滤会 missing tenant 全拒——
	// 按记录自身 TenantID 补（workload OperationPacer 同款坑，SweepInterrupted 先例）。
	ctx = tenant.WithTenant(ctx, b.TenantID)
	// 观察超时（Job 10min deadline + 观察窗口，仍未终态判超时——结果未知如实标注）。
	if time.Since(b.CreatedAt) > p.Timeout {
		msg := "观察超时（结果未知，产物可能仍在；请核对数据服务与 PVC 后重试）"
		p.update(ctx, b, ProgressUpdate{
			Status: strPtr(StatusFailed), Error: strPtr(msg), FinishedAt: timePtr(time.Now()),
		})
		return
	}
	if b.Mode == ModeRestore {
		if b.RestoredResourceID == "" {
			p.update(ctx, b, ProgressUpdate{Status: strPtr(StatusFailed), Error: strPtr("旧恢复任务缺少确定目标，已阻止写入源实例"), FinishedAt: timePtr(time.Now())})
			return
		}
		if p.restorer != nil {
			id, err := p.restorer.CreateRestored(ctx, b.TenantID, b.ResourceID, b.RestoredResourceID, b.RestoreName)
			if err != nil || id != b.RestoredResourceID {
				message := "恢复目标创建失败；请检查配额与操作记录，不要重复创建"
				if err != nil {
					message += ": " + truncate(err.Error(), 160)
				}
				p.update(ctx, b, ProgressUpdate{Status: strPtr(StatusFailed), Error: strPtr(message), FinishedAt: timePtr(time.Now())})
				return
			}
		}
	}
	t, err := p.targets.ResolveTarget(ctx, b.TenantID, targetResourceID(b))
	if err != nil {
		p.update(ctx, b, ProgressUpdate{
			Status: strPtr(StatusFailed), Error: strPtr(truncate(err.Error(), 200)), FinishedAt: timePtr(time.Now()),
		})
		return
	}
	res, err := p.executor.Probe(ctx, b, t)
	if err != nil {
		log.Printf("[backup-pacer] Probe 失败 id=%s err=%v", b.ID, err) // 观察端故障不误标业务失败
		return
	}
	switch res.Phase {
	case "pending":
		if serr := p.executor.Start(ctx, b, t); serr != nil {
			p.update(ctx, b, ProgressUpdate{
				Status: strPtr(StatusFailed), Error: strPtr(truncate(serr.Error(), 200)), FinishedAt: timePtr(time.Now()),
			})
			return
		}
		if b.Status == StatusAccepted {
			p.update(ctx, b, ProgressUpdate{Status: strPtr(StatusRunning)})
		}
	case "running":
		if b.Status != StatusRunning {
			p.update(ctx, b, ProgressUpdate{Status: strPtr(StatusRunning)})
		}
	case "succeeded":
		if !ValidChecksum(res.Checksum) || (b.Mode == ModeSnapshot && res.SizeBytes <= 0) || (b.Mode == ModeRestore && res.Checksum != b.Checksum) {
			p.update(ctx, b, ProgressUpdate{Status: strPtr(StatusFailed), Error: strPtr("作业已结束但完整性校验证据缺失或不一致，不能判定成功"), FinishedAt: timePtr(time.Now())})
			return
		}
		now := time.Now()
		up := ProgressUpdate{Status: strPtr(StatusCompleted), FinishedAt: timePtr(now), VerifiedAt: timePtr(now), Checksum: strPtr(res.Checksum)}
		if b.Mode == ModeSnapshot {
			up.ArtifactRef = strPtr("file://" + backupsPVCName + "/" + dumpFile(b))
			up.SizeBytes = int64Ptr(res.SizeBytes)
		}
		p.update(ctx, b, up)
	case "failed":
		p.update(ctx, b, ProgressUpdate{
			Status: strPtr(StatusFailed), Error: strPtr(res.ErrMsg), FinishedAt: timePtr(time.Now()),
		})
	}
}

// targetResourceID 恢复作业的执行目标是新实例（RestoredResourceID），快照是源实例。
func targetResourceID(b Backup) string {
	if b.Mode == ModeRestore && b.RestoredResourceID != "" {
		return b.RestoredResourceID
	}
	return b.ResourceID
}

func (p *Pacer) update(ctx context.Context, b Backup, up ProgressUpdate) {
	if err := p.repo.UpdateProgress(ctx, b.TenantID, b.ID, up); err != nil {
		log.Printf("[backup-pacer] 推进失败 id=%s err=%v", b.ID, err)
	}
}

func strPtr(s string) *string        { return &s }
func timePtr(t time.Time) *time.Time { return &t }
