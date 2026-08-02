// Package pg 提供 maas.Repository 的 PostgreSQL 实现。
// 模型目录平台级（无 tenant_id），不走 RLS；adminGuard super_admin 兜底访问控制。
// capabilities 用 JSONB 存 []string；通道 impl 不入库（运行时 BuildProvider 构造）。
// 所有写操作前校验 model 存在性，返友好 ErrModelNotFound（FK 错误对调用方不友好）。
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aitoys/paas/internal/maas"
	storagepg "github.com/aitoys/paas/internal/storage/pg"
	"github.com/aitoys/paas/pkg/provider"
)

// Store 是 maas.Repository 的 PostgreSQL 实现。
type Store struct {
	db *storagepg.DB
}

// NewStore 创建 maas PG 仓储。db 必须已完成迁移。
func NewStore(db *storagepg.DB) *Store {
	return &Store{db: db}
}

// modelCols 与 provider.Model 字段顺序对齐（不含 channels，channels 单独查 maas_channels 组装）。
const modelCols = `id, name, vendor, context_window, capabilities, input_price, output_price, "desc"`

// chCols 与 provider.Channel 字段顺序对齐；model_id 不映射到 Channel（关联由调用方上下文携带）。
const chCols = `id, model_id, type, priority, status, endpoint, vendor, upstream_model, credential_ref`

// marshalCaps 把 []string 序列化为 JSONB 字节（nil -> '[]'，与 DEFAULT 一致）。
func marshalCaps(caps []string) ([]byte, error) {
	if caps == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(caps)
}

// unmarshalCaps 把 JSONB 字节反序列化为 []string（nil/空/null/无效 -> 空 slice 非 nil）。
func unmarshalCaps(raw []byte) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return []string{}
	}
	var caps []string
	if err := json.Unmarshal(raw, &caps); err != nil {
		return []string{}
	}
	if caps == nil {
		return []string{}
	}
	return caps
}

func scanModel(r storagepg.RowScanner, m *provider.Model) error {
	var capsRaw []byte
	if err := r.Scan(&m.ID, &m.Name, &m.Vendor, &m.ContextWindow, &capsRaw, &m.InputPrice, &m.OutputPrice, &m.Description); err != nil {
		return err
	}
	m.Capabilities = unmarshalCaps(capsRaw)
	return nil
}

// modelExists 校验 model 存在性，供 channel 写操作前置检查（FK 错误不友好）。
func (s *Store) modelExists(ctx context.Context, id string) (bool, error) {
	var ok bool
	err := s.db.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM maas_models WHERE id=$1)`, id).Scan(&ok)
	return ok, err
}

// ListModels 返回全部模型（含其通道，按优先级排序），供 plugin 加载注册与 handler 列表。
// 两次查询（models + channels）后内存按 model_id 聚合，避免 N+1。
func (s *Store) ListModels(ctx context.Context) ([]*provider.Model, error) {
	rows, err := s.db.Pool().Query(ctx, `SELECT `+modelCols+` FROM maas_models ORDER BY id`)
	if err != nil {
		return nil, err
	}
	models := make([]*provider.Model, 0)
	byID := make(map[string]*provider.Model)
	for rows.Next() {
		var m provider.Model
		if err = scanModel(rows, &m); err != nil {
			rows.Close()
			return nil, err
		}
		m.Channels = make([]*provider.Channel, 0)
		byID[m.ID] = &m
		models = append(models, &m)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}

	chRows, err := s.db.Pool().Query(ctx, `SELECT `+chCols+` FROM maas_channels ORDER BY model_id, priority, id`)
	if err != nil {
		return nil, err
	}
	for chRows.Next() {
		var modelID string
		var c provider.Channel
		if err = chRows.Scan(&c.ID, &modelID, &c.Type, &c.Priority, &c.Status, &c.Endpoint, &c.Vendor, &c.UpstreamModel, &c.CredentialRef); err != nil {
			chRows.Close()
			return nil, err
		}
		if m, ok := byID[modelID]; ok {
			m.Channels = append(m.Channels, &c)
		}
	}
	chRows.Close()
	return models, chRows.Err()
}

// GetModel 返回单模型（含通道）。not found 返 ErrModelNotFound。
func (s *Store) GetModel(ctx context.Context, id string) (*provider.Model, error) {
	row := s.db.Pool().QueryRow(ctx, `SELECT `+modelCols+` FROM maas_models WHERE id=$1`, id)
	var m provider.Model
	if err := scanModel(row, &m); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", maas.ErrModelNotFound, id)
		}
		return nil, err
	}
	chs, err := s.ListChannels(ctx, id)
	if err != nil {
		return nil, err
	}
	m.Channels = chs
	return &m, nil
}

// CreateModel 写入模型标量（channels 经 CreateChannel 单独建，与内存一致）。
// 主键冲突 -> ErrModelExists。
func (s *Store) CreateModel(ctx context.Context, m *provider.Model) error {
	if m == nil || m.ID == "" {
		return fmt.Errorf("%w: model 与 ID 不能为空", maas.ErrModelExists)
	}
	caps, err := marshalCaps(m.Capabilities)
	if err != nil {
		return err
	}
	_, err = s.db.Pool().Exec(ctx, `
INSERT INTO maas_models (id, name, vendor, context_window, capabilities, input_price, output_price, "desc")
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		m.ID, m.Name, m.Vendor, m.ContextWindow, caps, m.InputPrice, m.OutputPrice, m.Description)
	if err != nil {
		if storagepg.IsUniqueViolation(err) {
			return fmt.Errorf("%w: %s", maas.ErrModelExists, m.ID)
		}
		return err
	}
	return nil
}

// UpdateModel 仅更新标量字段（channels 不动，与内存一致）。not found 返 ErrModelNotFound。
func (s *Store) UpdateModel(ctx context.Context, m *provider.Model) error {
	if m == nil || m.ID == "" {
		return maas.ErrModelNotFound
	}
	caps, err := marshalCaps(m.Capabilities)
	if err != nil {
		return err
	}
	tag, err := s.db.Pool().Exec(ctx, `
UPDATE maas_models SET name=$1, vendor=$2, context_window=$3, capabilities=$4, input_price=$5, output_price=$6, "desc"=$7
WHERE id=$8`,
		m.Name, m.Vendor, m.ContextWindow, caps, m.InputPrice, m.OutputPrice, m.Description, m.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", maas.ErrModelNotFound, m.ID)
	}
	return nil
}

// DeleteModel 删除模型，FK CASCADE 清其下通道。
func (s *Store) DeleteModel(ctx context.Context, id string) error {
	tag, err := s.db.Pool().Exec(ctx, `DELETE FROM maas_models WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s", maas.ErrModelNotFound, id)
	}
	return nil
}

// ListChannels 返回某模型的通道（按优先级）。model 不存在返 ErrModelNotFound。
func (s *Store) ListChannels(ctx context.Context, modelID string) ([]*provider.Channel, error) {
	ok, err := s.modelExists(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: %s", maas.ErrModelNotFound, modelID)
	}
	rows, err := s.db.Pool().Query(ctx,
		`SELECT `+chCols+` FROM maas_channels WHERE model_id=$1 ORDER BY priority, id`, modelID)
	if err != nil {
		return nil, err
	}
	out := make([]*provider.Channel, 0)
	for rows.Next() {
		var modelID2 string
		var c provider.Channel
		if err = rows.Scan(&c.ID, &modelID2, &c.Type, &c.Priority, &c.Status, &c.Endpoint, &c.Vendor, &c.UpstreamModel, &c.CredentialRef); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, &c)
	}
	rows.Close()
	return out, rows.Err()
}

// CreateChannel 写入通道。model 不存在返 ErrModelNotFound；主键冲突返 ErrChannelExists。
func (s *Store) CreateChannel(ctx context.Context, modelID string, c *provider.Channel) error {
	if c == nil || c.ID == "" {
		return fmt.Errorf("%w: channel 与 ID 不能为空", maas.ErrChannelExists)
	}
	ok, err := s.modelExists(ctx, modelID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", maas.ErrModelNotFound, modelID)
	}
	_, err = s.db.Pool().Exec(ctx, `
INSERT INTO maas_channels (id, model_id, type, priority, status, endpoint, vendor, upstream_model, credential_ref)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		c.ID, modelID, c.Type, c.Priority, c.Status, c.Endpoint, c.Vendor, c.UpstreamModel, c.CredentialRef)
	if err != nil {
		if storagepg.IsUniqueViolation(err) {
			return fmt.Errorf("%w: %s", maas.ErrChannelExists, c.ID)
		}
		return err
	}
	return nil
}

// UpdateChannel 仅更新通道标量（impl 不在此重建）。model/channel 不存在返对应错误。
func (s *Store) UpdateChannel(ctx context.Context, modelID string, c *provider.Channel) error {
	if c == nil || c.ID == "" {
		return maas.ErrChannelNotFound
	}
	ok, err := s.modelExists(ctx, modelID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", maas.ErrModelNotFound, modelID)
	}
	tag, err := s.db.Pool().Exec(ctx, `
UPDATE maas_channels SET type=$1, priority=$2, status=$3, endpoint=$4, vendor=$5, upstream_model=$6, credential_ref=$7
WHERE id=$8 AND model_id=$9`,
		c.Type, c.Priority, c.Status, c.Endpoint, c.Vendor, c.UpstreamModel, c.CredentialRef, c.ID, modelID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s/%s", maas.ErrChannelNotFound, modelID, c.ID)
	}
	return nil
}

// DeleteChannel 删除通道。model/channel 不存在返对应错误。
func (s *Store) DeleteChannel(ctx context.Context, modelID, channelID string) error {
	ok, err := s.modelExists(ctx, modelID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: %s", maas.ErrModelNotFound, modelID)
	}
	tag, err := s.db.Pool().Exec(ctx, `DELETE FROM maas_channels WHERE id=$1 AND model_id=$2`, channelID, modelID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: %s/%s", maas.ErrChannelNotFound, modelID, channelID)
	}
	return nil
}

// ModelsCount 返回模型总数，供 PG seed 判空（表空才灌 catalog，幂等）。
func (s *Store) ModelsCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM maas_models`).Scan(&n)
	return n, err
}
