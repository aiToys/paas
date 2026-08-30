// Package pg 提供 configcenter.Repository 的 PostgreSQL 实现（namespace + 配置项 draft + 发布版本，单 Store）。
//
// 一个 Store 同时实现三个子接口（NamespaceStore/ItemStore/PublishStore），
// 与内存版同构（方法名带实体前缀，避免单 Store 实现时的重名冲突）。
// 显式 WHERE tenant_id 强制多租户过滤；Create 以 ctx 租户为准忽略请求体 TenantID；
// 跨租户访问统一 not found（不泄漏存在性）；错误消息沿用内存版领域文本。
//
// Publish.Snapshot 用 JSONB 存 map[string]string（不可变，只随新 Publish 生成；nil 安全
// 由 marshalSnapshot/unmarshalSnapshot 保证，与 dataservice.Spec / governance.Meta 同款）。
//
// CreatePublish 在事务内：
//  1. SELECT COALESCE(MAX(version),0)+1 算下一版本号（namespace 内单调）；
//  2. SELECT key,value FROM cc_items WHERE namespace_id=$1 组装 snapshot；
//  3. INSERT 新行 status=active；
//  4. UPDATE 旧 active → rolled-back；
//     事务保证 version 单调递增 + 同 namespace 内 active 唯一。
//
// RollbackPublish 在事务内：当前 active 翻 rolled-back，目标 rolled-back 翻 active
// （与内存版同款语义）。
//
// DeleteNamespace 在事务内级联清 items + publishes（与内存版同款，PG 版扩到 publishes）。
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/configcenter"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
)

// Store 实现 configcenter.Repository（三子接口）。与内存版同款单 Store 模式。
type Store struct {
	db *storagepg.DB
}

// NewStore 创建 configcenter PG 仓储。db 必须已完成迁移。
func NewStore(db *storagepg.DB) *Store { return &Store{db: db} }

// 列常量与各 struct 字段顺序严格对齐（scan 列序必须一致）。
// snapshot 列读取为 []byte，由 scanPublish 转 nil 安全的 map。
const (
	nsCols   = `id, tenant_id, name, scope, app_id, env_id, service_id, "desc", updated_at`
	itemCols = `id, tenant_id, namespace_id, key, value, type, updated_at`
	pubCols  = `id, tenant_id, namespace_id, version, snapshot, status, created_at`
	ovCols   = `id, tenant_id, app_id, env_id, lane_id, key, value, updated_at`
)

// ---------- JSONB 辅助（nil 安全） ----------

// marshalSnapshot 把 map[string]string 序列化为 JSONB 字节；nil → '{}'（与列 DEFAULT 一致）。
func marshalSnapshot(m map[string]string) ([]byte, error) {
	if m == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(m)
}

// unmarshalSnapshot 反序列化 JSONB 为 map[string]string；nil/空/null/无效 → 空 map（非 nil）。
// 保证调用方对返回值直接写入不 panic（与 dataservice.unmarshalSpec 同款）。
func unmarshalSnapshot(raw []byte) map[string]string {
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

// ---------- scan 辅助 ----------

func scanNamespace(r storagepg.RowScanner, n *configcenter.Namespace) error {
	return r.Scan(&n.ID, &n.TenantID, &n.Name, &n.Scope, &n.AppID, &n.EnvID, &n.ServiceID, &n.Desc, &n.UpdatedAt)
}

func scanOverride(r storagepg.RowScanner, o *configcenter.LaneOverride) error {
	return r.Scan(&o.ID, &o.TenantID, &o.AppID, &o.EnvID, &o.LaneID, &o.Key, &o.Value, &o.UpdatedAt)
}

func scanItem(r storagepg.RowScanner, it *configcenter.ConfigItem) error {
	return r.Scan(&it.ID, &it.TenantID, &it.NamespaceID, &it.Key, &it.Value, &it.Type, &it.UpdatedAt)
}

func scanPublish(r storagepg.RowScanner, p *configcenter.Publish) error {
	var snapRaw []byte
	if err := r.Scan(&p.ID, &p.TenantID, &p.NamespaceID, &p.Version, &snapRaw, &p.Status, &p.CreatedAt); err != nil {
		return err
	}
	p.Snapshot = unmarshalSnapshot(snapRaw)
	return nil
}

// ---------- NamespaceStore ----------

// ListNamespaces 列出当前租户的全部命名空间，按 Name 升序（与内存版一致）。
// serviceID 非空时按关联服务过滤（空=该租户全部）。
func (s *Store) ListNamespaces(ctx context.Context, serviceID string) ([]configcenter.Namespace, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + nsCols + ` FROM cc_namespaces WHERE tenant_id=$1`
	args := []any{tid}
	if serviceID != "" {
		args = append(args, serviceID)
		q += fmt.Sprintf(" AND service_id=$%d", len(args))
	}
	q += " ORDER BY name"
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]configcenter.Namespace, 0)
	for rows.Next() {
		var n configcenter.Namespace
		if err = scanNamespace(rows, &n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ListAllNamespaces 跨租户列出全部命名空间（admin 平台总览，不过滤 tenant；按 tenant_id, name 排序）。
func (s *Store) ListAllNamespaces(ctx context.Context) ([]configcenter.Namespace, error) {
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+nsCols+` FROM cc_namespaces ORDER BY tenant_id, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]configcenter.Namespace, 0)
	for rows.Next() {
		var n configcenter.Namespace
		if err = scanNamespace(rows, &n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetNamespace 取单个命名空间；跨租户访问返回 not found（不泄漏存在性）。
func (s *Store) GetNamespace(ctx context.Context, id string) (configcenter.Namespace, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return configcenter.Namespace{}, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+nsCols+` FROM cc_namespaces WHERE id=$1 AND tenant_id=$2`, id, tid)
	var n configcenter.Namespace
	if err = scanNamespace(row, &n); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return configcenter.Namespace{}, fmt.Errorf("%w: %s", configcenter.ErrNamespaceNotFound, id)
		}
		return configcenter.Namespace{}, err
	}
	return n, nil
}

// CreateNamespace 写入命名空间。以 ctx 租户为准忽略请求体；空 ID 自动生成。
// 租户内 Name 唯一冲突 → ErrNamespaceNameTaken sentinel（跨实现统一 409 判定）。
func (s *Store) CreateNamespace(ctx context.Context, n configcenter.Namespace) (configcenter.Namespace, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return configcenter.Namespace{}, err
	}
	if err := n.Validate(); err != nil {
		return configcenter.Namespace{}, err
	}
	// scope 语义：AppID 非空 → 应用派生（Name 强制派生名，防伪造 shared 占名）；空 → 共享。
	if n.AppID != "" {
		n.Scope = configcenter.ScopeApp
		n.Name = configcenter.AppNSName(n.AppID, n.EnvID)
	} else {
		n.Scope = configcenter.ScopeShared
		n.EnvID = ""
	}
	if n.ID == "" {
		n.ID = newCCID("ns")
	}
	n.TenantID = tid
	n.UpdatedAt = time.Now()
	_, err = s.db.Pool().Exec(ctx,
		`INSERT INTO cc_namespaces (`+nsCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		n.ID, n.TenantID, n.Name, n.Scope, n.AppID, n.EnvID, n.ServiceID, n.Desc, n.UpdatedAt)
	if storagepg.IsUniqueViolation(err) {
		return configcenter.Namespace{}, fmt.Errorf("%w: %s", configcenter.ErrNamespaceNameTaken, n.Name)
	}
	if err != nil {
		return configcenter.Namespace{}, err
	}
	return n, nil
}

// EnsureByApp 懒建（或返回既有的）应用派生命名空间（env=” 基线，兼容旧签名）。
func (s *Store) EnsureByApp(ctx context.Context, appID string) (configcenter.Namespace, error) {
	return s.EnsureByAppEnv(ctx, appID, "")
}

// FindAppNamespace 查应用派生命名空间（env=” 基线，兼容旧签名）。无返回 false。
func (s *Store) FindAppNamespace(ctx context.Context, appID string) (configcenter.Namespace, bool, error) {
	return s.FindAppNamespaceEnv(ctx, appID, "")
}

// findAppNSRow 按 (tenant, app, env) 精确查询单行（不回退）。
func (s *Store) findAppNSRow(ctx context.Context, tid, appID, envID string) (configcenter.Namespace, error) {
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+nsCols+` FROM cc_namespaces WHERE tenant_id=$1 AND scope='app' AND app_id=$2 AND env_id=$3`,
		tid, appID, envID)
	var n configcenter.Namespace
	if err := scanNamespace(row, &n); err != nil {
		return configcenter.Namespace{}, err
	}
	return n, nil
}

// EnsureByAppEnv 懒建（或返回既有的）(app, env) 维度应用派生命名空间。幂等。
// 先查后插；并发竞态由 UNIQUE (tenant_id, name) 兜底——唯一冲突时回查返回（幂等兜底）。
func (s *Store) EnsureByAppEnv(ctx context.Context, appID, envID string) (configcenter.Namespace, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return configcenter.Namespace{}, err
	}
	if n, err := s.findAppNSRow(ctx, tid, appID, envID); err == nil {
		return n, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return configcenter.Namespace{}, err
	}
	created, err := s.CreateNamespace(ctx, configcenter.Namespace{
		Name: configcenter.AppNSName(appID, envID), Scope: configcenter.ScopeApp, AppID: appID, EnvID: envID,
	})
	if storagepg.IsUniqueViolation(err) {
		// 并发 Ensure：另一请求已建，回查返回（幂等兜底）；回查 ErrNoRows 说明冲突
		// 来自手工共享 ns 抢占派生名，映射 ErrNamespaceNameTaken（与 memory 实现对齐）。
		if n, qerr := s.findAppNSRow(ctx, tid, appID, envID); qerr == nil {
			return n, nil
		} else if errors.Is(qerr, pgx.ErrNoRows) {
			return configcenter.Namespace{}, fmt.Errorf("%w: %s", configcenter.ErrNamespaceNameTaken, configcenter.AppNSName(appID, envID))
		}
	}
	return created, err
}

// FindAppNamespaceEnv 查 (app, env) 维度命名空间（不创建）。发现解析语义：
// envID 非空时精确未命中回退 env=” 基线；envID 空仅精确匹配 env=”。
func (s *Store) FindAppNamespaceEnv(ctx context.Context, appID, envID string) (configcenter.Namespace, bool, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return configcenter.Namespace{}, false, err
	}
	n, err := s.findAppNSRow(ctx, tid, appID, envID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return configcenter.Namespace{}, false, err
		}
		// 回退：env 精确未命中 → env='' 基线（envID 空不走此分支，已精确匹配过）。
		if envID != "" {
			n, err = s.findAppNSRow(ctx, tid, appID, "")
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return configcenter.Namespace{}, false, nil
				}
				return configcenter.Namespace{}, false, err
			}
			return n, true, nil
		}
		return configcenter.Namespace{}, false, nil
	}
	return n, true, nil
}

// DeleteNamespace 删除命名空间 + 级联清 items + publishes（事务保证原子）。
// 跨租户访问 RowsAffected==0 → not found（不泄漏）。
func (s *Store) DeleteNamespace(ctx context.Context, id string) error {
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
		`DELETE FROM cc_namespaces WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", configcenter.ErrNamespaceNotFound, id)
	}
	// 级联清子表：namespace_id 已锁定到本租户的该 ns，带 tenant_id 更稳（防极端跨租户残留）。
	if _, err = tx.Exec(ctx,
		`DELETE FROM cc_items WHERE namespace_id=$1 AND tenant_id=$2`, id, tid); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx,
		`DELETE FROM cc_publishes WHERE namespace_id=$1 AND tenant_id=$2`, id, tid); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---------- ItemStore ----------

// ListItems 按 namespaceID 过滤（空=该租户全部）；按 Key 升序（与内存版一致）。
func (s *Store) ListItems(ctx context.Context, namespaceID string) ([]configcenter.ConfigItem, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + itemCols + ` FROM cc_items WHERE tenant_id=$1`
	args := []any{tid}
	if namespaceID != "" {
		args = append(args, namespaceID)
		q += fmt.Sprintf(" AND namespace_id=$%d", len(args))
	}
	q += " ORDER BY key"
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]configcenter.ConfigItem, 0)
	for rows.Next() {
		var it configcenter.ConfigItem
		if err = scanItem(rows, &it); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// UpsertItem 同 (tenant, namespace, key) 视为同一项更新，否则新增。
// 校验 namespace 存在且属本租户（与内存版同款锁内校验）。
// type 空补 text（与内存版一致）；以 ctx 租户写 tenant_id。
func (s *Store) UpsertItem(ctx context.Context, item configcenter.ConfigItem) (configcenter.ConfigItem, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return configcenter.ConfigItem{}, err
	}
	// tx 内锁 namespace 行 + INSERT，防 GetNamespace 与 INSERT 间 DeleteNamespace 级联清产生孤儿 item。
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return configcenter.ConfigItem{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var nsTid string
	if err = tx.QueryRow(ctx, `SELECT tenant_id FROM cc_namespaces WHERE id=$1 FOR UPDATE`, item.NamespaceID).Scan(&nsTid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return configcenter.ConfigItem{}, fmt.Errorf("%w: %s", configcenter.ErrNamespaceNotFound, item.NamespaceID)
		}
		return configcenter.ConfigItem{}, err
	}
	if nsTid != tid {
		return configcenter.ConfigItem{}, fmt.Errorf("%w: %s", configcenter.ErrNamespaceNotFound, item.NamespaceID)
	}
	if err := item.Validate(); err != nil {
		return configcenter.ConfigItem{}, err
	}
	if item.Type == "" {
		item.Type = configcenter.TypeText
	}
	if item.ID == "" {
		item.ID = newCCID("item")
	}
	item.TenantID = tid
	item.UpdatedAt = time.Now()
	// ON CONFLICT 主路径：命中唯一键 (namespace_id, key) 则更新 value/type/updated_at，
	// RETURNING 取实际落库行（含生成的 id 与 updated_at）。
	row := tx.QueryRow(ctx, `
INSERT INTO cc_items (`+itemCols+`)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (namespace_id, key) DO UPDATE
    SET value      = EXCLUDED.value,
        type       = EXCLUDED.type,
        updated_at = EXCLUDED.updated_at
RETURNING `+itemCols,
		item.ID, item.TenantID, item.NamespaceID, item.Key, item.Value, item.Type, item.UpdatedAt,
	)
	var saved configcenter.ConfigItem
	if err = scanItem(row, &saved); err != nil {
		return configcenter.ConfigItem{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return configcenter.ConfigItem{}, err
	}
	return saved, nil
}

// DeleteItem 删除配置项；跨租户访问 RowsAffected==0 → not found（不泄漏存在性）。
func (s *Store) DeleteItem(ctx context.Context, id string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM cc_items WHERE id=$1 AND tenant_id=$2`, id, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("配置项不存在: %s", id)
	}
	return nil
}

// ---------- PublishStore ----------

// ListPublishes 发布历史（按 version 降序，最新在前；与内存版一致）。
func (s *Store) ListPublishes(ctx context.Context, namespaceID string) ([]configcenter.Publish, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + pubCols + ` FROM cc_publishes WHERE tenant_id=$1`
	args := []any{tid}
	if namespaceID != "" {
		args = append(args, namespaceID)
		q += fmt.Sprintf(" AND namespace_id=$%d", len(args))
	}
	q += " ORDER BY version DESC"
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]configcenter.Publish, 0)
	for rows.Next() {
		var p configcenter.Publish
		if err = scanPublish(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CreatePublish 快照当前 namespace 全部 item 生成新 active 发布。
// 事务内：取 max(version)+1 → 组装 snapshot → 插新 active → 旧 active 翻 rolled-back
// （逐行对齐内存版 CreatePublish：内存遍历 publishes 计算 max + 翻 status，PG 换 SQL 同款语义）。
// 事务保证 version 单调 + 同 namespace active 唯一（UNIQUE(namespace_id, version) 兜底并发）。
//
// 注意：snapshot 仅当本 namespace 当前存在 item 时非空；无 item 也允许发布空 snapshot
// （与内存版一致：snapshot 初始化为空 map，无 item 时也为 map{}）。
func (s *Store) CreatePublish(ctx context.Context, namespaceID string) (configcenter.Publish, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return configcenter.Publish{}, err
	}
	// namespace 存在性/归属由事务内 FOR UPDATE 复校验统一保证（不重复池上预检）。
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return configcenter.Publish{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // 已提交或失败均无害

	// 锁 namespace 行复校验存在 + 归属（防 GetNamespace 与 tx 间 DeleteNamespace 级联清产生孤儿 publish）
	var nsTid string
	if err = tx.QueryRow(ctx, `SELECT tenant_id FROM cc_namespaces WHERE id=$1 FOR UPDATE`, namespaceID).Scan(&nsTid); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return configcenter.Publish{}, fmt.Errorf("%w: %s", configcenter.ErrNamespaceNotFound, namespaceID)
		}
		return configcenter.Publish{}, err
	}
	if nsTid != tid {
		return configcenter.Publish{}, fmt.Errorf("%w: %s", configcenter.ErrNamespaceNotFound, namespaceID)
	}

	// 1) 下一版本号 = namespace 内 MAX(version)+1；无历史则 1。
	var version int
	if err = tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM cc_publishes WHERE namespace_id=$1 AND tenant_id=$2`,
		namespaceID, tid).Scan(&version); err != nil {
		return configcenter.Publish{}, err
	}

	// 2) 组装 snapshot：扫该 namespace 全部 (key, value)（与内存版同款遍历 items）。
	rows, err := tx.Query(ctx,
		`SELECT key, value FROM cc_items WHERE namespace_id=$1 AND tenant_id=$2`, namespaceID, tid)
	if err != nil {
		return configcenter.Publish{}, err
	}
	defer rows.Close()
	snapshot := map[string]string{}
	for rows.Next() {
		var k, v string
		if err = rows.Scan(&k, &v); err != nil {
			return configcenter.Publish{}, err
		}
		snapshot[k] = v
	}
	if err = rows.Err(); err != nil {
		return configcenter.Publish{}, err
	}

	// 2.5) 空发布拒绝：新快照与当前 active 完全一致 → ErrNoChanges（事务内比较，
	// 防 API 直调/前端状态错位产出内容相同的空版本虚涨版本号、污染回滚目标）。
	var curActiveRaw []byte
	err = tx.QueryRow(ctx,
		`SELECT snapshot FROM cc_publishes WHERE namespace_id=$1 AND tenant_id=$2 AND status=$3`,
		namespaceID, tid, configcenter.StatusActive).Scan(&curActiveRaw)
	switch {
	case err == nil:
		if configcenter.SnapshotsEqual(unmarshalSnapshot(curActiveRaw), snapshot) {
			return configcenter.Publish{}, configcenter.ErrNoChanges
		}
	case errors.Is(err, pgx.ErrNoRows):
		// 无 active（首次发布）——放行
	default:
		return configcenter.Publish{}, err
	}

	// 3) 旧 active → rolled-back（先翻后插：partial unique index uq_cc_publishes_ns_active
	// 要求同 ns 仅一行 active，先 INSERT 会撞索引）。
	if _, err = tx.Exec(ctx,
		`UPDATE cc_publishes SET status=$1 WHERE namespace_id=$2 AND tenant_id=$3 AND status=$4`,
		configcenter.StatusRolledBack, namespaceID, tid, configcenter.StatusActive); err != nil {
		return configcenter.Publish{}, err
	}

	// 4) INSERT 新 active 行。
	pub := configcenter.Publish{
		ID:          newCCID("pub"),
		TenantID:    tid,
		NamespaceID: namespaceID,
		Version:     version,
		Snapshot:    snapshot,
		Status:      configcenter.StatusActive,
		CreatedAt:   time.Now(),
	}
	snapBytes, err := marshalSnapshot(pub.Snapshot)
	if err != nil {
		return configcenter.Publish{}, err
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO cc_publishes (`+pubCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		pub.ID, pub.TenantID, pub.NamespaceID, pub.Version, snapBytes, pub.Status, pub.CreatedAt); err != nil {
		return configcenter.Publish{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return configcenter.Publish{}, err
	}
	return pub, nil
}

// RollbackPublish 激活历史 rolled-back 发布为 active。
// 事务内：校验目标存在 + 本租户 + 非 active（已是 active 则报错与内存版一致）；
// 当前 active 翻 rolled-back，目标翻 active（逐行对齐内存版 RollbackPublish）。
func (s *Store) RollbackPublish(ctx context.Context, publishID string) (configcenter.Publish, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return configcenter.Publish{}, err
	}
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return configcenter.Publish{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // 已提交或失败均无害

	// 1) 取目标行（带 tenant 校验）；同时拿 namespace_id 与 status。
	var target configcenter.Publish
	row := tx.QueryRow(ctx,
		`SELECT `+pubCols+` FROM cc_publishes WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, publishID, tid)
	if err = scanPublish(row, &target); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return configcenter.Publish{}, fmt.Errorf("%w: %s", configcenter.ErrPublishNotFound, publishID)
		}
		return configcenter.Publish{}, err
	}
	if target.Status == configcenter.StatusActive {
		return configcenter.Publish{}, fmt.Errorf("%w: v%d", configcenter.ErrPublishAlreadyActive, target.Version)
	}

	// 2) 当前 active → rolled-back（同 namespace，排除目标行；目标已非 active 故自然排除）。
	if _, err = tx.Exec(ctx,
		`UPDATE cc_publishes SET status=$1 WHERE namespace_id=$2 AND tenant_id=$3 AND status=$4`,
		configcenter.StatusRolledBack, target.NamespaceID, tid, configcenter.StatusActive); err != nil {
		return configcenter.Publish{}, err
	}

	// 3) 目标 → active。
	if _, err = tx.Exec(ctx,
		`UPDATE cc_publishes SET status=$1 WHERE id=$2 AND tenant_id=$3`,
		configcenter.StatusActive, publishID, tid); err != nil {
		return configcenter.Publish{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return configcenter.Publish{}, err
	}
	target.Status = configcenter.StatusActive
	return target, nil
}

// ActivePublish 返回 namespace 当前 active 发布（客户端发现用）。
// 无发布返回零值 + false（与内存版一致）。
func (s *Store) ActivePublish(ctx context.Context, namespaceID string) (configcenter.Publish, bool, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return configcenter.Publish{}, false, err
	}
	row := s.db.Pool().QueryRow(ctx,
		`SELECT `+pubCols+` FROM cc_publishes WHERE namespace_id=$1 AND tenant_id=$2 AND status=$3 LIMIT 1`,
		namespaceID, tid, configcenter.StatusActive)
	var p configcenter.Publish
	if err = scanPublish(row, &p); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return configcenter.Publish{}, false, nil
		}
		return configcenter.Publish{}, false, err
	}
	return p, true, nil
}

// PublishNamespaceID 返回发布所属 namespace（回滚路由校验用）。
func (s *Store) PublishNamespaceID(ctx context.Context, publishID string) (string, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return "", err
	}
	var nsID string
	err = s.db.Pool().QueryRow(ctx,
		`SELECT namespace_id FROM cc_publishes WHERE id=$1 AND tenant_id=$2`, publishID, tid).Scan(&nsID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%w: %s", configcenter.ErrPublishNotFound, publishID)
	}
	return nsID, err
}

// SeedIfEmpty no-op（去假数据）：不灌 mock 命名空间/配置/发布。用户经控制台配置真实配置中心。
// 保留签名兼容 seedPGAllIfEmpty 调用。
func (s *Store) SeedIfEmpty(ctx context.Context) error {
	return nil
}

// 编译期断言：Store 实现全部三子接口（类型不匹配时编译失败）。
var (
	_ configcenter.NamespaceStore = (*Store)(nil)
	_ configcenter.ItemStore      = (*Store)(nil)
	_ configcenter.PublishStore   = (*Store)(nil)
)

// newCCID 生成带前缀的短 ID（纳秒时间戳 + 前缀）。mock 期保证基本唯一，与 governance PG 同款风格。
func newCCID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// ---------- LaneOverrideStore ----------

// UpsertLaneOverride 同 (tenant, app, env, lane, key) 覆盖更新，否则新增（ON CONFLICT 主路径）。
func (s *Store) UpsertLaneOverride(ctx context.Context, o configcenter.LaneOverride) (configcenter.LaneOverride, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return configcenter.LaneOverride{}, err
	}
	if err := o.Validate(); err != nil {
		return configcenter.LaneOverride{}, err
	}
	if o.ID == "" {
		o.ID = newCCID("ovr")
	}
	o.TenantID = tid
	o.UpdatedAt = time.Now()
	row := s.db.Pool().QueryRow(ctx, `
INSERT INTO cc_lane_overrides (`+ovCols+`)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT (tenant_id, app_id, env_id, lane_id, key) DO UPDATE
    SET value      = EXCLUDED.value,
        updated_at = EXCLUDED.updated_at
RETURNING `+ovCols,
		o.ID, o.TenantID, o.AppID, o.EnvID, o.LaneID, o.Key, o.Value, o.UpdatedAt)
	var saved configcenter.LaneOverride
	if err := scanOverride(row, &saved); err != nil {
		return configcenter.LaneOverride{}, err
	}
	return saved, nil
}

// DeleteLaneOverride 删除覆盖；跨租户/不存在 RowsAffected==0 → ErrLaneOverrideNotFound（不泄漏）。
func (s *Store) DeleteLaneOverride(ctx context.Context, appID, envID, laneID, key string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM cc_lane_overrides WHERE tenant_id=$1 AND app_id=$2 AND env_id=$3 AND lane_id=$4 AND key=$5`,
		tid, appID, envID, laneID, key)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", configcenter.ErrLaneOverrideNotFound, key)
	}
	return nil
}

// ListLaneOverrides 按 (app, env, lane) 过滤（lane 空=该 env 全部泳道），按 Key 升序。
func (s *Store) ListLaneOverrides(ctx context.Context, appID, envID, laneID string) ([]configcenter.LaneOverride, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT ` + ovCols + ` FROM cc_lane_overrides WHERE tenant_id=$1 AND app_id=$2 AND env_id=$3`
	args := []any{tid, appID, envID}
	if laneID != "" {
		args = append(args, laneID)
		q += fmt.Sprintf(" AND lane_id=$%d", len(args))
	}
	q += " ORDER BY key"
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]configcenter.LaneOverride, 0)
	for rows.Next() {
		var o configcenter.LaneOverride
		if err = scanOverride(rows, &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ListLaneOverridesForClean 泳道回收级联清理用：按 (env, lane) 跨 app 列出（tenant 从 ctx）。
func (s *Store) ListLaneOverridesForClean(ctx context.Context, envID, laneID string) ([]configcenter.LaneOverride, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+ovCols+` FROM cc_lane_overrides WHERE tenant_id=$1 AND env_id=$2 AND lane_id=$3 ORDER BY key`,
		tid, envID, laneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]configcenter.LaneOverride, 0)
	for rows.Next() {
		var o configcenter.LaneOverride
		if err = scanOverride(rows, &o); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// ---- 共享配置引用（shared ns → 应用派生 ns）----

const refCols = `id, tenant_id, app_ns_id, shared_ns_id, created_at`

func scanNSRef(r storagepg.RowScanner, ref *configcenter.NSRef) error {
	return r.Scan(&ref.ID, &ref.TenantID, &ref.AppNSID, &ref.SharedNSID, &ref.CreatedAt)
}

// AddNSRef 建引用：前置校验 shared ns 存在 + 本租户 + scope=shared + 非自引；
// 唯一约束 (tenant, app_ns, shared_ns) 冲突 → ErrRefExists。
func (s *Store) AddNSRef(ctx context.Context, appNSID, sharedNSID string) (configcenter.NSRef, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return configcenter.NSRef{}, err
	}
	// 跨租户/不存在统一 NotFound 不泄漏（scope 校验在前，非 shared 的 NotFound 也不泄漏存在性细节）。
	var scope string
	err = s.db.Pool().QueryRow(ctx,
		`SELECT scope FROM cc_namespaces WHERE id=$1 AND tenant_id=$2`, sharedNSID, tid).Scan(&scope)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return configcenter.NSRef{}, fmt.Errorf("%w: %s", configcenter.ErrNamespaceNotFound, sharedNSID)
		}
		return configcenter.NSRef{}, err
	}
	if scope != configcenter.ScopeShared || appNSID == sharedNSID {
		return configcenter.NSRef{}, configcenter.ErrRefNotShared
	}
	ref := configcenter.NSRef{
		ID: newCCID("ref"), TenantID: tid,
		AppNSID: appNSID, SharedNSID: sharedNSID, CreatedAt: time.Now(),
	}
	row := s.db.Pool().QueryRow(ctx, `
INSERT INTO cc_ns_refs (`+refCols+`)
VALUES ($1,$2,$3,$4,$5)
ON CONFLICT (tenant_id, app_ns_id, shared_ns_id) DO NOTHING
RETURNING `+refCols,
		ref.ID, ref.TenantID, ref.AppNSID, ref.SharedNSID, ref.CreatedAt)
	var saved configcenter.NSRef
	if err := scanNSRef(row, &saved); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return configcenter.NSRef{}, configcenter.ErrRefExists
		}
		return configcenter.NSRef{}, err
	}
	return saved, nil
}

// DeleteNSRef 解除引用；跨租户/不存在 RowsAffected==0 → ErrRefNotFound（不泄漏）。
func (s *Store) DeleteNSRef(ctx context.Context, refID string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx,
		`DELETE FROM cc_ns_refs WHERE id=$1 AND tenant_id=$2`, refID, tid)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", configcenter.ErrRefNotFound, refID)
	}
	return nil
}

// ListNSRefs 列 app ns 的引用（created_at 升序 = merge 铺垫顺序）。
func (s *Store) ListNSRefs(ctx context.Context, appNSID string) ([]configcenter.NSRef, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	return s.queryNSRefs(ctx,
		`SELECT `+refCols+` FROM cc_ns_refs WHERE tenant_id=$1 AND app_ns_id=$2 ORDER BY created_at`, tid, appNSID)
}

// ListNSRefUsers 反查 shared ns 的引用方（影响面展示）。
func (s *Store) ListNSRefUsers(ctx context.Context, sharedNSID string) ([]configcenter.NSRef, error) {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return nil, err
	}
	return s.queryNSRefs(ctx,
		`SELECT `+refCols+` FROM cc_ns_refs WHERE tenant_id=$1 AND shared_ns_id=$2 ORDER BY created_at`, tid, sharedNSID)
}

func (s *Store) queryNSRefs(ctx context.Context, q string, args ...any) ([]configcenter.NSRef, error) {
	rows, err := s.db.Pool().Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]configcenter.NSRef, 0)
	for rows.Next() {
		var r configcenter.NSRef
		if err = scanNSRef(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ResetItemsToSnapshot 单事务把 ns 的 draft items 对齐到快照（回滚同步草稿）：
// items 行 FOR UPDATE 消除并发编辑交错；快照 key 补缺/改异（保留原 type），快照外删除。
func (s *Store) ResetItemsToSnapshot(ctx context.Context, namespaceID string, snapshot map[string]string) error {
	tid, err := storagepg.TenantOrErr(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.Pool().Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // 已提交或失败均无害

	// ns 归属校验（FOR UPDATE 锁 ns 行，串行化同 ns 的并发 reset）。
	var nsCheck int
	if err = tx.QueryRow(ctx,
		`SELECT 1 FROM cc_namespaces WHERE id=$1 AND tenant_id=$2 FOR UPDATE`, namespaceID, tid).Scan(&nsCheck); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: %s", configcenter.ErrNamespaceNotFound, namespaceID)
		}
		return err
	}

	// 现有 items（FOR UPDATE）：快照内更新值，快照外删除。
	rows, err := tx.Query(ctx,
		`SELECT id, key, value, type FROM cc_items WHERE tenant_id=$1 AND namespace_id=$2 FOR UPDATE`, tid, namespaceID)
	if err != nil {
		return err
	}
	type existingItem struct{ id, key, val, typ string }
	existing := make([]existingItem, 0, 8)
	for rows.Next() {
		var it existingItem
		if err = rows.Scan(&it.id, &it.key, &it.val, &it.typ); err != nil {
			rows.Close()
			return err
		}
		existing = append(existing, it)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}

	seen := make(map[string]bool, len(snapshot))
	for _, it := range existing {
		if val, ok := snapshot[it.key]; ok {
			seen[it.key] = true
			if it.val == val {
				continue
			}
			if _, err = tx.Exec(ctx,
				`UPDATE cc_items SET value=$1, updated_at=now() WHERE id=$2`, val, it.id); err != nil {
				return err
			}
			continue
		}
		if _, err = tx.Exec(ctx, `DELETE FROM cc_items WHERE id=$1`, it.id); err != nil {
			return err
		}
	}
	for key, val := range snapshot {
		if seen[key] {
			continue
		}
		if _, err = tx.Exec(ctx, `
INSERT INTO cc_items (id, tenant_id, namespace_id, key, value, type, updated_at)
VALUES ($1,$2,$3,$4,$5,'text',now()) ON CONFLICT DO NOTHING`,
			newCCID("item"), tid, namespaceID, key, val); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
