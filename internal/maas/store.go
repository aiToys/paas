package maas

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/aitoys/paas/pkg/provider"
)

// Provider 类型常量（Channel.Type）。BuildProvider 据此构造运行时 Provider。
const (
	ProviderEcho             = "echo"              // 进程内回显（演示）
	ProviderMock             = "mock"              // 进程内预设文本（演示）
	ProviderOpenAICompatible = "openai-compatible" // 第三方供应商 OpenAI 兼容协议
)

// mockReplyText 是 mock 通道的固定演示回复。
// mock 通道仅用于演示路由/降级，生产通道应为 openai-compatible；不为演示文本增加 DB 字段（YAGNI）。
const mockReplyText = "（mock 演示回复：模型管理创建的 mock 通道）"

// 仓储错误。与各模块 sentinel 模式一致，handler 据此映射 HTTP 状态。
var (
	ErrModelNotFound   = errors.New("模型不存在")
	ErrModelExists     = errors.New("模型已存在")
	ErrChannelNotFound = errors.New("通道不存在")
	ErrChannelExists   = errors.New("通道已存在")
	ErrVendorNotFound  = errors.New("供应商不存在")
	ErrVendorExists    = errors.New("供应商已存在")
)

// Repository 是 Model/Channel/Vendor 的平台级仓储。
// 模型目录全租户共享（CLAUDE.md 已定），故不带 tenant ctx（与租户 Repository 模式不同）；
// 仍带 context.Context 以传播请求取消/超时（与其他模块一致）。
// Channel 作为 Model 的子资源管理（内嵌 model.Channels，与 provider.Model 结构同源）。
type Repository interface {
	// Model CRUD（平台级）。
	ListModels(ctx context.Context) ([]*provider.Model, error)
	GetModel(ctx context.Context, id string) (*provider.Model, error) // not found 返 ErrModelNotFound
	CreateModel(ctx context.Context, m *provider.Model) error
	UpdateModel(ctx context.Context, m *provider.Model) error // 仅更新标量字段，Channels 由 Channel CRUD 单独管理（不被覆盖）
	DeleteModel(ctx context.Context, id string) error         // 级联清其下通道

	// Channel CRUD（模型子资源）。modelID 不存在返 ErrModelNotFound。
	ListChannels(ctx context.Context, modelID string) ([]*provider.Channel, error)
	CreateChannel(ctx context.Context, modelID string, c *provider.Channel) error
	UpdateChannel(ctx context.Context, modelID string, c *provider.Channel) error // 仅更新标量，impl 不在此重建
	DeleteChannel(ctx context.Context, modelID, channelID string) error

	// Vendor CRUD（平台级预设供应商：BaseURL+凭证+Type，供创建通道时选供应商带入）。
	ListVendors(ctx context.Context) ([]*provider.Vendor, error)
	GetVendor(ctx context.Context, id string) (*provider.Vendor, error) // not found 返 ErrVendorNotFound
	CreateVendor(ctx context.Context, v *provider.Vendor) error
	UpdateVendor(ctx context.Context, v *provider.Vendor) error // 仅更新标量
	DeleteVendor(ctx context.Context, id string) error
	VendorsCount(ctx context.Context) (int, error) // 供 seed 判空（表空才灌，幂等）
}

// memoryStore 是 Repository 的进程内实现，cmd/core 内存路径注入。
// 所有读方法返回深拷贝（Clone），隔离调用方与内部状态，避免锁外读 Channel.Status 竞态。
type memoryStore struct {
	mu      sync.Mutex
	models  map[string]*provider.Model
	vendors map[string]*provider.Vendor
}

// NewMemoryStore 返回空内存仓储（不 seed；demo 灌入由 plugin/cmd 层门控）。
func NewMemoryStore() Repository {
	return &memoryStore{
		models:  map[string]*provider.Model{},
		vendors: map[string]*provider.Vendor{},
	}
}

func (s *memoryStore) ListModels(ctx context.Context) ([]*provider.Model, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*provider.Model, 0, len(s.models))
	for _, m := range s.models {
		out = append(out, m.Clone())
	}
	return out, ctx.Err()
}

func (s *memoryStore) GetModel(ctx context.Context, id string) (*provider.Model, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.models[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrModelNotFound, id)
	}
	return m.Clone(), nil
}

func (s *memoryStore) CreateModel(ctx context.Context, m *provider.Model) error {
	if m == nil || m.ID == "" {
		return fmt.Errorf("%w: model 与 ID 不能为空", ErrModelExists)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.models[m.ID]; exists {
		return fmt.Errorf("%w: %s", ErrModelExists, m.ID)
	}
	cp := m.Clone()
	cp.Channels = nil // channels 由 CreateChannel 单独管理（避免 CreateModel 带 channels 后 seed 再 CreateChannel 重复）
	s.models[m.ID] = cp
	return nil
}

func (s *memoryStore) UpdateModel(ctx context.Context, m *provider.Model) error {
	if m == nil || m.ID == "" {
		return ErrModelNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.models[m.ID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrModelNotFound, m.ID)
	}
	// 仅更新标量字段，Channels 保留存储现状（channel CRUD 单独管理，避免 update 误清通道）。
	existing.Name = m.Name
	existing.Vendor = m.Vendor
	existing.ContextWindow = m.ContextWindow
	existing.InputPrice = m.InputPrice
	existing.OutputPrice = m.OutputPrice
	existing.Description = m.Description
	if m.Capabilities != nil {
		existing.Capabilities = append([]string(nil), m.Capabilities...)
	}
	return nil
}

func (s *memoryStore) DeleteModel(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.models[id]; !ok {
		return fmt.Errorf("%w: %s", ErrModelNotFound, id)
	}
	delete(s.models, id) // channels 内嵌，随 model 级联清
	return nil
}

func (s *memoryStore) ListChannels(ctx context.Context, modelID string) ([]*provider.Channel, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.models[modelID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrModelNotFound, modelID)
	}
	out := make([]*provider.Channel, 0, len(m.Channels))
	for _, c := range m.Channels {
		out = append(out, c.Clone())
	}
	return out, nil
}

func (s *memoryStore) CreateChannel(ctx context.Context, modelID string, c *provider.Channel) error {
	if c == nil || c.ID == "" {
		return fmt.Errorf("%w: channel 与 ID 不能为空", ErrChannelExists)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.models[modelID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrModelNotFound, modelID)
	}
	for _, ex := range m.Channels {
		if ex.ID == c.ID {
			return fmt.Errorf("%w: %s", ErrChannelExists, c.ID)
		}
	}
	m.Channels = append(m.Channels, c.Clone())
	return nil
}

func (s *memoryStore) UpdateChannel(ctx context.Context, modelID string, c *provider.Channel) error {
	if c == nil || c.ID == "" {
		return ErrChannelNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.models[modelID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrModelNotFound, modelID)
	}
	for _, ex := range m.Channels {
		if ex.ID == c.ID {
			// 仅更新标量；impl 不在此重建（impl 不持久化，由调用方 BuildProvider+SetImpl 后刷新 gateway）。
			ex.Type = c.Type
			ex.Priority = c.Priority
			ex.Status = c.Status
			ex.Endpoint = c.Endpoint
			ex.Vendor = c.Vendor
			ex.UpstreamModel = c.UpstreamModel
			ex.CredentialRef = c.CredentialRef
			ex.VendorID = c.VendorID
			return nil
		}
	}
	return fmt.Errorf("%w: %s/%s", ErrChannelNotFound, modelID, c.ID)
}

func (s *memoryStore) DeleteChannel(ctx context.Context, modelID, channelID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.models[modelID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrModelNotFound, modelID)
	}
	for i, ex := range m.Channels {
		if ex.ID == channelID {
			m.Channels = append(m.Channels[:i], m.Channels[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("%w: %s/%s", ErrChannelNotFound, modelID, channelID)
}

// vendorClone 返回 Vendor 的深拷贝（全标量字段，值复制即可）。
func vendorClone(v *provider.Vendor) *provider.Vendor {
	if v == nil {
		return nil
	}
	cp := *v
	return &cp
}

func (s *memoryStore) ListVendors(ctx context.Context) ([]*provider.Vendor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*provider.Vendor, 0, len(s.vendors))
	for _, v := range s.vendors {
		out = append(out, vendorClone(v))
	}
	return out, ctx.Err()
}

func (s *memoryStore) GetVendor(ctx context.Context, id string) (*provider.Vendor, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.vendors[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrVendorNotFound, id)
	}
	return vendorClone(v), nil
}

func (s *memoryStore) CreateVendor(ctx context.Context, v *provider.Vendor) error {
	if v == nil || v.ID == "" {
		return fmt.Errorf("%w: vendor 与 ID 不能为空", ErrVendorExists)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.vendors[v.ID]; exists {
		return fmt.Errorf("%w: %s", ErrVendorExists, v.ID)
	}
	s.vendors[v.ID] = vendorClone(v)
	return nil
}

func (s *memoryStore) UpdateVendor(ctx context.Context, v *provider.Vendor) error {
	if v == nil || v.ID == "" {
		return ErrVendorNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.vendors[v.ID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrVendorNotFound, v.ID)
	}
	existing.Name = v.Name
	existing.Type = v.Type
	existing.BaseURL = v.BaseURL
	existing.CredentialRef = v.CredentialRef
	existing.Description = v.Description
	return nil
}

func (s *memoryStore) DeleteVendor(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.vendors[id]; !ok {
		return fmt.Errorf("%w: %s", ErrVendorNotFound, id)
	}
	delete(s.vendors, id)
	return nil
}

func (s *memoryStore) VendorsCount(ctx context.Context) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.vendors), ctx.Err()
}

// BuildProvider 按 Channel.Type 构造运行时 Provider。
//   - echo/mock：进程内演示，不依赖外部配置；
//   - openai-compatible：用 Endpoint/UpstreamModel/CredentialRef + resolver 转发真实供应商。
//
// resolver 为 nil 时 openai-compatible 通道仍可构造（Chat 时返 ErrCredentialMissing，不阻断注册）。
// 未知 Type 返回 nil（调用方应忽略并标通道 offline/failed）。
func BuildProvider(ch *provider.Channel, resolver provider.CredentialResolver) provider.Provider {
	if ch == nil {
		return nil
	}
	switch ch.Type {
	case ProviderEcho:
		return EchoProvider{}
	case ProviderMock:
		return NewMockProvider(mockReplyText)
	case ProviderOpenAICompatible:
		return NewOpenAICompatibleProvider(ch.Vendor, ch.Endpoint, ch.UpstreamModel, ch.CredentialRef, resolver, nil)
	default:
		return nil
	}
}
