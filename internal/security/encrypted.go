// encrypted.go Secret 静态加密装饰器：写路径（CreateSecret）加密 Value，Resolve 解密；
// List/Get 掩码直通（掩码无密文前缀，Decrypt 透传无害）。cipher nil = 明文模式
// （dev 未配 master key），全链路行为与现状一致。
package security

import (
	"context"

	"github.com/aitoys/paas/internal/crypto"
)

// NewEncryptedRepo 包装 Repository：CreateSecret 加密 / Resolve 解密，其余直通。
func NewEncryptedRepo(inner Repository, c *crypto.Cipher) Repository {
	return &encryptedRepo{inner: inner, c: c}
}

type encryptedRepo struct {
	inner Repository
	c     *crypto.Cipher
}

func (e *encryptedRepo) ListSecrets(ctx context.Context) ([]Secret, error) {
	return e.inner.ListSecrets(ctx) // 掩码直通
}

func (e *encryptedRepo) GetSecret(ctx context.Context, id string) (Secret, error) {
	return e.inner.GetSecret(ctx, id) // 掩码直通
}

func (e *encryptedRepo) ListAllSecrets(ctx context.Context) ([]Secret, error) {
	return e.inner.ListAllSecrets(ctx) // 掩码直通（admin 总览）
}

// CreateSecret 先加密 Value 再透传 inner；返回值保持 inner 的掩码返回。
func (e *encryptedRepo) CreateSecret(ctx context.Context, s Secret) (Secret, error) {
	enc, err := e.c.Encrypt(s.Value)
	if err != nil {
		return Secret{}, err
	}
	s.Value = enc
	return e.inner.CreateSecret(ctx, s)
}

func (e *encryptedRepo) DeleteSecret(ctx context.Context, id string) error {
	return e.inner.DeleteSecret(ctx, id)
}

// Resolve 解密明文消费点：inner 返回后解密 Value（存量无前缀明文原样透出）。
func (e *encryptedRepo) Resolve(ctx context.Context, id string) (Secret, error) {
	s, err := e.inner.Resolve(ctx, id)
	if err != nil {
		return s, err
	}
	plain, err := e.c.Decrypt(s.Value)
	if err != nil {
		return Secret{}, err
	}
	s.Value = plain
	return s, nil
}

// —— AuditStore 直通（审计不含敏感明文）——

func (e *encryptedRepo) ListAuditLogs(ctx context.Context, resourceType, action string) ([]AuditLog, error) {
	return e.inner.ListAuditLogs(ctx, resourceType, action)
}

func (e *encryptedRepo) RecordAudit(ctx context.Context, log AuditLog) error {
	return e.inner.RecordAudit(ctx, log)
}

func (e *encryptedRepo) ListAllAuditLogs(ctx context.Context) ([]AuditLog, error) {
	return e.inner.ListAllAuditLogs(ctx)
}
