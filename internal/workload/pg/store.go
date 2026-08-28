// Package pg 提供 workload.Repository 的 PostgreSQL 实现。
// 显式 WHERE tenant_id=$1 多租户过滤（与内存 1:1）；
// Create 以 ctx 租户为准、忽略请求体 TenantID（防越权写）；
// 跨租户访问统一返回 not found（不泄漏存在性）。
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/internal/workload"
)

// Store 是 workload.Repository 的 PostgreSQL 实现。
type Store struct {
	db *pg.DB
}

// NewStore 创建 workload PG 仓储。db 必须已完成迁移。
func NewStore(db *pg.DB) *Store { return &Store{db: db} }

// wlCols 与 model.Workload 字段顺序对齐（scan 列顺序必须一致）。
const wlCols = `id, tenant_id, app_id, env_id, lane_id, service, service_id, type, name, image, image_ref, replicas, ready, status, schedule, command, port, container_port, domain, resources, created_at`

// marshalResources 把 ResourceSpec 序列化为 JSONB 字节；零值 -> '{}'（与列 DEFAULT 一致）。
func marshalResources(r workload.ResourceSpec) []byte {
	if r.IsEmpty() {
		return []byte(`{}`)
	}
	b, err := json.Marshal(r)
	if err != nil { // 纯字符串字段，不会失败；防御性兜底
		return []byte(`{}`)
	}
	return b
}

// unmarshalResources 反序列化 JSONB 为 ResourceSpec；nil/空/null/无效 -> 零值（nil 安全）。
func unmarshalResources(raw []byte) workload.ResourceSpec {
	var r workload.ResourceSpec
	if len(raw) == 0 {
		return r
	}
	_ = json.Unmarshal(raw, &r)
	return r
}

// scanWL 通过 pg.RowScanner 抽象 QueryRow 与 Row 两种 Scan 来源。
func scanWL(r pg.RowScanner, w *workload.Workload) error {
	var resRaw []byte
	if err := r.Scan(
		&w.ID, &w.TenantID, &w.AppID, &w.EnvID, &w.LaneID, &w.Service, &w.ServiceID, &w.Type,
		&w.Name, &w.Image, &w.ImageRef, &w.Replicas, &w.Ready,
		&w.Status, &w.Schedule, &w.Command, &w.Port, &w.ContainerPort, &w.Domain, &resRaw, &w.CreatedAt,
	); err != nil {
		return err
	}
	w.Resources = unmarshalResources(resRaw)
	return nil
}

// List 按租户 + 可选 envID/appID/laneID/wtype/service 过滤；空串表示该维度不过滤（与内存语义一致）。
// 动态拼 WHERE：固定 tenant_id=$1，各过滤维度非空各追加 AND col=$N。
// 参数顺序按 append 动态编号。
func (s *Store) List(ctx context.Context, envID, appID, laneID, wtype, service string) ([]workload.Workload, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + wlCols + ` FROM workloads WHERE tenant_id=$1`
	args := []any{tid}
	if envID != "" {
		args = append(args, envID)
		q += fmt.Sprintf(` AND env_id=$%d`, len(args))
	}
	if appID != "" {
		args = append(args, appID)
		q += fmt.Sprintf(` AND app_id=$%d`, len(args))
	}
	if laneID != "" {
		args = append(args, laneID)
		q += fmt.Sprintf(` AND lane_id=$%d`, len(args))
	}
	if wtype != "" {
		args = append(args, wtype)
		q += fmt.Sprintf(` AND type=$%d`, len(args))
	}
	if service != "" {
		args = append(args, service)
		q += fmt.Sprintf(` AND service=$%d`, len(args))
	}
	q += ` ORDER BY id`
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]workload.Workload, 0)
	for rows.Next() {
		var w workload.Workload
		if err = scanWL(rows, &w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// ListAll 跨租户返回全部工作负载（admin 视图，不过滤 tenant，返回对象带 TenantID）。
// 不做 env/app/type 过滤；按 TenantID 升序、再 ID 升序排序。SELECT 列含 tenant_id（wlCols 已含）。
// 与 List 共享 wlCols/scanWL，避免列漂移；查询逻辑独立（无 WHERE tenant_id）保持清晰。
func (s *Store) ListAll(ctx context.Context) ([]workload.Workload, error) {
	q := `SELECT ` + wlCols + ` FROM workloads ORDER BY tenant_id, id`
	rows, err := s.db.Pool().Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]workload.Workload, 0)
	for rows.Next() {
		var w workload.Workload
		if err = scanWL(rows, &w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Get 取单个工作负载。跨租户访问返回 not found（不泄漏）。
func (s *Store) Get(ctx context.Context, id string) (workload.Workload, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return workload.Workload{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+wlCols+` FROM workloads WHERE id=$1 AND tenant_id=$2`, id, tid)
	var w workload.Workload
	if err = scanWL(row, &w); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return workload.Workload{}, fmt.Errorf("工作负载不存在: %s", id)
		}
		return workload.Workload{}, err
	}
	return w, nil
}

// Create 写入工作负载。以 ctx 租户为准、忽略请求体 TenantID；空 LaneID 补 default 基线。
// 主键冲突返回「工作负载已存在」（与内存实现消息一致，不使用 FormatExists 哨兵）。
func (s *Store) Create(ctx context.Context, w workload.Workload) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	if err := w.Validate(); err != nil {
		return err
	}
	if w.ID == "" {
		return fmt.Errorf("工作负载 ID 不能为空")
	}
	w.TenantID = tid // 以 ctx 为准
	if w.LaneID == "" {
		w.LaneID = workload.LaneDefault
	}
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO workloads (`+wlCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		w.ID, w.TenantID, w.AppID, w.EnvID, w.LaneID, w.Service, w.ServiceID, w.Type,
		w.Name, w.Image, w.ImageRef, w.Replicas, w.Ready,
		w.Status, w.Schedule, w.Command, w.Port, w.ContainerPort, w.Domain, marshalResources(w.Resources), w.CreatedAt)
	if pg.IsUniqueViolation(err) {
		// 同租户 Name 唯一约束：Name 即 K8s Service 名，同名会让 reconciler 抢建同一
		// K8s Service（AlreadyOwned）无限 requeue（审计第 6 轮 I2）。主键冲突同理。
		if strings.Contains(err.Error(), "workloads_tenant_name") {
			return fmt.Errorf("同名工作负载已存在: %s", w.Name)
		}
		return fmt.Errorf("工作负载已存在: %s", w.ID)
	}
	return err
}

// Update 调整期望副本与状态；status 空串表示不改状态。
// mock 语义：status 切到 running 时 ready 跟随 replicas（与内存实现一致）。
// 返回更新后的工作负载；跨租户访问返回 not found（不泄漏）。
func (s *Store) Update(ctx context.Context, id string, replicas int, status string) (workload.Workload, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return workload.Workload{}, err
	}
	// status 空串不改状态；status=running 时 ready 跟随 replicas（mock 语义）。
	q := `UPDATE workloads SET replicas=$3,` +
		`status=CASE WHEN $4<>'' THEN $4 ELSE status END,` +
		`ready=CASE WHEN $4='running' THEN $3 ELSE ready END ` +
		`WHERE id=$1 AND tenant_id=$2 RETURNING ` + wlCols
	row := s.db.Pool().QueryRow(ctx, q, id, tid, replicas, status)
	var w workload.Workload
	if err = scanWL(row, &w); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return workload.Workload{}, fmt.Errorf("工作负载不存在: %s", id)
		}
		return workload.Workload{}, err
	}
	return w, nil
}

// UpdateImage 更新工作负载镜像（display + digest）；imageRef 空串不覆盖已有 digest
// （与内存实现一致，兼容仅刷新 display 的场景）。返回更新后的工作负载。
// 跨租户访问返回 not found（不泄漏）。
func (s *Store) UpdateImage(ctx context.Context, id, image, imageRef string) (workload.Workload, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return workload.Workload{}, err
	}
	q := `UPDATE workloads SET image=$3,` +
		`image_ref=CASE WHEN $4<>'' THEN $4 ELSE image_ref END ` +
		`WHERE id=$1 AND tenant_id=$2 RETURNING ` + wlCols
	row := s.db.Pool().QueryRow(ctx, q, id, tid, image, imageRef)
	var w workload.Workload
	if err = scanWL(row, &w); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return workload.Workload{}, fmt.Errorf("工作负载不存在: %s", id)
		}
		return workload.Workload{}, err
	}
	return w, nil
}

// UpdateSchedule 修改 cronjob 的 cron 表达式。
// 仅 cronjob 类型有效（service/job 拒绝）；schedule 空对 cronjob 拒绝（Validate 语义）。
// 跨租户访问返回 not found（不泄漏）。
func (s *Store) UpdateSchedule(ctx context.Context, id, schedule string) (workload.Workload, error) {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return workload.Workload{}, err
	}
	// 先取 + 校验类型（cronjob 专属）+ 归属租户，跨租户统一 not found 不泄漏
	var wtype string
	if err = s.db.Pool().QueryRow(ctx,
		`SELECT type FROM workloads WHERE id=$1 AND tenant_id=$2`, id, tid).Scan(&wtype); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return workload.Workload{}, fmt.Errorf("工作负载不存在: %s", id)
		}
		return workload.Workload{}, err
	}
	if wtype != workload.TypeCronJob {
		return workload.Workload{}, fmt.Errorf("仅 cronjob 支持修改 schedule，当前类型: %s", wtype)
	}
	if schedule == "" {
		return workload.Workload{}, fmt.Errorf("cronjob schedule 不能为空")
	}
	row := s.db.Pool().QueryRow(ctx,
		`UPDATE workloads SET schedule=$3 WHERE id=$1 AND tenant_id=$2 RETURNING `+wlCols, id, tid, schedule)
	var w workload.Workload
	if err = scanWL(row, &w); err != nil {
		return workload.Workload{}, err
	}
	return w, nil
}

// Delete 删除指定工作负载。跨租户访问返回 not found（不泄漏）。
// SetServiceID 回填服务实体关联（存量回填/新部署写入）。
// 跨租户访问返回 not found（不泄漏）。
func (s *Store) SetServiceID(ctx context.Context, id, serviceID string) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE workloads SET service_id=$3 WHERE id=$1 AND tenant_id=$2`, id, tid, serviceID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("工作负载不存在: %s", id)
	}
	return nil
}

// SetResources 覆盖容器资源规格（deploy 显式 resources 时更新既有 Workload）。
// 跨租户访问返回 not found（不泄漏）。
func (s *Store) SetResources(ctx context.Context, id string, res workload.ResourceSpec) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE workloads SET resources=$3 WHERE id=$1 AND tenant_id=$2`, id, tid, marshalResources(res))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("工作负载不存在: %s", id)
	}
	return nil
}

// SetDomain 设置对外域名（canary 独立验证域名；与 SetResources 同模式）。
func (s *Store) SetDomain(ctx context.Context, id, domain string) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`UPDATE workloads SET domain=$3 WHERE id=$1 AND tenant_id=$2`, id, tid, domain)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("工作负载不存在: %s", id)
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	tid, err := pg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM workloads WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("工作负载不存在: %s", id)
	}
	return nil
}

// WorkloadsCount 返回全表工作负载数，供 PG seed 判空（表空才灌，幂等）。
// 注意：不经租户过滤，仅用于启动期 seed 判空，不暴露给业务层（不入 Repository 接口）。
func (s *Store) WorkloadsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM workloads`).Scan(&n)
	return n, err
}
