// Package pg 提供 governance.Repository 的 PostgreSQL 实现（服务 + 实例 + 路由 + 熔断器，单 Store）。
//
// 一个 Store 同时实现四个子接口（ServiceStore/InstanceStore/RouteStore/BreakerStore），
// 与内存版同构（方法名带实体前缀，避免单 Store 实现时的重名冲突）。
// 显式 WHERE tenant_id 强制多租户过滤；Create 以 ctx 租户为准忽略请求体 TenantID；
// 跨租户访问统一 not found（不泄漏存在性）；错误消息沿用内存版领域文本。
//
// JSONB 两处：
//   - Instance.Meta  map[string]string（nil 安全，与 dataservice.Spec 同款模式）
//   - Route.Methods  []string（nil/空 → '[]'，读出空 slice 非 nil）
//
// CircuitBreaker.State/WindowStats **不持久化**——只存配置列；读出后由 handler 调
// EvaluateBreaker 即时评估填充（与内存版 handler 同构）。本 store 只负责返回配置。
//
// DeleteService 在事务内级联清 instances/routes/breakers（与内存版同款语义，但内存版
// 只级联 instances；PG 版按 task 约定扩展到三表，事务保证原子）。
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/governance"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Store 实现 governance.Repository（四子接口）。与内存版同款单 Store 模式。
type Store struct {
	db *storagepg.DB
}

// NewStore 创建 governance PG 仓储。db 必须已完成迁移。
func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

// 列常量与各 struct 字段顺序严格对齐（scan 列序必须一致）。
// 注意：JSONB 列读取为 []byte，由 scan 辅助函数转 nil 安全的结构。
const (
	svcCols     = `id, tenant_id, name, app_id, env_id, protocol, port, "desc", updated_at`
	instCols    = `id, tenant_id, service_id, addr, status, lane_id, meta, updated_at`
	routeCols   = `id, tenant_id, name, path, service_id, methods, strip_path, enabled, updated_at`
	breakerCols = `id, tenant_id, name, service_id, strategy, threshold, min_requests, window_secs, enabled, updated_at`
)

// ---------- JSONB 辅助（nil 安全） ----------

// marshalMeta 把 map[string]string 序列化为 JSONB 字节；nil → '{}'（与列 DEFAULT 一致）。
func marshalMeta(m map[string]string) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// unmarshalMeta 反序列化 JSONB 为 map[string]string；nil/空/null/无效 → 空 map（非 nil）。
// 保证调用方对返回值直接写入不 panic（与 dataservice.unmarshalSpec 同款）。
func unmarshalMeta(raw []byte) map[string]string {
	m := map[string]string{}
	if len(raw) == 0 {
		return m
	}
	if string(raw) == "null" {
		return m
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]string{} // 容错：单行坏数据不阻塞整个 List
	}
	return m
}

// marshalMethods 把 []string 序列化为 JSONB 字节；nil/空 → '[]'（与列 DEFAULT 一致）。
func marshalMethods(m []string) ([]byte, error) {
	if m == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(m)
}

// unmarshalMethods 反序列化 JSONB 为 []string；nil/空/null/无效 → 空 slice（非 nil）。
// 保证调用方对返回值直接 range 不 panic。
func unmarshalMethods(raw []byte) []string {
	s := []string{}
	if len(raw) == 0 {
		return s
	}
	if string(raw) == "null" {
		return s
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return []string{}
	}
	return s
}

// ---------- scan 辅助 ----------

func scanService(r storagepg.RowScanner, s *governance.Service) error {
	return r.Scan(&s.ID, &s.TenantID, &s.Name, &s.AppID, &s.EnvID, &s.Protocol, &s.Port, &s.Desc, &s.UpdatedAt)
}

func scanInstance(r storagepg.RowScanner, in *governance.Instance) error {
	var metaRaw []byte
	if err := r.Scan(&in.ID, &in.TenantID, &in.ServiceID, &in.Addr, &in.Status, &in.LaneID, &metaRaw, &in.UpdatedAt); err != nil {
		return err
	}
	in.Meta = unmarshalMeta(metaRaw)
	return nil
}

func scanRoute(r storagepg.RowScanner, rt *governance.Route) error {
	var methodsRaw []byte
	if err := r.Scan(&rt.ID, &rt.TenantID, &rt.Name, &rt.Path, &rt.ServiceID, &methodsRaw, &rt.StripPath, &rt.Enabled, &rt.UpdatedAt); err != nil {
		return err
	}
	rt.Methods = unmarshalMethods(methodsRaw)
	return nil
}

func scanBreaker(r storagepg.RowScanner, b *governance.CircuitBreaker) error {
	// 注意：不读 state/stats 列（不存在）；State/Stats 留空，由 handler 调 EvaluateBreaker 填充。
	return r.Scan(&b.ID, &b.TenantID, &b.Name, &b.ServiceID, &b.Strategy,
		&b.Threshold, &b.MinRequests, &b.WindowSecs, &b.Enabled, &b.UpdatedAt)
}

// ---------- ServiceStore ----------

// ListServices 按 (envID, appID) 过滤（空=不过滤）；按 Name 升序（与内存版一致）。
func (s *Store) ListServices(ctx context.Context, envID, appID string) ([]governance.Service, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + svcCols + ` FROM gov_services WHERE tenant_id=$1`
	args := []any{tid}
	if envID != "" {
		args = append(args, envID)
		q += fmt.Sprintf(" AND env_id=$%d", len(args))
	}
	if appID != "" {
		args = append(args, appID)
		q += fmt.Sprintf(" AND app_id=$%d", len(args))
	}
	q += " ORDER BY name"
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]governance.Service, 0)
	for rows.Next() {
		var sv governance.Service
		if err = scanService(rows, &sv); err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

// GetService 取单个服务；跨租户访问返回 not found（不泄漏）。
func (s *Store) GetService(ctx context.Context, id string) (governance.Service, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return governance.Service{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+svcCols+` FROM gov_services WHERE id=$1 AND tenant_id=$2`, id, tid)
	var sv governance.Service
	if err = scanService(row, &sv); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return governance.Service{}, fmt.Errorf("服务不存在: %s", id)
		}
		return governance.Service{}, err
	}
	return sv, nil
}

// CreateService 写入服务定义。以 ctx 租户为准忽略请求体；空 ID 自动生成。
// 租户内 Name 唯一冲突 → 「服务名已存在」（沿用内存版领域文本，不用 FormatExists 哨兵）。
func (s *Store) CreateService(ctx context.Context, svc governance.Service) (governance.Service, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return governance.Service{}, err
	}
	if err := svc.Validate(); err != nil {
		return governance.Service{}, err
	}
	if svc.ID == "" {
		svc.ID = newGovID("svc")
	}
	svc.TenantID = tid
	svc.UpdatedAt = time.Now()
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO gov_services (`+svcCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		svc.ID, svc.TenantID, svc.Name, svc.AppID, svc.EnvID, svc.Protocol, svc.Port, svc.Desc, svc.UpdatedAt)
	if storagepg.IsUniqueViolation(err) {
		return governance.Service{}, fmt.Errorf("服务名已存在: %s", svc.Name)
	}
	if err != nil {
		return governance.Service{}, err
	}
	return svc, nil
}

// DeleteService 删除服务 + 级联清 instances/routes/breakers（事务保证原子）。
// 跨租户访问 RowsAffected==0 → not found（不泄漏）。
func (s *Store) DeleteService(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // 已提交或失败均无害
	// 先删主表（带 tenant 校验）；RowsAffected==0 表示不存在或跨租户。
	tag, err := tx.Exec(ctx,
		`DELETE FROM gov_services WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("服务不存在: %s", id)
	}
	// 级联清子表：service_id 已锁定到本租户的该服务，带 tenant_id 更稳（防极端跨租户残留）。
	if _, err = tx.Exec(ctx,
		`DELETE FROM gov_instances WHERE service_id=$1 AND tenant_id=$2`, id, tid); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx,
		`DELETE FROM gov_routes WHERE service_id=$1 AND tenant_id=$2`, id, tid); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx,
		`DELETE FROM gov_breakers WHERE service_id=$1 AND tenant_id=$2`, id, tid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------- InstanceStore ----------

// ListInstances 按 serviceID 过滤（空=该租户全部）；按 Addr 升序（与内存版一致）。
func (s *Store) ListInstances(ctx context.Context, serviceID string) ([]governance.Instance, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + instCols + ` FROM gov_instances WHERE tenant_id=$1`
	args := []any{tid}
	if serviceID != "" {
		args = append(args, serviceID)
		q += fmt.Sprintf(" AND service_id=$%d", len(args))
	}
	q += " ORDER BY addr"
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]governance.Instance, 0)
	for rows.Next() {
		var in governance.Instance
		if err = scanInstance(rows, &in); err != nil {
			return nil, err
		}
		out = append(out, in)
	}
	return out, rows.Err()
}

// RegisterInstance 注册实例。校验服务存在且属本租户（与内存版同款锁内校验，避免注册到不存在的服务）。
// Status 空补 healthy，LaneID 空补 default；空 ID 自动生成。
func (s *Store) RegisterInstance(ctx context.Context, in governance.Instance) (governance.Instance, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return governance.Instance{}, err
	}
	if err := in.Validate(); err != nil {
		return governance.Instance{}, err
	}
	// 校验服务归属（与内存版同款语义）：存在 + 本租户。
	var exists int
	if err = s.db.Pool().QueryRow(ctx,
		`SELECT 1 FROM gov_services WHERE id=$1 AND tenant_id=$2`, in.ServiceID, tid).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return governance.Instance{}, fmt.Errorf("服务不存在: %s", in.ServiceID)
		}
		return governance.Instance{}, err
	}
	if in.Status == "" {
		in.Status = governance.StatusHealthy
	}
	if in.LaneID == "" {
		in.LaneID = governance.LaneDefault
	}
	if in.ID == "" {
		in.ID = newGovID("inst")
	}
	in.TenantID = tid
	in.UpdatedAt = time.Now()
	metaBytes, err := marshalMeta(in.Meta)
	if err != nil {
		return governance.Instance{}, err
	}
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO gov_instances (`+instCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		in.ID, in.TenantID, in.ServiceID, in.Addr, in.Status, in.LaneID, metaBytes, in.UpdatedAt)
	if err != nil {
		return governance.Instance{}, err
	}
	return in, nil
}

// DeregisterInstance 删除实例；跨租户访问 RowsAffected==0 → not found（不泄漏）。
func (s *Store) DeregisterInstance(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM gov_instances WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("实例不存在: %s", id)
	}
	return nil
}

// Heartbeat 更新实例 UpdatedAt（与内存版一致：仅刷新时间戳，本期不过期剔除）。
// 注意 SQL 用 NOW()，无需参数占位（避免 Brief 中 `$2` 同时绑定时间与 tenant 的歧义）。
func (s *Store) Heartbeat(ctx context.Context, id string) (governance.Instance, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return governance.Instance{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`UPDATE gov_instances SET updated_at=NOW() WHERE id=$1 AND tenant_id=$2 RETURNING `+instCols,
		id, tid)
	var in governance.Instance
	if err = scanInstance(row, &in); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return governance.Instance{}, fmt.Errorf("实例不存在: %s", id)
		}
		return governance.Instance{}, err
	}
	return in, nil
}

// InstanceServiceID 返回实例所属服务 ID（handler 注销时校验生产权限用）。
func (s *Store) InstanceServiceID(ctx context.Context, id string) (string, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return "", err
	}
	var svcID string
	err = s.db.Pool().QueryRow(ctx,
		`SELECT service_id FROM gov_instances WHERE id=$1 AND tenant_id=$2`, id, tid).Scan(&svcID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("实例不存在: %s", id)
	}
	return svcID, err
}

// ---------- RouteStore ----------

// ListRoutes 按 serviceID 过滤（空=全部）；按 UpdatedAt 倒序（与内存版一致，最新在前）。
func (s *Store) ListRoutes(ctx context.Context, serviceID string) ([]governance.Route, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + routeCols + ` FROM gov_routes WHERE tenant_id=$1`
	args := []any{tid}
	if serviceID != "" {
		args = append(args, serviceID)
		q += fmt.Sprintf(" AND service_id=$%d", len(args))
	}
	q += " ORDER BY updated_at DESC"
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]governance.Route, 0)
	for rows.Next() {
		var rt governance.Route
		if err = scanRoute(rows, &rt); err != nil {
			return nil, err
		}
		out = append(out, rt)
	}
	return out, rows.Err()
}

// GetRoute 取单条；跨租户访问返回 not found（不泄漏）。
func (s *Store) GetRoute(ctx context.Context, id string) (governance.Route, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return governance.Route{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+routeCols+` FROM gov_routes WHERE id=$1 AND tenant_id=$2`, id, tid)
	var rt governance.Route
	if err = scanRoute(row, &rt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return governance.Route{}, fmt.Errorf("路由不存在: %s", id)
		}
		return governance.Route{}, err
	}
	return rt, nil
}

// CreateRoute 创建路由（租户内 Name 唯一）。
// Methods nil/空 → '[]'（Validate 在调用前应已拒绝空 Methods；这里底层兜底）。
func (s *Store) CreateRoute(ctx context.Context, r governance.Route) (governance.Route, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return governance.Route{}, err
	}
	if err := r.Validate(); err != nil {
		return governance.Route{}, err
	}
	if r.ID == "" {
		r.ID = newGovID("route")
	}
	r.TenantID = tid
	r.UpdatedAt = time.Now()
	methodsBytes, err := marshalMethods(r.Methods)
	if err != nil {
		return governance.Route{}, err
	}
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO gov_routes (`+routeCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		r.ID, r.TenantID, r.Name, r.Path, r.ServiceID, methodsBytes, r.StripPath, r.Enabled, r.UpdatedAt)
	if storagepg.IsUniqueViolation(err) {
		return governance.Route{}, fmt.Errorf("路由名已存在: %s", r.Name)
	}
	if err != nil {
		return governance.Route{}, err
	}
	return r, nil
}

// UpdateRoute 更新路由（path/serviceId/methods/stripPath/enabled）；与内存版同款「非空覆盖」语义。
// Methods nil 表示不改；非 nil（含空 slice）覆盖。合并后复校验，防 PUT 用空 methods 绕过 Create 时的非空不变量。
func (s *Store) UpdateRoute(ctx context.Context, r governance.Route) (governance.Route, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return governance.Route{}, err
	}
	// 取现有行（带租户校验）。
	ex, err := s.GetRoute(ctx, r.ID)
	if err != nil {
		return governance.Route{}, err
	}
	if r.Path != "" {
		ex.Path = r.Path
	}
	if r.ServiceID != "" {
		ex.ServiceID = r.ServiceID
	}
	if r.Methods != nil {
		ex.Methods = r.Methods
	}
	ex.StripPath = r.StripPath
	ex.Enabled = r.Enabled
	ex.UpdatedAt = time.Now()
	// 合并后复校验（与内存版一致）。
	if err := ex.Validate(); err != nil {
		return governance.Route{}, err
	}
	methodsBytes, err := marshalMethods(ex.Methods)
	if err != nil {
		return governance.Route{}, err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE gov_routes SET path=$1, service_id=$2, methods=$3, strip_path=$4, enabled=$5, updated_at=$6
		 WHERE id=$7 AND tenant_id=$8`,
		ex.Path, ex.ServiceID, methodsBytes, ex.StripPath, ex.Enabled, ex.UpdatedAt, ex.ID, tid)
	if err != nil {
		return governance.Route{}, err
	}
	if tag.RowsAffected() == 0 {
		return governance.Route{}, fmt.Errorf("路由不存在: %s", r.ID)
	}
	return ex, nil
}

// DeleteRoute 删除路由；跨租户访问 RowsAffected==0 → not found（不泄漏）。
func (s *Store) DeleteRoute(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM gov_routes WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("路由不存在: %s", id)
	}
	return nil
}

// ---------- BreakerStore ----------

// ListBreakers 按 serviceID 过滤（空=全部）；按 UpdatedAt 倒序（与内存版一致，最新在前）。
// 注意：返回的 Breaker.State/Stats 为零值（不持久化），由 handler 调 EvaluateBreaker 即时填充。
func (s *Store) ListBreakers(ctx context.Context, serviceID string) ([]governance.CircuitBreaker, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + breakerCols + ` FROM gov_breakers WHERE tenant_id=$1`
	args := []any{tid}
	if serviceID != "" {
		args = append(args, serviceID)
		q += fmt.Sprintf(" AND service_id=$%d", len(args))
	}
	q += " ORDER BY updated_at DESC"
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]governance.CircuitBreaker, 0)
	for rows.Next() {
		var b governance.CircuitBreaker
		if err = scanBreaker(rows, &b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetBreaker 取单条；跨租户访问返回 not found（不泄漏）。State/Stats 留空。
func (s *Store) GetBreaker(ctx context.Context, id string) (governance.CircuitBreaker, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return governance.CircuitBreaker{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+breakerCols+` FROM gov_breakers WHERE id=$1 AND tenant_id=$2`, id, tid)
	var b governance.CircuitBreaker
	if err = scanBreaker(row, &b); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return governance.CircuitBreaker{}, fmt.Errorf("熔断器不存在: %s", id)
		}
		return governance.CircuitBreaker{}, err
	}
	return b, nil
}

// CreateBreaker 创建熔断器（租户内 Name 唯一）。只写配置列；State/Stats 不入库。
func (s *Store) CreateBreaker(ctx context.Context, b governance.CircuitBreaker) (governance.CircuitBreaker, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return governance.CircuitBreaker{}, err
	}
	if err := b.Validate(); err != nil {
		return governance.CircuitBreaker{}, err
	}
	if b.ID == "" {
		b.ID = newGovID("cb")
	}
	b.TenantID = tid
	b.UpdatedAt = time.Now()
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO gov_breakers (`+breakerCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		b.ID, b.TenantID, b.Name, b.ServiceID, b.Strategy,
		b.Threshold, b.MinRequests, b.WindowSecs, b.Enabled, b.UpdatedAt)
	if storagepg.IsUniqueViolation(err) {
		return governance.CircuitBreaker{}, fmt.Errorf("熔断器名已存在: %s", b.Name)
	}
	if err != nil {
		return governance.CircuitBreaker{}, err
	}
	return b, nil
}

// UpdateBreaker 更新熔断器（strategy/threshold/minRequests/windowSecs/enabled/serviceId）；
// 与内存版同款「非零覆盖」语义；合并后复校验。
func (s *Store) UpdateBreaker(ctx context.Context, b governance.CircuitBreaker) (governance.CircuitBreaker, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return governance.CircuitBreaker{}, err
	}
	ex, err := s.GetBreaker(ctx, b.ID)
	if err != nil {
		return governance.CircuitBreaker{}, err
	}
	if b.Strategy != "" {
		ex.Strategy = b.Strategy
	}
	if b.Threshold > 0 {
		ex.Threshold = b.Threshold
	}
	if b.MinRequests > 0 {
		ex.MinRequests = b.MinRequests
	}
	if b.WindowSecs > 0 {
		ex.WindowSecs = b.WindowSecs
	}
	if b.ServiceID != "" {
		ex.ServiceID = b.ServiceID
	}
	ex.Enabled = b.Enabled
	ex.UpdatedAt = time.Now()
	if err := ex.Validate(); err != nil {
		return governance.CircuitBreaker{}, err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE gov_breakers SET strategy=$1, threshold=$2, min_requests=$3, window_secs=$4, enabled=$5, service_id=$6, updated_at=$7
		 WHERE id=$8 AND tenant_id=$9`,
		ex.Strategy, ex.Threshold, ex.MinRequests, ex.WindowSecs, ex.Enabled, ex.ServiceID, ex.UpdatedAt, ex.ID, tid)
	if err != nil {
		return governance.CircuitBreaker{}, err
	}
	if tag.RowsAffected() == 0 {
		return governance.CircuitBreaker{}, fmt.Errorf("熔断器不存在: %s", b.ID)
	}
	return ex, nil
}

// DeleteBreaker 删除熔断器；跨租户访问 RowsAffected==0 → not found（不泄漏）。
func (s *Store) DeleteBreaker(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM gov_breakers WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("熔断器不存在: %s", id)
	}
	return nil
}

// ---------- Count 方法（供 PG seed 判空，表空才灌，幂等；不经租户过滤，仅启动期用） ----------

// ServicesCount 返回 gov_services 全表行数。
func (s *Store) ServicesCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM gov_services`).Scan(&n)
	return n, err
}

// InstancesCount 返回 gov_instances 全表行数。
func (s *Store) InstancesCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM gov_instances`).Scan(&n)
	return n, err
}

// RoutesCount 返回 gov_routes 全表行数。
func (s *Store) RoutesCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM gov_routes`).Scan(&n)
	return n, err
}

// BreakersCount 返回 gov_breakers 全表行数。
func (s *Store) BreakersCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM gov_breakers`).Scan(&n)
	return n, err
}

// 编译期断言：Store 实现全部四子接口（类型不匹配时编译失败）。
var (
	_ governance.ServiceStore  = (*Store)(nil)
	_ governance.InstanceStore = (*Store)(nil)
	_ governance.RouteStore    = (*Store)(nil)
	_ governance.BreakerStore  = (*Store)(nil)
)

// newGovID 生成带前缀的短 ID（纳秒时间戳 + 前缀）。mock 期保证基本唯一，与 devops PG 同款风格。
func newGovID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
