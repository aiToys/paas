// Package pg 提供 dataservice.Repository 的 PostgreSQL 实现。
// 显式 WHERE tenant_id=$1 多租户过滤（与内存 1:1）；
// Create 以 ctx 租户为准、忽略请求体 TenantID（防越权写）；
// spec/connection 用 JSONB 列存 map[string]string，读写经 marshalSpec/unmarshalSpec 辅助 nil 安全处理
// （读出 nil/`null`/空 -> 空 map 非 nil，避免后续 nil map 写 panic）。
package pg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/dataservice"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Store 是 dataservice.Repository 的 PostgreSQL 实现。
type Store struct {
	db         *storagepg.DB
	nsResolver dataservice.NamespaceResolver
	seq        atomic.Int64 // ID 生成 seq，避免纳秒精度撞主键（与内存 ds-%d-%d 同款）
}

// Option 配置 Store。
type Option func(*Store)

// WithNamespaceResolver 注入 K8s namespace 解析器，Create 时用于生成 Connection FQDN。
// 未注入兜底 dataservice.DefaultNamespace。
func WithNamespaceResolver(r dataservice.NamespaceResolver) Option {
	return func(s *Store) { s.nsResolver = r }
}

// NewStore 创建 dataservice PG 仓储。db 必须已完成迁移。
func NewStore(db *storagepg.DB, opts ...Option) *Store {
	s := &Store{db: db}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *Store) namespace() string {
	if s.nsResolver != nil {
		if ns := s.nsResolver.Namespace(); ns != "" {
			return ns
		}
	}
	return dataservice.DefaultNamespace
}

// dsCols 与 model.DataService 字段顺序对齐（scan 列顺序必须一致）。
// spec/connection 均为 JSONB map[string]string，分别对应 DataService.Spec / Connection。
// 末尾列：实例浅管理字段（replicas 可空，cpu/memory/image NOT NULL DEFAULT ''，storage_gb DEFAULT 0）+
// source（managed|external，DEFAULT 'managed'）。
const dsCols = `id, tenant_id, kind, name, spec, connection, status, source, engine_id, env_id, app_id, created_at, updated_at, replicas, cpu, memory, storage_gb, image`

// marshalSpec 把 map[string]string 序列化为 JSONB 列所需的字节切片（spec 与 connection 共用）。
// nil map 也合法--json.Marshal(nil) 输出 "null"，DB 列 NOT NULL 但默认值 '{}' 兜底；
// 调用方 INSERT 显式传值，故这里保证 nil -> '{}'（与 DEFAULT 一致，不依赖隐式转换）。
func marshalSpec(m map[string]string) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// unmarshalSpec 把 JSONB 列字节反序列化为 map[string]string（spec 与 connection 共用）。
// nil/空/null/无效 -> 返回空 map（非 nil），保证调用方对返回值直接写入不 panic。
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
// spec/connection 列读出为 []byte，经 unmarshalSpec 转 nil 安全的 map。
func scanDS(r storagepg.RowScanner, d *dataservice.DataService) error {
	var specRaw, connRaw []byte
	var replicas sql.NullInt32
	if err := r.Scan(&d.ID, &d.TenantID, &d.Kind, &d.Name, &specRaw, &connRaw, &d.Status, &d.Source, &d.EngineID, &d.EnvID, &d.AppID, &d.CreatedAt, &d.UpdatedAt, &replicas, &d.CPU, &d.Memory, &d.StorageGB, &d.Image); err != nil {
		return err
	}
	d.Spec = unmarshalSpec(specRaw)
	d.Connection = unmarshalSpec(connRaw)
	if replicas.Valid {
		rv := int(replicas.Int32)
		d.Replicas = &rv
	}
	return nil
}

// queryAll 是 List/ListAll 共用的查询 helper：拼 SELECT + 可选 kind 过滤 + 排序，扫描后返回切片。
// whereArgs 为 WHERE 子句位置参数（不含 tenant 过滤时为空），orderCol 指定排序列。
func (s *Store) queryAll(ctx context.Context, whereArgs []any, kind string, orderCol string) ([]dataservice.DataService, error) {
	q := `SELECT ` + dsCols + ` FROM data_services`
	if len(whereArgs) > 0 {
		q += " WHERE tenant_id=$1"
	}
	args := append([]any{}, whereArgs...)
	if kind != "" {
		args = append(args, kind)
		if len(whereArgs) > 0 {
			q += fmt.Sprintf(" AND kind=$%d", len(args))
		} else {
			q += fmt.Sprintf(" WHERE kind=$%d", len(args))
		}
	}
	q += " ORDER BY " + orderCol
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

// List 按 kind 过滤（kind 空表示全部），按 created_at 倒序（与内存实现一致）。
func (s *Store) List(ctx context.Context, kind string) ([]dataservice.DataService, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	return s.queryAll(ctx, []any{tid}, kind, "created_at DESC")
}

// ListAll 跨租户返回全部数据服务（admin 后台用），按 tenant_id 升序、created_at 倒序。
// 不经 tenant 过滤；SELECT 含 tenant_id 且 scanDS 已填充 DataService.TenantID。
func (s *Store) ListAll(ctx context.Context) ([]dataservice.DataService, error) {
	return s.queryAll(ctx, nil, "", "tenant_id, created_at DESC")
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
// CreatedAt 零值兜底当前时间（与内存一致，避免 0001-01-01 排序异常）；
// 租户内 name 唯一冲突 -> 「数据服务已存在」（与内存一致）。
func (s *Store) Create(ctx context.Context, d dataservice.DataService) (dataservice.DataService, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return dataservice.DataService{}, err
	}
	if err := d.Validate(); err != nil {
		return dataservice.DataService{}, err
	}
	// ID 由 store 兜底生成（与内存一致；handler 未传时 PG 也不能插空主键）。
	// 带 seq 避免同进程并发 Create 在纳秒精度下撞主键（与内存 ds-%d-%d 同款）。
	if d.ID == "" {
		s.seq.Add(1)
		d.ID = fmt.Sprintf("ds-%d-%d", time.Now().UnixNano(), s.seq.Load())
	}
	d.TenantID = tid
	if d.Source == "" {
		d.Source = dataservice.SourceManaged
	}
	if d.Status == "" {
		d.Status = dataservice.StatusRunning
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now()
		d.UpdatedAt = d.CreatedAt
	}
	// managed：生成凭证 + 平台 FQDN；external：保留用户填的连接信息，不重生。
	if !dataservice.IsExternal(d.Source) {
		d.FillConnection(s.namespace())
	}
	specBytes, err := marshalSpec(d.Spec)
	if err != nil {
		return dataservice.DataService{}, err
	}
	connBytes, err := marshalSpec(d.Connection)
	if err != nil {
		return dataservice.DataService{}, err
	}
	row := s.db.Pool().QueryRow(ctx, `
INSERT INTO data_services (id, tenant_id, kind, name, spec, connection, status, source, engine_id, env_id, app_id, created_at, updated_at, replicas, cpu, memory, storage_gb, image)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
RETURNING `+dsCols,
		d.ID, d.TenantID, d.Kind, d.Name, specBytes, connBytes, d.Status, d.Source, d.EngineID, d.EnvID, d.AppID, d.CreatedAt, d.UpdatedAt,
		d.Replicas, d.CPU, d.Memory, d.StorageGB, d.Image,
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
// 读出现有后合并 + FillConnection 重算 connection（namespace 可能变），与内存路径行为一致；
// 跨租户访问 RowsAffected==0 -> not found（不泄漏）。
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
	// 先读现有（确认存在 + 跨租户隔离 + 拿原 spec/connection/envId）。
	ex, err := s.Get(ctx, d.ID)
	if err != nil {
		return dataservice.DataService{}, err
	}
	// 合并：status 空保留原值；spec=nil 保留原值（与内存「nil 不覆盖」一致）。
	if d.Status != "" {
		ex.Status = d.Status
	}
	if d.Spec != nil {
		ex.Spec = d.Spec
	}
	// 实例管理字段（非零覆盖，与内存一致）：handler 读现有→改对应字段→Update。
	if d.Replicas != nil {
		ex.Replicas = d.Replicas
	}
	if d.CPU != "" {
		ex.CPU = d.CPU
	}
	if d.Memory != "" {
		ex.Memory = d.Memory
	}
	if d.StorageGB > 0 {
		ex.StorageGB = d.StorageGB
	}
	if d.Image != "" {
		ex.Image = d.Image
	}
	ex.UpdatedAt = time.Now()
	// spec 改后重算 connection（凭证保留，host/port/uri 按 ns+engine 重算；与内存一致）。
	// external 模式跳过：连接信息是用户填的真实外部地址，不能被平台 FQDN 覆盖。
	if ex.Connection != nil && !dataservice.IsExternal(ex.Source) {
		ex.FillConnection(s.namespace())
	}
	// 合并后复校验，防止 PUT 用空 spec 清空 Create 时强制的必填字段。
	if err := ex.Validate(); err != nil {
		return dataservice.DataService{}, err
	}
	specBytes, err := marshalSpec(ex.Spec)
	if err != nil {
		return dataservice.DataService{}, err
	}
	connBytes, err := marshalSpec(ex.Connection)
	if err != nil {
		return dataservice.DataService{}, err
	}
	row := s.db.Pool().QueryRow(ctx, `
UPDATE data_services SET status=$1, spec=$2, connection=$3, replicas=$4, cpu=$5, memory=$6, storage_gb=$7, image=$8, updated_at=$9
WHERE id=$10 AND tenant_id=$11 RETURNING `+dsCols,
		ex.Status, specBytes, connBytes, ex.Replicas, ex.CPU, ex.Memory, ex.StorageGB, ex.Image, ex.UpdatedAt, d.ID, tid,
	)
	var saved dataservice.DataService
	if err = scanDS(row, &saved); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return dataservice.DataService{}, fmt.Errorf("数据服务不存在: %s", d.ID)
		}
		return dataservice.DataService{}, err
	}
	return saved, nil
}

// Delete 删除指定数据服务。跨租户访问 RowsAffected==0 -> not found（不泄漏）。
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
