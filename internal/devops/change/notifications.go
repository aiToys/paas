// notifications.go 通知聚合（L1 站内通知）：实时扫描批次/runs/变更状态拼装「需要用户关注」事件。
//
// 设计：无持久化（YAGNI）——每次 GET 实时聚合当前态，已读状态存前端 localStorage。
// 事件源：
//   - 批次 conflict（集成冲突，error）
//   - 批次 testing/releasing（进行中，info）
//   - 批次 tested（测试通过待审批，warning）
//   - run failed（error）/ paused（等审批，warning）
package change

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// 通知类型。
const (
	NotifBatchConflict  = "batch_conflict"
	NotifBatchTesting   = "batch_testing"
	NotifBatchReleasing = "batch_releasing"
	NotifRunFailed      = "run_failed"
	NotifRunPaused      = "run_paused"
	NotifRunRunning     = "run_running"
	NotifBatchApprove   = "batch_approve"
	NotifAlertFiring    = "alert_firing"  // 告警触发（评估引擎 firing）
	NotifAlertPending   = "alert_pending" // 告警观察中（pending，未达持续窗口）
)

// NotifWindow 通知时间窗口：超过该时长的终态事件（failed run 等）不再进通知——
// 历史失败堆积会让值班台数字只涨不消（狼来了），窗口外自然退出。进行中/待审批类
// （testing/releasing/paused）状态本身会流转，不受窗口约束（按 CreatedAt 也天然新鲜）。
const NotifWindow = 7 * 24 * time.Hour

// inNotifWindow 判断事件时间是否在通知窗口内（解析失败保守放行，防漏报）。
func inNotifWindow(iso string, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return true
	}
	return now.Sub(t) <= NotifWindow
}

// Notification 单条通知（camelCase json，前端直取）。
type Notification struct {
	ID         string `json:"id"` // 稳定 ID（targetType:targetID[:status]，前端记已读用）
	Type       string `json:"type"`
	Severity   string `json:"severity"` // error|warning|info
	Title      string `json:"title"`
	AppID      string `json:"appId"`
	TargetType string `json:"targetType"` // batch|run|change（跳转目标类型）
	TargetID   string `json:"targetId"`
	At         string `json:"at"`
}

// RunStatusItem run 最小字段（RunLister 只暴露列表所需，避免 change→pipeline 全量依赖）。
type RunStatusItem struct {
	ID      string
	AppID   string
	Status  string // running|paused|succeeded|failed|aborted
	Current string // 当前 stage 名
	At      string // 创建时间 ISO（通知展示/排序用）
}

// RunLister 通知聚合对 run 的最小依赖（cmd/core 桥接 pipeline ListRuns）。
type RunLister interface {
	ListRunStatuses(ctx context.Context) ([]RunStatusItem, error)
}

// AlertItem 告警通知最小字段（AlertLister 返回，避免 change→observability import）。
type AlertItem struct {
	RuleName   string
	TargetType string // app|workload|env|dataservice
	TargetID   string
	MetricName string
	Severity   string // critical|warning
	Status     string // firing|pending
	At         string // ISO 时间（通知展示/排序用）
}

// AlertLister 通知聚合对告警的最小依赖（cmd/core 桥接 observability 评估引擎）。
type AlertLister interface {
	ListAlertItems(ctx context.Context) ([]AlertItem, error)
}

// Notifications 聚合通知（tenant 内，按 at 倒序）。
// alerts 可为 nil（未注入告警源时跳过，通知其余源不受影响）。
func Notifications(ctx context.Context, repo Repository, runs RunLister, alerts AlertLister) ([]Notification, error) {
	now := time.Now()
	var out []Notification
	batches, err := repo.ListBatches(ctx, "", "")
	if err != nil {
		return nil, err
	}
	for _, b := range batches {
		switch b.Status {
		case BatchConflict:
			// 冲突若长期未处理也退岀窗口（同 failed run 语义）
			if now.Sub(b.CreatedAt) > NotifWindow {
				continue
			}
			out = append(out, Notification{
				ID: "batch:" + b.ID + ":conflict", Type: NotifBatchConflict, Severity: "error",
				Title: fmt.Sprintf("批次「%s」集成冲突，需解决后重新集成", b.Title), AppID: b.AppID,
				TargetType: "batch", TargetID: b.ID, At: b.CreatedAt.Format(time.RFC3339),
			})
		case BatchTesting:
			out = append(out, Notification{
				ID: "batch:" + b.ID + ":testing", Type: NotifBatchTesting, Severity: "info",
				Title: fmt.Sprintf("批次「%s」集成测试进行中", b.Title), AppID: b.AppID,
				TargetType: "batch", TargetID: b.ID, At: b.CreatedAt.Format(time.RFC3339),
			})
		case BatchReleasing:
			out = append(out, Notification{
				ID: "batch:" + b.ID + ":releasing", Type: NotifBatchReleasing, Severity: "info",
				Title: fmt.Sprintf("批次「%s」正在发布", b.Title), AppID: b.AppID,
				TargetType: "batch", TargetID: b.ID, At: b.CreatedAt.Format(time.RFC3339),
			})
		case BatchTested:
			out = append(out, Notification{
				ID: "batch:" + b.ID + ":tested", Type: NotifBatchApprove, Severity: "warning",
				Title: fmt.Sprintf("批次「%s」测试通过，待审批发布", b.Title), AppID: b.AppID,
				TargetType: "batch", TargetID: b.ID, At: b.CreatedAt.Format(time.RFC3339),
			})
		}
	}

	// runs：failed / paused（含 approve 门禁等待）。
	// 租户过滤由 bridge 的 ListRuns ctx 过滤保证（runTriggerBridge → pipeline ListRuns
	// 强制 tenant_id）；不以批次归属应用作白名单——无批次应用的 failed/paused run 同样需通知。
	if runs != nil {
		items, err := runs.ListRunStatuses(ctx)
		if err == nil { // 读失败降级跳过（通知非关键路径）
			for _, r := range items {
				switch r.Status {
				case "failed":
					// 终态失败只通知窗口内（历史失败堆积退岀值班台）
					if !inNotifWindow(r.At, now) {
						continue
					}
					out = append(out, Notification{
						ID: "run:" + r.ID + ":failed", Type: NotifRunFailed, Severity: "error",
						Title: fmt.Sprintf("流水线运行失败（%s）", r.Current), AppID: r.AppID,
						TargetType: "run", TargetID: r.ID, At: r.At,
					})
				case "paused":
					out = append(out, Notification{
						ID: "run:" + r.ID + ":paused", Type: NotifRunPaused, Severity: "warning",
						Title: fmt.Sprintf("流水线等待审批（%s）", r.Current), AppID: r.AppID,
						TargetType: "run", TargetID: r.ID, At: r.At,
					})
				case "running":
					out = append(out, Notification{
						ID: "run:" + r.ID + ":running", Type: NotifRunRunning, Severity: "info",
						Title: fmt.Sprintf("流水线运行中（%s）", r.Current), AppID: r.AppID,
						TargetType: "run", TargetID: r.ID, At: r.At,
					})
				}
			}
		}
	}

	// 告警：firing（error/warning 按 severity）/ pending（info，提示观察中）。
	// resolved 不进通知（恢复无需打扰）；读失败降级跳过。
	if alerts != nil {
		if items, err := alerts.ListAlertItems(ctx); err == nil {
			for _, a := range items {
				switch a.Status {
				case "firing":
					sev := "warning"
					if a.Severity == "critical" {
						sev = "error"
					}
					out = append(out, Notification{
						ID:         "alert:" + a.RuleName + ":" + a.TargetType + ":" + a.TargetID + ":firing",
						Type:       NotifAlertFiring, Severity: sev,
						Title:      fmt.Sprintf("告警「%s」触发：%s %s=%s 超阈值（%s）", a.RuleName, a.TargetType, a.TargetID, a.MetricName, a.Status),
						TargetType: "alert", TargetID: a.TargetType, At: a.At,
					})
				case "pending":
					out = append(out, Notification{
						ID:         "alert:" + a.RuleName + ":" + a.TargetType + ":" + a.TargetID + ":pending",
						Type:       NotifAlertPending, Severity: "info",
						Title:      fmt.Sprintf("告警「%s」观察中：%s %s %s 接近阈值", a.RuleName, a.TargetType, a.TargetID, a.MetricName),
						TargetType: "alert", TargetID: a.TargetType, At: a.At,
					})
				}
			}
		}
	}

	// 排序：error > warning > info，同级按时间倒序（字符串比较 ISO 时间即倒序需先按 severity）
	sev := map[string]int{"error": 0, "warning": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool {
		if sev[out[i].Severity] != sev[out[j].Severity] {
			return sev[out[i].Severity] < sev[out[j].Severity]
		}
		return out[i].At > out[j].At
	})
	return out, nil
}
