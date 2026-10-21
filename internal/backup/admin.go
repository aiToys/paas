package backup

// AdminBackupRow 是 admin 平台总览的备份/恢复行：在租户侧原始记录上补「能否作为恢复源」。
//
// 语义边界（spec 2026-10-19 §D4）：
//   - Restorable=true 只表示平台当前会受理该记录的恢复请求，不证明恢复曾成功；
//   - record 模式恒 false（无产物无恢复证明）；
//   - snapshot 的 VerifiedAt 只是校验和记录时间，与「恢复演练通过」无关。
type AdminBackupRow struct {
	Backup
	Restorable bool `json:"restorable"`
}

// AdminRows 把跨租户记录转成 admin 视图行（可恢复判定与 restore 端点同源）。
func AdminRows(list []Backup) []AdminBackupRow {
	out := make([]AdminBackupRow, len(list))
	for i, b := range list {
		out[i] = AdminBackupRow{Backup: b, Restorable: b.Restorable()}
	}
	return out
}
