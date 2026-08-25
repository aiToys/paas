// Package pg 实现 marketplace.Repository 的 PostgreSQL 持久化。
// 平台级公开（无 tenant 过滤）；同 entityType+name+publisher 唯一，重发布 ON CONFLICT 覆盖。
package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/ai/marketplace"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

type Store struct {
	db  *storagepg.DB
	seq atomic.Int64
}

func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

// 列顺序与 scan 对齐（列错位 panic 警示）。
const itemCols = `id, entity_type, name, description, category, snapshot, publisher_tenant, publisher_name, installs, created_at`

func (s *Store) newID() string {
	s.seq.Add(1)
	return fmt.Sprintf("mk-%d-%d", time.Now().UnixNano(), s.seq.Load())
}

func scanItem(r storagepg.RowScanner, it *marketplace.Item) error {
	var snap []byte
	if err := r.Scan(&it.ID, &it.EntityType, &it.Name, &it.Description, &it.Category, &snap,
		&it.PublisherTenant, &it.PublisherName, &it.Installs, &it.CreatedAt); err != nil {
		return err
	}
	it.Snapshot = append([]byte(nil), snap...)
	return nil
}

func (s *Store) List(ctx context.Context, entityType, category, q string) ([]marketplace.Item, error) {
	sb := strings.Builder{}
	sb.WriteString(`SELECT ` + itemCols + ` FROM marketplace_items WHERE 1=1`)
	args := []any{}
	if entityType != "" {
		args = append(args, entityType)
		sb.WriteString(fmt.Sprintf(` AND entity_type=$%d`, len(args)))
	}
	if category != "" {
		args = append(args, category)
		sb.WriteString(fmt.Sprintf(` AND category=$%d`, len(args)))
	}
	if kw := marketplace.NormalizeQuery(q); kw != "" {
		args = append(args, "%"+kw+"%")
		sb.WriteString(fmt.Sprintf(` AND LOWER(name || ' ' || description) LIKE $%d`, len(args)))
	}
	sb.WriteString(` ORDER BY installs DESC, created_at DESC LIMIT 1000`)
	rows, err := s.db.Pool().Query(ctx, sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]marketplace.Item, 0)
	for rows.Next() {
		var it marketplace.Item
		if err = scanItem(rows, &it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id string) (marketplace.Item, error) {
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+itemCols+` FROM marketplace_items WHERE id=$1`, id)
	var it marketplace.Item
	if err := scanItem(row, &it); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return marketplace.Item{}, marketplace.ErrItemNotFound
		}
		return marketplace.Item{}, err
	}
	return it, nil
}

func (s *Store) Create(ctx context.Context, in marketplace.Item) (marketplace.Item, error) {
	if err := in.Validate(); err != nil {
		return marketplace.Item{}, err
	}
	if in.ID == "" {
		in.ID = s.newID()
	}
	in.Installs = 0
	in.CreatedAt = time.Now()
	// upsert：同 entityType+name+publisher 覆盖（重发布；installs 重置、created_at 刷新）
	_, err := s.db.Pool().Exec(ctx,
		`INSERT INTO marketplace_items (`+itemCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		 ON CONFLICT (entity_type, name, publisher_tenant) DO UPDATE SET
		   id=EXCLUDED.id, description=EXCLUDED.description, category=EXCLUDED.category,
		   snapshot=EXCLUDED.snapshot, publisher_name=EXCLUDED.publisher_name,
		   installs=0, created_at=EXCLUDED.created_at`,
		in.ID, in.EntityType, in.Name, in.Description, in.Category, in.Snapshot,
		in.PublisherTenant, in.PublisherName, in.Installs, in.CreatedAt)
	if err != nil {
		return marketplace.Item{}, err
	}
	return in, nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	ct, err := s.db.Pool().Exec(ctx, `DELETE FROM marketplace_items WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return marketplace.ErrItemNotFound
	}
	return nil
}

func (s *Store) IncInstalls(ctx context.Context, id string) error {
	ct, err := s.db.Pool().Exec(ctx,
		`UPDATE marketplace_items SET installs=installs+1 WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return marketplace.ErrItemNotFound
	}
	return nil
}

func (s *Store) ListByPublisher(ctx context.Context, tenantID string) ([]marketplace.Item, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+itemCols+` FROM marketplace_items WHERE publisher_tenant=$1 ORDER BY installs DESC, created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]marketplace.Item, 0)
	for rows.Next() {
		var it marketplace.Item
		if err = scanItem(rows, &it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) ListAll(ctx context.Context) ([]marketplace.Item, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+itemCols+` FROM marketplace_items ORDER BY installs DESC, created_at DESC LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]marketplace.Item, 0)
	for rows.Next() {
		var it marketplace.Item
		if err = scanItem(rows, &it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
