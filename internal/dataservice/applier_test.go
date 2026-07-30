package dataservice

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRepo 最小 Repository 实现（测试专用）。
type fakeRepo struct {
	saved   DataService
	deleted string
}

func (f *fakeRepo) List(_ context.Context, _ string) ([]DataService, error) { return nil, nil }
func (f *fakeRepo) Get(_ context.Context, _ string) (DataService, error)     { return DataService{}, nil }
func (f *fakeRepo) Create(_ context.Context, d DataService) (DataService, error) {
	f.saved = d
	return d, nil
}
func (f *fakeRepo) Update(_ context.Context, d DataService) (DataService, error) {
	f.saved = d
	return d, nil
}
func (f *fakeRepo) Delete(_ context.Context, id string) error {
	f.deleted = id
	return nil
}

type fakeApplier struct {
	applied  DataService
	deleted  string
	calledN  int
}

func (a *fakeApplier) Apply(_ context.Context, d DataService) error { a.applied = d; a.calledN++; return nil }
func (a *fakeApplier) Delete(_ context.Context, id string) error    { a.deleted = id; a.calledN++; return nil }

func TestApplyRepoDecoratesCreate(t *testing.T) {
	fr := &fakeRepo{}
	ap := &fakeApplier{}
	repo := NewApplyRepo(fr, ap)
	_, err := repo.Create(context.Background(), DataService{ID: "ds-1", Name: "pg"})
	require.NoError(t, err)
	assert.Equal(t, "ds-1", ap.applied.ID, "Create 后应投影数据面")
	assert.Equal(t, "ds-1", fr.saved.ID)
}

func TestApplyRepoDecoratesDelete(t *testing.T) {
	fr := &fakeRepo{}
	ap := &fakeApplier{}
	repo := NewApplyRepo(fr, ap)
	require.NoError(t, repo.Delete(context.Background(), "ds-1"))
	assert.Equal(t, "ds-1", ap.deleted, "Delete 后应投影数据面")
}

func TestApplyRepoNilTransparent(t *testing.T) {
	fr := &fakeRepo{}
	repo := NewApplyRepo(fr, nil) // applier=nil
	_, err := repo.Create(context.Background(), DataService{ID: "ds-1"})
	require.NoError(t, err) // nil applier 不崩溃
	assert.Equal(t, "ds-1", fr.saved.ID)
}
