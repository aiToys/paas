// Package pg 提供 dataservice.Repository 的 PostgreSQL 实现。
// 显式 WHERE tenant_id=$1 多租户过滤（与内存 1:1）；
// Create 以 ctx 租户为准、忽略请求体 TenantID（防越权写）；
// spec 用 JSONB 列存 map[string]string，读写经 marshalSpec/unmarshalSpec 辅助 nil 安全处理
// （读出 nil/`null`/空 → 空 map 非 nil，避免后续 nil map 写 panic）。
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/dataservice"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Store 是 dataservice.Repository 的 PostgreSQL 实现。
type Store struct {
	db *storagepg.DB
}

// NewStore 创建 dataservice PG 仓储。db 必须已完成迁移。
func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

// dsCols 与 model.DataService 字段顺序对齐（scan 列顺序必须一致）。
// 注意：spec 列在 name 与 status 之间，对应 DataService.Spec。
const dsCols = `id, tenant_id, kind, name, spec, status, env_id, app_id, created_at, updated_at`

// marshalSpec 把 map[string]string 序列化为 JSONB 列所需的字节切片。
// nil map 也合法——json.Marshal(nil) 输出 "null"，DB 列 NOT NULL 但默认值 '{}' 兜底；
// 调用方 INSERT 显式传值，故这里保证 nil → '{}'（与 DEFAULT 一致，不依赖隐式转换）。
func marshalSpec(m map[string]string) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// unmarshalSpec 把 JSONB 列字节反序列化为 map[string]string。
// nil/空/null/无效 → 返回空 map（非 nil），保证调用方对返回值直接写入不 panic。
func unmarshalSpec(raw []byte) map[string]string {
	m := map[string]string{}
	if len(raw) == 0 {
		return m
	}
	// 显式处理 JSON null（json.Unmarshal 对 null 不动目标，需先判）
	if string(raw) == "null" {
		return m
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		// 容错：返回空 map 而非报错，避免一行坏数据阻塞整个 List
		return map[string]string{}
	}
	return m
}

// scanDS 通过 storagepg.RowScanner 抽象 QueryRow 与 Row 两种 Scan 来源。
// spec 列读出为 []byte，经 unmarshalSpec 转 nil 安全的 map。
func scanDS(r storagepg.RowScanner, d *dataservice.DataService) error {
	var specRaw []byte
	if err := r.Scan(&d.ID, &d.TenantID, &d.Kind, &d.Name, &specRaw, &d.Status, &d.EnvID, &d.AppID, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return err
	}
	d.Spec = unmarshalSpec(specRaw)
	return nil
}

// List 按 kind 过滤（kind 空表示全部），按 created_at 倒序（与内存实现一致）。
func (s *Store) List(ctx context.Context, kind string) ([]dataservice.DataService, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + dsCols + ` FROM data_services WHERE tenant_id=$1`
	args := []any{tid}
	if kind != "" {
		args = append(args, kind)
		q += fmt.Sprintf(" AND kind=$%d", len(args))
	}
	q += " ORDER BY created_at DESC"
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]dataservice.DataService, 0)
	for rows.Next() {
		var d dataservice.DataService
		if err = scanDS(rows, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Get 读取单条。跨租户访问返回 not found（不泄漏存在性）。
func (s *Store) Get(ctx context.Context, id string) (dataservice.DataService, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return dataservice.DataService{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+dsCols+` FROM data_services WHERE id=$1 AND tenant_id=$2`, id, tid)
	var d dataservice.DataService
	if err = scanDS(row, &d); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dataservice.DataService{}, fmt.Errorf("数据服务不存在: %s", id)
		}
		return dataservice.DataService{}, err
	}
	return d, nil
}

// Create 写入数据服务。以 ctx 租户写 tenant_id；status 空补 running（与内存一致）。
// 租户内 name 唯一冲突 → 「数据服务已存在」（与内存一致）。
func (s *Store) Create(ctx context.Context, d dataservice.DataService) (dataservice.DataService, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return dataservice.DataService{}, err
	}
	if err := d.Validate(); err != nil {
		return dataservice.DataService{}, err
	}
	d.TenantID = tid
	if d.Status == "" {
		d.Status = dataservice.StatusRunning
	}
	specBytes, err := marshalSpec(d.Spec)
	if err != nil {
		return dataservice.DataService{}, err
	}
	row := s.db.Pool().QueryRow(ctx, `
INSERT INTO data_services (id, tenant_id, kind, name, spec, status, env_id, app_id, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING `+dsCols,
		d.ID, d.TenantID, d.Kind, d.Name, specBytes, d.Status, d.EnvID, d.AppID, d.CreatedAt, d.UpdatedAt,
	)
	var saved dataservice.DataService
	if err = scanDS(row, &saved); err != nil {
		if storagepg.IsUniqueViolation(err) {
			return dataservice.DataService{}, storagepg.FormatExists("数据服务")
		}
		return dataservice.DataService{}, err
	}
	return saved, nil
}

// Update 仅允许改 spec/status（与内存一致）；kind/name/tenant_id/env_id/app_id 不变。
// 跨租户访问 RowsAffected==0 → not found（不泄漏）。
func (s *Store) Update(ctx context.Context, d dataservice.DataService) (dataservice.DataService, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return dataservice.DataService{}, err
	}
	// status 合法性校验（与内存一致）：非空时必须属 creating|running|stopped。
	if d.Status != "" {
		if _, ok := map[string]struct{}{
			dataservice.StatusCreating: {}, dataservice.StatusRunning: {}, dataservice.StatusStopped: {},
		}[d.Status]; !ok {
			return dataservice.DataService{}, fmt.Errorf("非法状态: %s", d.Status)
		}
	}
	// 动态拼 SET：status 空保留原值；spec=nil 保留原值（与内存「nil 不覆盖」一致）。
	setParts := make([]string, 0, 3)
	args := make([]any, 0, 4)
	if d.Status != "" {
		args = append(args, d.Status)
		setParts = append(setParts, fmt.Sprintf("status=$%d", len(args)))
	}
	var specBytes []byte
	if d.Spec != nil {
		specBytes, err = marshalSpec(d.Spec)
		if err != nil {
			return dataservice.DataService{}, err
		}
		args = append(args, specBytes)
		setParts = append(setParts, fmt.Sprintf("spec=$%d", len(args)))
	}
	if len(setParts) == 0 {
		// 无字段需更新：直接返回现有行（仍校验存在 + 跨租户隔离）。
		return s.Get(ctx, d.ID)
	}
	args = append(args, d.ID, tid)
	q := `UPDATE data_services SET ` +
		strings.Join(setParts, ", ") +
		fmt.Sprintf(", updated_at=NOW() WHERE id=$%d AND tenant_id=$%d RETURNING ", len(args)-1, len(args)) +
		dsCols
	row := s.db.Pool().QueryRow(ctx, q, args...)
	var saved dataservice.DataService
	if err = scanDS(row, &saved); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dataservice.DataService{}, fmt.Errorf("数据服务不存在: %s", d.ID)
		}
		return dataservice.DataService{}, err
	}
	return saved, nil
}

// Delete 删除指定数据服务。跨租户访问 RowsAffected==0 → not found（不泄漏）。
func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM data_services WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("数据服务不存在: %s", id)
	}
	return nil
}

// DataServicesCount 返回全表数据服务数，供 PG seed 判空（表空才灌，幂等）。
// 注意：不经租户过滤，仅用于启动期 seed 判空，不暴露给业务层。
func (s *Store) DataServicesCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM data_services`).Scan(&n)
	return n, err
}
