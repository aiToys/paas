// encrypted.go DataService Connection 字段级静态加密装饰器：仅 maskKeys
// （password/secretKey/token/api_key/master_key/uri）加密，host/port/user/database
// 明文保留（排障可读）。写加密、读解密；无前缀密文透传（存量明文兼容）。
// cipher nil = 明文模式（dev 未配 master key），全链路行为与现状一致。
package dataservice

import (
	"context"

	"github.com/aitoys/paas/internal/crypto"
)

// NewEncryptedRepo 包装 Repository：Create/Update 加密 Connection 敏感字段，
// List/ListAll/Get/GetAny 解密，Delete 直通。
func NewEncryptedRepo(inner Repository, c *crypto.Cipher) Repository {
	return &encryptedRepo{inner: inner, c: c}
}

type encryptedRepo struct {
	inner Repository
	c     *crypto.Cipher
}

// encConn 加密 Connection 敏感字段（写路径）。加密失败保留原值
// （与 nil cipher 透传一致，不阻断主流程；读侧无前缀仍原样可读）。
func (e *encryptedRepo) encConn(d DataService) DataService {
	out := make(map[string]string, len(d.Connection))
	for k, v := range d.Connection {
		if maskKeys[k] && v != "" {
			if enc, err := e.c.Encrypt(v); err == nil {
				v = enc
			}
		}
		out[k] = v
	}
	d.Connection = out
	return d
}

// decConn 解密 Connection 敏感字段（读路径；无前缀存量明文透传）。
func (e *encryptedRepo) decConn(d DataService) DataService {
	out := make(map[string]string, len(d.Connection))
	for k, v := range d.Connection {
		if maskKeys[k] && v != "" {
			if plain, err := e.c.Decrypt(v); err == nil {
				v = plain
			}
		}
		out[k] = v
	}
	d.Connection = out
	return d
}

func (e *encryptedRepo) List(ctx context.Context, kind string) ([]DataService, error) {
	list, err := e.inner.List(ctx, kind)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i] = e.decConn(list[i])
	}
	return list, nil
}

func (e *encryptedRepo) ListAll(ctx context.Context) ([]DataService, error) {
	list, err := e.inner.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list {
		list[i] = e.decConn(list[i])
	}
	return list, nil
}

func (e *encryptedRepo) Get(ctx context.Context, id string) (DataService, error) {
	d, err := e.inner.Get(ctx, id)
	if err != nil {
		return d, err
	}
	return e.decConn(d), nil
}

func (e *encryptedRepo) GetAny(ctx context.Context, id string) (DataService, error) {
	d, err := e.inner.GetAny(ctx, id)
	if err != nil {
		return d, err
	}
	return e.decConn(d), nil
}

func (e *encryptedRepo) Create(ctx context.Context, d DataService) (DataService, error) {
	return e.inner.Create(ctx, e.encConn(d))
}

func (e *encryptedRepo) Update(ctx context.Context, d DataService) (DataService, error) {
	return e.inner.Update(ctx, e.encConn(d))
}

func (e *encryptedRepo) Delete(ctx context.Context, id string) error {
	return e.inner.Delete(ctx, id)
}
