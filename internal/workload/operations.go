// Package workload —— 运维操作记录与可恢复推进（spec 2026-10-17 §6）。
//
// 滚动重启/实例重建是持久化期望状态驱动的异步操作：HTTP 202 只表示受理，
// 最终完成由 OperationPacer 周期观察数据面推进。core 重启后扫描非终态自动恢复观察
// （操作与期望状态都在库里，不依赖进程内存）。
package workload

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aitoys/paas/pkg/tenant"
)

// 操作种类与状态机。
const (
	OpKindRestart  = "restart"
	OpKindRecreate = "recreate"

	OpStatusAccepted    = "accepted"    // 已受理（期望状态已持久化）
	OpStatusReconciling = "reconciling" // 数据面已看到新期望（CRD/Deployment 带 token）
	OpStatusRunning     = "running"     // 滚动/替代进行中
	OpStatusSucceeded   = "succeeded"   // 已确认完成（按 token/UID 观察达标）
	OpStatusFailed      = "failed"      // 失败（含超时）
	OpStatusSuperseded  = "superseded"  // 被更新的发布覆盖（不是失败也不是成功）
	OpStatusUnknown     = "unknown"     // 观察超时/无法确认，保留人工核实入口

	// supersededGracePeriod：受理后「最新 RS token 不匹配」的判定宽限——投影链
	// （CRD Apply → reconciler → Deployment 新 RS）有秒级窗口，宽限期内视为未生效
	// 而非被覆盖，防止刚受理的重启被误杀成 superseded 终态。
	supersededGracePeriod = 30 * time.Second
)

// OpActiveStates 是非终态集合（同一 workload 同时只允许一个活跃操作）。
var OpActiveStates = []string{OpStatusAccepted, OpStatusReconciling, OpStatusRunning}

// Operation 是一次运维操作的持久化记录（workload_operations 表）。
type Operation struct {
	ID             string     `json:"id"`
	TenantID       string     `json:"tenantId,omitempty"`
	WorkloadID     string     `json:"workloadId"`
	Kind           string     `json:"kind"` // restart|recreate
	Status         string     `json:"status"`
	Reason         string     `json:"reason,omitempty"` // 操作者填写的原因
	Actor          string     `json:"actor,omitempty"`
	IdempotencyKey string     `json:"idempotencyKey,omitempty"`
	TargetToken    string     `json:"targetToken,omitempty"` // restart：目标 restartToken
	PodUID         string     `json:"podUid,omitempty"`      // recreate：被驱逐的旧 Pod UID
	PodName        string     `json:"podName,omitempty"`
	Replicas       int        `json:"replicas,omitempty"` // 受理时副本快照（并发扩缩容检测）
	Evidence       string     `json:"evidence,omitempty"` // 观察证据摘要（阶段/revision/退出详情）
	AcceptedAt     time.Time  `json:"acceptedAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
	FinishedAt     *time.Time `json:"finishedAt,omitempty"`
}

// 语义化错误：handler 映射 409（冲突）/429（PDB）/503（能力不可用）。
var (
	ErrOpConflict   = errors.New("该工作负载已有进行中的运维操作")
	ErrUIDMismatch  = errors.New("实例已变化（UID 不匹配），请刷新后重试")
	ErrTooManyEvict = errors.New("驱逐被 PodDisruptionBudget 阻断")
	ErrNotManaged   = errors.New("该实例不由平台托管控制器管理，不支持重建")
)

// OperationsRepo 是运维操作记录的持久化抽象（PG/memory 双实现）。
// 所有方法从 ctx 取租户强制过滤；跨租户统一 not found。
type OperationsRepo interface {
	// CreateRestart 受理滚动重启：同一事务写 workloads.restart_token 与操作记录（accepted）。
	// 幂等：IdempotencyKey 命中既有记录时返回该记录与 created=false（不重复受理/投影）。
	// workload 已有活跃操作时返回 ErrOpConflict。
	CreateRestart(ctx context.Context, w Workload, reason, actor, idemKey string) (op Operation, created bool, err error)
	// CreateRecreate 受理实例重建（记录操作 + 快照副本数）。幂等语义同上。
	CreateRecreate(ctx context.Context, w Workload, podName, podUID, reason, actor, idemKey string) (op Operation, created bool, err error)
	Get(ctx context.Context, workloadID, operationID string) (Operation, error)
	// ListByWorkload 按时间倒序分页（limit<=0 默认 50）。
	ListByWorkload(ctx context.Context, workloadID string, limit, offset int) ([]Operation, error)
	// Active 返回该 workload 当前活跃操作（0 或 1 条）；跨租户/无操作返回零值。
	Active(ctx context.Context, workloadID string) (Operation, error)
	// ListActive 跨租户列出全部非终态操作（Pacer 恢复扫描；管理面专用，不带租户过滤）。
	ListActive(ctx context.Context) ([]Operation, error)
	// UpdateStatus 推进状态（写 Evidence/FinishedAt）。终态幂等：已是终态时忽略非终态写入。
	UpdateStatus(ctx context.Context, op Operation) error
}

// ApplyOperations 装饰 OperationsRepo：受理成功（created）后投影数据面——
// restart 的 token 经 Applier.Apply 落 CRD spec.restartToken，reconciler 据此滚动。
// 投影失败仅记日志：期望状态已在库，Pacer 观察按 notfound 保持受理态，
// 修复投影链路（reconcileWorkloadDrift 启动扫 + 手动 Apply）后自然续上，不丢操作。
type ApplyOperations struct {
	OperationsRepo
	applier Applier
}

// NewApplyOperations 包装 inner（applier nil 时透传，无数据面投影）。
func NewApplyOperations(inner OperationsRepo, a Applier) *ApplyOperations {
	return &ApplyOperations{OperationsRepo: inner, applier: a}
}

func (o *ApplyOperations) CreateRestart(ctx context.Context, w Workload, reason, actor, idemKey string) (Operation, bool, error) {
	op, created, err := o.OperationsRepo.CreateRestart(ctx, w, reason, actor, idemKey)
	if err != nil || !created {
		return op, created, err
	}
	if o.applier != nil {
		w2 := withTenant(ctx, w)
		w2.RestartToken = op.TargetToken
		if aerr := o.applier.Apply(ctx, w2); aerr != nil {
			log.Printf("[workload-applier] 数据面投影失败 id=%s op=restart err=%v", w.ID, aerr)
		}
	}
	return op, created, nil
}

// OperationPacer 周期推进非终态操作：观察数据面 → 更新状态。
// 恢复语义：每 tick 从库扫非终态（core 重启后自动续观察），无内存状态。
type OperationPacer struct {
	Ops      OperationsRepo
	Repo     Repository    // 取 workload（token/类型校验）
	Reader   RuntimeReader // 数据面观察
	Interval time.Duration // 扫描周期（默认 10s）
	Timeout  time.Duration // 受理起观察超时（默认 10min，超时标 unknown）
}

// Run 阻塞循环至 ctx 取消（cmd/core 里 go 起）。
func (p *OperationPacer) Run(ctx context.Context) {
	p.applyDefaults()
	t := time.NewTicker(p.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.sweep(ctx)
		}
	}
}

// applyDefaults 补齐 Interval/Timeout 默认值（10s / 10min）。
func (p *OperationPacer) applyDefaults() {
	if p.Interval <= 0 {
		p.Interval = 10 * time.Second
	}
	if p.Timeout <= 0 {
		p.Timeout = 10 * time.Minute
	}
}

// sweep 扫一轮非终态操作并推进。单条失败记日志继续（下轮重试）。
func (p *OperationPacer) sweep(ctx context.Context) {
	p.applyDefaults() // 直接调 sweep（测试/恢复路径）也持正确默认值，避免零值超时恒真
	ops, err := p.Ops.ListActive(ctx)
	if err != nil {
		log.Printf("[op-pacer] 扫描非终态操作失败: %v", err)
		return
	}
	for _, op := range ops {
		opCtx := withOpTenant(ctx, op)
		if err := p.advance(opCtx, op); err != nil {
			log.Printf("[op-pacer] 推进失败 id=%s kind=%s status=%s err=%v", op.ID, op.Kind, op.Status, err)
		}
	}
}

// advance 推进单个操作的状态机。
func (p *OperationPacer) advance(ctx context.Context, op Operation) error {
	// 超时：accepted 起超过 Timeout 仍非终态 → unknown（保留人工核实，不谎报成功/失败）。
	if time.Since(op.AcceptedAt) > p.Timeout {
		op.Status = OpStatusUnknown
		op.Evidence = appendEvidence(op.Evidence, "观察超时，结果待人工核实")
		now := time.Now()
		op.FinishedAt = &now
		return p.Ops.UpdateStatus(ctx, op)
	}
	switch op.Kind {
	case OpKindRestart:
		return p.advanceRestart(ctx, op)
	case OpKindRecreate:
		return p.advanceRecreate(ctx, op)
	default:
		return fmt.Errorf("未知操作类型 %q", op.Kind)
	}
}

// advanceRestart：accepted → reconciling → running → succeeded|superseded。
func (p *OperationPacer) advanceRestart(ctx context.Context, op Operation) error {
	phase, detail, err := p.Reader.RestartProgress(ctx, op.WorkloadID, op.TargetToken)
	if err != nil {
		return err // 观察失败本轮跳过（下轮重试），不改变状态
	}
	switch phase {
	case "completed":
		op.Status = OpStatusSucceeded
		op.Evidence = appendEvidence(op.Evidence, "滚动完成："+detail)
		now := time.Now()
		op.FinishedAt = &now
	case "superseded":
		// 受理初期投影尚未生效（受理同步 Apply CRD → reconciler → Deployment 有秒级
		// 窗口），此时「最新 RS token 不匹配」是旧版本残留而非真被覆盖——宽限期内
		// 不终态化，等投影生效后下轮再判。
		if op.Status == OpStatusAccepted && time.Since(op.AcceptedAt) < supersededGracePeriod {
			return nil
		}
		op.Status = OpStatusSuperseded
		op.Evidence = appendEvidence(op.Evidence, "被更新的发布覆盖："+detail)
		now := time.Now()
		op.FinishedAt = &now
	case "running":
		if op.Status == OpStatusAccepted || op.Status == OpStatusReconciling {
			op.Status = OpStatusRunning
		}
		op.Evidence = appendEvidence(op.Evidence, detail)
	case "reconciling":
		fallthrough
	case "progressing", "notfound":
		if op.Status == OpStatusAccepted {
			op.Status = OpStatusReconciling
		}
	default:
		return nil // 未知 phase 保守跳过
	}
	return p.Ops.UpdateStatus(ctx, op)
}

// advanceRecreate：观察旧 UID 退场 + 新 Pod 就绪 → succeeded。
func (p *OperationPacer) advanceRecreate(ctx context.Context, op Operation) error {
	phase, detail, err := p.Reader.RecreateProgress(ctx, op.WorkloadID, op.PodUID)
	if err != nil {
		return err
	}
	switch phase {
	case "completed":
		op.Status = OpStatusSucceeded
		op.Evidence = appendEvidence(op.Evidence, "替代实例就绪："+detail)
		now := time.Now()
		op.FinishedAt = &now
	case "progressing":
		if op.Status == OpStatusAccepted {
			op.Status = OpStatusRunning
		}
		op.Evidence = appendEvidence(op.Evidence, detail)
	}
	return p.Ops.UpdateStatus(ctx, op)
}

// appendEvidence 追加证据行（去重护栏：同文本不重复追加，防 Pacer 每 tick 写放大）。
func appendEvidence(cur, add string) string {
	if add == "" || strings.Contains(cur, add) {
		return cur
	}
	if cur == "" {
		return add
	}
	return cur + "\n" + add
}

// withOpTenant 把操作的租户写进 ctx（ListActive 是跨租户管理查询，观察须回到资源租户上下文）。
func withOpTenant(ctx context.Context, op Operation) context.Context {
	if op.TenantID == "" {
		return ctx
	}
	return tenant.WithTenant(ctx, op.TenantID)
}
