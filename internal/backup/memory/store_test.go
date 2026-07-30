package memory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/aitoys/paas/internal/backup"
)

func TestSeedBackup(t *testing.T) {
	s := NewStore()
	bs, err := s.List(context.Background(), "t-acme", "ds-db-acme")
	require.NoError(t, err)
	require.Len(t, bs, 1)
	assert.Equal(t, backup.TypeFull, bs[0].Type)
}

func TestBackupCRUD(t *testing.T) {
	s := NewStore()
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, backup.Backup{
		ID: "bk-2", TenantID: "t-acme", ResourceID: "ds-db-acme",
		Type: backup.TypeIncremental, Status: backup.StatusCompleted,
	}))
	bs, _ := s.List(ctx, "t-acme", "")
	assert.Len(t, bs, 2)
	require.NoError(t, s.Delete(ctx, "t-acme", "bk-2"))
}

func TestBackupTenantIsolation(t *testing.T) {
	s := NewStore()
	// t-globex 看不到 t-acme 备份
	bs, _ := s.List(context.Background(), "t-globex", "")
	assert.Empty(t, bs)
	// 跨租户删失败
	assert.Error(t, s.Delete(context.Background(), "t-globex", "bk-1"))
}
