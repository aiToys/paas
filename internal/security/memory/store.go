// Package memory 提供 security.Repository 的内存实现，seed 跨两租户示例密钥与审计。
package memory

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/aitoys/paas/internal/security"
	"github.com/aitoys/paas/pkg/tenant"
)

// Store 实现 security.Repository（SecretStore + AuditStore），单 Store 避免重名。
type Store struct {
	mu      sync.RWMutex
	secrets map[string]security.Secret
	audits  []security.AuditLog // 按追加顺序；List 时倒序返回
	seq     int
}

func NewStore() *Store {
	s := &Store{secrets: map[string]security.Secret{}}
	s.seed()
	return s
}

// —— Secret ——

// ListSecrets 返回「该租户的租户级 Secret」+「所有平台级 Secret」（均掩码）。
// 平台级凭证全租户共享（第三方供应商 Key），故不按租户过滤。
func (s *Store) ListSecrets(ctx context.Context) ([]security.Secret, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]security.Secret, 0)
	for _, sec := range s.secrets {
		if sec.Scope == security.ScopePlatform || sec.TenantID == tid {
			out = append(out, sec.Masked())
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListAllSecrets 跨租户列出全部密钥（admin 平台总览，掩码返回；含平台级+各租户级）。
// 按 TenantID 升序（平台级 TenantID 为空排最前）再 Name 升序。
func (s *Store) ListAllSecrets(ctx context.Context) ([]security.Secret, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]security.Secret, 0, len(s.secrets))
	for _, sec := range s.secrets {
		out = append(out, sec.Masked())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (s *Store) GetSecret(ctx context.Context, id string) (security.Secret, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return security.Secret{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sec, ok := s.secrets[id]
	// 租户级按租户隔离；平台级跨租户可读（掩码）。跨租户访问租户级 → not found 不泄漏。
	if !ok || (sec.Scope != security.ScopePlatform && sec.TenantID != tid) {
		return security.Secret{}, fmt.Errorf("密钥不存在: %s", id)
	}
	return sec.Masked(), nil
}

// CreateSecret 明文存储，返回掩码。平台级 TenantID 强制为空（全租户共享）。
func (s *Store) CreateSecret(ctx context.Context, sec security.Secret) (security.Secret, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return security.Secret{}, err
	}
	if err := sec.Validate(); err != nil {
		return security.Secret{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// 唯一性：平台级按 (scope=platform, name) 全局唯一；租户级按 (tenant, name)。
	for _, ex := range s.secrets {
		if sec.Scope == security.ScopePlatform {
			if ex.Scope == security.ScopePlatform && ex.Name == sec.Name {
				return security.Secret{}, fmt.Errorf("平台级密钥名已存在: %s", sec.Name)
			}
		} else if ex.Scope != security.ScopePlatform && ex.TenantID == tid && ex.Name == sec.Name {
			return security.Secret{}, fmt.Errorf("密钥名已存在: %s", sec.Name)
		}
	}
	s.seq++
	sec.ID = fmt.Sprintf("sec-%d-%d", time.Now().UnixNano(), s.seq)
	sec.UpdatedAt = time.Now()
	if sec.Scope == security.ScopePlatform {
		sec.TenantID = "" // 平台级无租户归属
	} else {
		sec.TenantID = tid
	}
	s.secrets[sec.ID] = sec
	return sec.Masked(), nil
}

func (s *Store) DeleteSecret(ctx context.Context, id string) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sec, ok := s.secrets[id]
	if !ok || (sec.Scope != security.ScopePlatform && sec.TenantID != tid) {
		return fmt.Errorf("密钥不存在: %s", id)
	}
	delete(s.secrets, id)
	return nil
}

// Resolve 按 ID 取**平台级** Secret 明文（供第三方供应商通道运行时解析 API Key）。
// 租户级 Secret 经此路径返回 not found，防止绕过掩码读明文。
func (s *Store) Resolve(ctx context.Context, id string) (security.Secret, error) {
	_ = ctx // 平台级不按租户过滤
	s.mu.RLock()
	defer s.mu.RUnlock()
	sec, ok := s.secrets[id]
	if !ok || sec.Scope != security.ScopePlatform {
		return security.Secret{}, fmt.Errorf("平台级密钥不存在: %s", id)
	}
	return sec, nil // 明文（不掩码，仅内存传给 Provider）
}

// —— Audit ——

func (s *Store) ListAuditLogs(ctx context.Context, resourceType, action string) ([]security.AuditLog, error) {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]security.AuditLog, 0)
	for _, l := range s.audits {
		if l.TenantID != tid {
			continue
		}
		if resourceType != "" && l.ResourceType != resourceType {
			continue
		}
		if action != "" && l.Action != action {
			continue
		}
		out = append(out, l)
	}
	// 倒序（最新在前）
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// ListAllAuditLogs 跨租户列出全部审计日志（admin 平台总览，不过滤 tenant；按 TenantID 升序再 At 倒序）。
func (s *Store) ListAllAuditLogs(ctx context.Context) ([]security.AuditLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]security.AuditLog, 0, len(s.audits))
	out = append(out, s.audits...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].At.After(out[j].At)
	})
	return out, nil
}

// RecordAudit 追加一条审计（actor 已由调用方填好）。
func (s *Store) RecordAudit(ctx context.Context, log security.AuditLog) error {
	tid, err := tenant.IDOrErr(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	log.ID = fmt.Sprintf("audit-%d-%d", time.Now().UnixNano(), s.seq)
	log.TenantID = tid
	if log.At.IsZero() {
		log.At = time.Now()
	}
	s.audits = append(s.audits, log)
	return nil
}

func (s *Store) seed() {
	for _, sec := range SeedSecrets() {
		s.secrets[sec.ID] = sec
	}
	s.audits = append(s.audits, SeedAuditLogs()...)
}

// SeedSecrets 返回预置密钥/证书资产（PG/内存同一真源，DRY）。
// 租户级 Secret 已带 TenantID；平台级 TenantID 为空（全租户共享，第三方供应商凭证占位）。
// 平台级 Value 留空占位，部署后由运维填写真实 Key。
func SeedSecrets() []security.Secret {
	t := time.Now()
	return []security.Secret{
		{ID: "sec-acme-db", TenantID: "t-acme", Scope: security.ScopeTenant, Name: "db-password", Type: security.TypeSecret, Value: "pg-super-secret-001", Desc: "主库密码", UpdatedAt: t},
		{ID: "sec-acme-tls", TenantID: "t-acme", Scope: security.ScopeTenant, Name: "api-tls-cert", Type: security.TypeCertificate, Value: "-----BEGIN CERTIFICATE-----...", Desc: "API 网关 TLS 证书", UpdatedAt: t},
		{ID: "sec-globex-token", TenantID: "t-globex", Scope: security.ScopeTenant, Name: "llm-api-token", Type: security.TypeSecret, Value: "sk-vendor-token-xyz", Desc: "第三方 LLM token", UpdatedAt: t},
		// 平台级供应商凭证（全租户共享，值空占位；部署后运维填写真实 Key）。
		// MaaS catalog 通道通过 CredentialRef 引用，运行时经 Resolve 取明文。
		{ID: "sec-platform-openai", TenantID: "", Scope: security.ScopePlatform, Name: "openai-api-key", Type: security.TypeSecret, Value: "", Desc: "OpenAI 供应商 API Key（部署后填写）", UpdatedAt: t},
		{ID: "sec-platform-deepseek", TenantID: "", Scope: security.ScopePlatform, Name: "deepseek-api-key", Type: security.TypeSecret, Value: "", Desc: "DeepSeek 供应商 API Key（部署后填写）", UpdatedAt: t},
		{ID: "sec-platform-qwen", TenantID: "", Scope: security.ScopePlatform, Name: "qwen-api-key", Type: security.TypeSecret, Value: "", Desc: "通义千问 DashScope API Key（部署后填写）", UpdatedAt: t},
		{ID: "sec-platform-airouter", TenantID: "", Scope: security.ScopePlatform, Name: "airouter-api-key", Type: security.TypeSecret, Value: os.Getenv("PAAS_AIROUTER_API_KEY"), Desc: "airouter LLM 网关 API Key（统一真实推理入口，部署时经 env 注入）", UpdatedAt: t},
	}
}

// SeedAuditLogs 返回预置审计记录（PG/内存同一真源，DRY）。
// 调用方负责按时间倒序查询；此处按时间正序返回（追加顺序）。
func SeedAuditLogs() []security.AuditLog {
	t := time.Now()
	return []security.AuditLog{
		{ID: "audit-acme-1", TenantID: "t-acme", Actor: "u-acme-admin", Action: security.ActionCreate, ResourceType: security.ResourceSecret, ResourceID: "sec-acme-db", Detail: "创建密钥 db-password", At: t.Add(-2 * time.Hour)},
		{ID: "audit-globex-1", TenantID: "t-globex", Actor: "u-globex-admin", Action: security.ActionCreate, ResourceType: security.ResourceSecret, ResourceID: "sec-globex-token", Detail: "创建密钥 llm-api-token", At: t.Add(-1 * time.Hour)},
	}
}
