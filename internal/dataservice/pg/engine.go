package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/dataservice"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Engine PG 实现（平台级，无 tenant）。connection 用 JSONB map（复用 unmarshalSpec nil 安全处理）。
// 列名 ord（避免 SQL 保留字 order）。

const engineCols = `id, kind, engine, label, description, mode, enabled, connection, ord`

// scanEngine 读引擎行（Store 方法：cipher 非空时解密 connection 敏感字段）。
func (s *Store) scanEngine(r storagepg.RowScanner, e *dataservice.Engine) error {
	var connRaw []byte
	if err := r.Scan(&e.ID, &e.Kind, &e.Engine, &e.Label, &e.Description, &e.Mode, &e.Enabled, &connRaw, &e.Order); err != nil {
		return err
	}
	e.Connection = decryptConnection(s.cipher, unmarshalSpec(connRaw))
	return nil
}

func (s *Store) ListEngines(ctx context.Context) ([]dataservice.Engine, error) {
	rows, err := s.db.Pool().Query(ctx, `SELECT `+engineCols+` FROM engines ORDER BY ord, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]dataservice.Engine, 0)
	for rows.Next() {
		var e dataservice.Engine
		if err = s.scanEngine(rows, &e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) GetEngine(ctx context.Context, id string) (dataservice.Engine, error) {
	row := s.db.Pool().QueryRow(ctx, `SELECT `+engineCols+` FROM engines WHERE id=$1`, id)
	var e dataservice.Engine
	if err := s.scanEngine(row, &e); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dataservice.Engine{}, fmt.Errorf("引擎不存在: %s", id)
		}
		return dataservice.Engine{}, err
	}
	return e, nil
}

func (s *Store) CreateEngine(ctx context.Context, e dataservice.Engine) (dataservice.Engine, error) {
	if err := e.Validate(); err != nil {
		return dataservice.Engine{}, err
	}
	connBytes, err := marshalSpec(encryptConnection(s.cipher, e.Connection))
	if err != nil {
		return dataservice.Engine{}, err
	}
	row := s.db.Pool().QueryRow(ctx, `
INSERT INTO engines (id, kind, engine, label, description, mode, enabled, connection, ord)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING `+engineCols,
		e.ID, e.Kind, e.Engine, e.Label, e.Description, e.Mode, e.Enabled, connBytes, e.Order,
	)
	var saved dataservice.Engine
	if err = s.scanEngine(row, &saved); err != nil {
		if storagepg.IsUniqueViolation(err) {
			return dataservice.Engine{}, storagepg.FormatExists("引擎")
		}
		return dataservice.Engine{}, err
	}
	return saved, nil
}

func (s *Store) UpdateEngine(ctx context.Context, e dataservice.Engine) (dataservice.Engine, error) {
	if err := e.Validate(); err != nil {
		return dataservice.Engine{}, err
	}
	connBytes, err := json.Marshal(encryptConnection(s.cipher, e.Connection))
	if err != nil {
		return dataservice.Engine{}, err
	}
	row := s.db.Pool().QueryRow(ctx, `
UPDATE engines SET kind=$1, engine=$2, label=$3, description=$4, mode=$5, enabled=$6, connection=$7, ord=$8
WHERE id=$9 RETURNING `+engineCols,
		e.Kind, e.Engine, e.Label, e.Description, e.Mode, e.Enabled, connBytes, e.Order, e.ID,
	)
	var saved dataservice.Engine
	if err = s.scanEngine(row, &saved); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dataservice.Engine{}, fmt.Errorf("引擎不存在: %s", e.ID)
		}
		return dataservice.Engine{}, err
	}
	return saved, nil
}

func (s *Store) DeleteEngine(ctx context.Context, id string) error {
	ct, err := s.db.Pool().Exec(ctx, `DELETE FROM engines WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("引擎不存在: %s", id)
	}
	return nil
}

func (s *Store) EnginesCount(ctx context.Context) (int, error) {
	var n int
	if err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM engines`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}
