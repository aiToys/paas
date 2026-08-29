// encrypted.go ConfigItem secret 静态加密装饰器：Upsert 加密（掩码回写保护），
// ListPlain 解密（reconciler 注入消费点）；List 掩码直通。
// cipher nil = 明文模式（dev 未配 master key），行为与现状一致（掩码保护仍生效）。
package appconfig

import (
	"context"

	"github.com/aitoys/paas/internal/crypto"
)

// NewEncryptedRepo 包装 Repository。
func NewEncryptedRepo(inner Repository, c *crypto.Cipher) Repository {
	return &encryptedRepo{inner: inner, c: c}
}

type encryptedRepo struct {
	inner Repository
	c     *crypto.Cipher
}

func (e *encryptedRepo) List(ctx context.Context, appID, envID string) ([]ConfigItem, error) {
	return e.inner.List(ctx, appID, envID) // 掩码直通
}

// ListPlain 明文消费点：secret 项逐项解密（存量无前缀明文透传）；env 项不动。
func (e *encryptedRepo) ListPlain(ctx context.Context, appID, envID string) ([]ConfigItem, error) {
	items, err := e.inner.ListPlain(ctx, appID, envID)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if items[i].Type == TypeSecret {
			plain, err := e.c.Decrypt(items[i].Value)
			if err != nil {
				return nil, err
			}
			items[i].Value = plain
		}
	}
	return items, nil
}

// Upsert：非 secret 直通；secret 先做掩码回写保护再加密。
func (e *encryptedRepo) Upsert(ctx context.Context, item ConfigItem) (ConfigItem, error) {
	if item.Type != TypeSecret {
		return e.inner.Upsert(ctx, item)
	}
	// 掩码回写保护：前端编辑 secret 不回填值（提交掩码），按字面写入会覆盖真实凭证。
	if item.Value == SecretMask {
		orig, err := e.findOriginal(ctx, item)
		if err != nil {
			return ConfigItem{}, err
		}
		if orig != nil {
			item.Value = orig.Value // 库中原值（密文/明文形态原样回写，不破坏）
			return e.inner.Upsert(ctx, item)
		}
		// 找不到原值（首次创建就填掩码）：按字面写入（异常输入，不静默吞）。
	}
	enc, err := e.c.Encrypt(item.Value)
	if err != nil {
		return ConfigItem{}, err
	}
	item.Value = enc
	return e.inner.Upsert(ctx, item)
}

// findOriginal 按 (appID, envID, key) 查库中原值。ListPlain 拿存储形态原值（回写保持形态不变）。
func (e *encryptedRepo) findOriginal(ctx context.Context, item ConfigItem) (*ConfigItem, error) {
	items, err := e.inner.ListPlain(ctx, item.AppID, item.EnvID)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.Key == item.Key {
			return &it, nil
		}
	}
	return nil, nil
}

func (e *encryptedRepo) Delete(ctx context.Context, id string) error {
	return e.inner.Delete(ctx, id)
}
