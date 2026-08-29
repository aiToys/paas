package security_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/crypto"
	"github.com/aitoys/paas/internal/security"
	secmemory "github.com/aitoys/paas/internal/security/memory"
	"github.com/aitoys/paas/pkg/tenant"
)

// fakeSecStore 可注入 fake inner：记录写请求、读返回预置数据，
// 用于直接断言装饰器传给 inner 的 Value 形态（memory store 读路径全掩码无法直查）。
type fakeSecStore struct {
	// lastCreateValue 记录最近一次 CreateSecret 收到的 Value（密文断言用）。
	lastCreateValue string
	// resolveSecret Resolve 返回的预置 Secret（Value 原样透出）。
	resolveSecret security.Secret
}

func (f *fakeSecStore) ListSecrets(ctx context.Context) ([]security.Secret, error) {
	return nil, nil
}
func (f *fakeSecStore) GetSecret(ctx context.Context, id string) (security.Secret, error) {
	return security.Secret{}, nil
}
func (f *fakeSecStore) CreateSecret(ctx context.Context, s security.Secret) (security.Secret, error) {
	f.lastCreateValue = s.Value
	return s, nil
}
func (f *fakeSecStore) DeleteSecret(ctx context.Context, id string) error { return nil }
func (f *fakeSecStore) Resolve(ctx context.Context, id string) (security.Secret, error) {
	return f.resolveSecret, nil
}
func (f *fakeSecStore) ListAllSecrets(ctx context.Context) ([]security.Secret, error) {
	return nil, nil
}
func (f *fakeSecStore) ListAuditLogs(ctx context.Context, rt, a string) ([]security.AuditLog, error) {
	return nil, nil
}
func (f *fakeSecStore) RecordAudit(ctx context.Context, l security.AuditLog) error { return nil }
func (f *fakeSecStore) ListAllAuditLogs(ctx context.Context) ([]security.AuditLog, error) {
	return nil, nil
}

// 测试 cipher（64 位 hex）。
func testCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	c, err := crypto.NewFromHex(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// 用例 1：CreateSecret 经装饰器后 inner 收到带 CipherPrefix 的密文。
func TestCreateEncryptsValue(t *testing.T) {
	fake := &fakeSecStore{}
	repo := security.NewEncryptedRepo(fake, testCipher(t))
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	_, err := repo.CreateSecret(ctx, security.Secret{Name: "db-pass", Type: security.TypeSecret, Value: "p@ss"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fake.lastCreateValue, crypto.CipherPrefix) {
		t.Fatalf("inner 未收到密文: %q", fake.lastCreateValue)
	}
	if strings.Contains(fake.lastCreateValue, "p@ss") {
		t.Fatal("inner 收到明文")
	}
}

// 用例 2：fake inner Resolve 返回预加密密文 → 装饰器解密返明文。
func TestResolveDecrypts(t *testing.T) {
	c := testCipher(t)
	enc, err := c.Encrypt("real-secret")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeSecStore{resolveSecret: security.Secret{ID: "s1", Name: "k", Type: security.TypeSecret, Value: enc}}
	repo := security.NewEncryptedRepo(fake, c)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	got, err := repo.Resolve(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "real-secret" {
		t.Fatalf("Resolve 未解密: %q", got.Value)
	}
}

// 用例 3：cipher nil → inner 收到原明文（dev 明文模式透传）。
func TestNilCipherPassthrough(t *testing.T) {
	fake := &fakeSecStore{}
	repo := security.NewEncryptedRepo(fake, nil)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	_, err := repo.CreateSecret(ctx, security.Secret{Name: "k", Type: security.TypeSecret, Value: "plain"})
	if err != nil {
		t.Fatal(err)
	}
	if fake.lastCreateValue != "plain" {
		t.Fatalf("nil cipher 应透传明文: %q", fake.lastCreateValue)
	}
}

// 用例 4：fake inner 返回无前缀明文（存量数据）→ Resolve 原样透出。
func TestDecryptPlaintextLegacy(t *testing.T) {
	fake := &fakeSecStore{resolveSecret: security.Secret{ID: "s2", Name: "k", Type: security.TypeSecret, Value: "legacy-plain"}}
	repo := security.NewEncryptedRepo(fake, testCipher(t))
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	got, err := repo.Resolve(ctx, "s2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != "legacy-plain" {
		t.Fatalf("存量明文应原样透出: %q", got.Value)
	}
}

// 用例 5：memory store 冒烟——全链路掩码语义不变（List/Create 返回掩码，非平台级 Resolve 拒绝）。
func TestResolveMaskedFromInner(t *testing.T) {
	c := testCipher(t)
	inner := secmemory.NewStore()
	repo := security.NewEncryptedRepo(inner, c)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	saved, err := repo.CreateSecret(ctx, security.Secret{Name: "k", Type: security.TypeSecret, Value: "super-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Value != security.SecretMask {
		t.Fatalf("Create 返回应仍为掩码: %q", saved.Value)
	}
	list, err := repo.ListSecrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.Value != security.SecretMask {
			t.Fatalf("List 应仍为掩码: %q", s.Value)
		}
	}
}
