//go:build integration

package pg

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aitoys/paas/internal/backup"
)

func TestRestoreRequestPersistsAcrossReads(t *testing.T) {
	s := NewStore(newTestDB(t))
	b := backup.Backup{ID: "bk-r", TenantID: "t-acme", ResourceID: "ds-source", Type: "full", Mode: backup.ModeRestore,
		Status: backup.StatusAccepted, CreatedAt: time.Now(), SourceBackupID: "bk-source", RestoredResourceID: "ds-new",
		RestoreName: "restored-name", Checksum: strings.Repeat("a", 64), ArtifactRef: "file://paas-backups/bk-source.dump"}
	if err := s.Create(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(context.Background(), "t-acme", b.ID)
	if err != nil || got.RestoreName != b.RestoreName || got.Checksum != b.Checksum || got.RestoredResourceID != b.RestoredResourceID {
		t.Fatalf("恢复请求持久化不完整: %+v %v", got, err)
	}
	rows, err := s.ListActive(context.Background())
	if err != nil || len(rows) != 1 || rows[0].RestoreName != b.RestoreName {
		t.Fatalf("重启扫描丢失恢复参数: %+v %v", rows, err)
	}
}
