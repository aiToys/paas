package backup

import (
	"strings"
	"testing"
)

// TestRestorableSemantics 锁定 admin 总览与 restore 端点共用的可恢复判定：
// record 恒不可恢复；snapshot 需 completed + 产物定位匹配 + 合法 sha256。
func TestRestorableSemantics(t *testing.T) {
	sum := strings.Repeat("a", 64)
	cases := []struct {
		name string
		b    Backup
		want bool
	}{
		{"record 模式无产物", Backup{ID: "bk-1", Mode: ModeRecord, Status: StatusCompleted}, false},
		{"存量空 mode 按 record 解释", Backup{ID: "bk-2", Status: StatusCompleted}, false},
		{"snapshot 完整证据", Backup{ID: "bk-3", Mode: ModeSnapshot, Status: StatusCompleted,
			ArtifactRef: "file://" + backupsPVCName + "/bk-3.dump", Checksum: sum}, true},
		{"snapshot 缺校验和", Backup{ID: "bk-4", Mode: ModeSnapshot, Status: StatusCompleted,
			ArtifactRef: "file://" + backupsPVCName + "/bk-4.dump"}, false},
		{"snapshot 未完成", Backup{ID: "bk-5", Mode: ModeSnapshot, Status: StatusRunning,
			ArtifactRef: "file://" + backupsPVCName + "/bk-5.dump", Checksum: sum}, false},
		{"snapshot 产物定位不匹配", Backup{ID: "bk-6", Mode: ModeSnapshot, Status: StatusCompleted,
			ArtifactRef: "file://" + backupsPVCName + "/other.dump", Checksum: sum}, false},
		{"restore 记录本身不可再恢复", Backup{ID: "bk-7", Mode: ModeRestore, Status: StatusCompleted,
			ArtifactRef: "file://" + backupsPVCName + "/bk-7.dump", Checksum: sum}, false},
	}
	for _, c := range cases {
		if got := c.b.Restorable(); got != c.want {
			t.Errorf("%s: Restorable=%v want %v", c.name, got, c.want)
		}
	}
}

// TestAdminRowsCarriesRestorable admin 视图行必须带 restorable 派生字段（前端据此禁用恢复入口）。
func TestAdminRowsCarriesRestorable(t *testing.T) {
	rows := AdminRows([]Backup{{ID: "r", Mode: ModeRecord, Status: StatusCompleted}})
	if len(rows) != 1 || rows[0].ID != "r" || rows[0].Restorable {
		t.Fatalf("unexpected rows: %+v", rows)
	}
}
