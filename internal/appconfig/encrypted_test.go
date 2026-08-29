package appconfig_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/appconfig"
	appcfgmemory "github.com/aitoys/paas/internal/appconfig/memory"
	"github.com/aitoys/paas/internal/crypto"
	"github.com/aitoys/paas/pkg/tenant"
)

// fakeCfgStore 可注入 fake inner：记录最近一次 Upsert 收到的 Value，
// ListPlain 返回预置行——直接断言装饰器写入/解密行为。
type fakeCfgStore struct {
	lastUpsertValue string
	// plainItems ListPlain 返回的预置行（Value 原样透出）。
	plainItems []appconfig.ConfigItem
}

func (f *fakeCfgStore) List(ctx context.Context, appID, envID string) ([]appconfig.ConfigItem, error) {
	return nil, nil
}
func (f *fakeCfgStore) ListPlain(ctx context.Context, appID, envID string) ([]appconfig.ConfigItem, error) {
	return f.plainItems, nil
}
func (f *fakeCfgStore) Upsert(ctx context.Context, item appconfig.ConfigItem) (appconfig.ConfigItem, error) {
	f.lastUpsertValue = item.Value
	return item, nil
}
func (f *fakeCfgStore) Delete(ctx context.Context, id string) error { return nil }

func testCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	c, err := crypto.NewFromHex(strings.Repeat("cd", 32))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// 用例 1：type=secret → inner 收到带前缀密文。
func TestUpsertEncryptsSecret(t *testing.T) {
	fake := &fakeCfgStore{}
	repo := appconfig.NewEncryptedRepo(fake, testCipher(t))
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	_, err := repo.Upsert(ctx, appconfig.ConfigItem{AppID: "app-1", EnvID: "e1", Key: "DB_PASS", Value: "p@ss", Type: appconfig.TypeSecret})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fake.lastUpsertValue, crypto.CipherPrefix) {
		t.Fatalf("inner 未收到密文: %q", fake.lastUpsertValue)
	}
}

// 用例 2：提交掩码值 + 库中有原值 → inner 收到原值（掩码回写保护，原值保持存储形态）。
func TestUpsertMaskPreservesOriginal(t *testing.T) {
	c := testCipher(t)
	// 库中原值以密文形态存储（ListPlain 拿到的即存储形态）。
	encOrig, err := c.Encrypt("real-original")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeCfgStore{plainItems: []appconfig.ConfigItem{
		{ID: "cfg-1", AppID: "app-1", EnvID: "e1", Key: "DB_PASS", Value: encOrig, Type: appconfig.TypeSecret},
	}}
	repo := appconfig.NewEncryptedRepo(fake, c)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	_, err = repo.Upsert(ctx, appconfig.ConfigItem{AppID: "app-1", EnvID: "e1", Key: "DB_PASS", Value: appconfig.SecretMask, Type: appconfig.TypeSecret})
	if err != nil {
		t.Fatal(err)
	}
	if fake.lastUpsertValue != encOrig {
		t.Fatalf("掩码应替换为库中原值: %q", fake.lastUpsertValue)
	}
}

// 用例 3：type=env → 直通明文不加密。
func TestUpsertEnvNotEncrypted(t *testing.T) {
	fake := &fakeCfgStore{}
	repo := appconfig.NewEncryptedRepo(fake, testCipher(t))
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	_, err := repo.Upsert(ctx, appconfig.ConfigItem{AppID: "app-1", EnvID: "e1", Key: "LOG_LEVEL", Value: "debug", Type: appconfig.TypeEnv})
	if err != nil {
		t.Fatal(err)
	}
	if fake.lastUpsertValue != "debug" {
		t.Fatalf("env 类型应明文直通: %q", fake.lastUpsertValue)
	}
}

// 用例 4：ListPlain 返回 secret 行解密（env 行不动；存量无前缀明文原样）。
func TestListPlainDecrypts(t *testing.T) {
	c := testCipher(t)
	enc, err := c.Encrypt("injected-secret")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeCfgStore{plainItems: []appconfig.ConfigItem{
		{Key: "DB_PASS", Value: enc, Type: appconfig.TypeSecret},
		{Key: "LOG_LEVEL", Value: "info", Type: appconfig.TypeEnv},
		{Key: "LEGACY", Value: "legacy-plain", Type: appconfig.TypeSecret}, // 存量明文
	}}
	repo := appconfig.NewEncryptedRepo(fake, c)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	items, err := repo.ListPlain(ctx, "app-1", "e1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"DB_PASS": "injected-secret", "LOG_LEVEL": "info", "LEGACY": "legacy-plain"}
	for _, it := range items {
		if it.Value != want[it.Key] {
			t.Fatalf("key=%s 得 %q 期望 %q", it.Key, it.Value, want[it.Key])
		}
	}
}

// 用例 5：nil cipher 全透传 + 掩码回写保护在 nil cipher 下仍生效。
func TestNilCipherPassthrough(t *testing.T) {
	fake := &fakeCfgStore{plainItems: []appconfig.ConfigItem{
		{Key: "DB_PASS", Value: "stored-plain", Type: appconfig.TypeSecret},
	}}
	repo := appconfig.NewEncryptedRepo(fake, nil)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	// 普通写入：明文透传。
	if _, err := repo.Upsert(ctx, appconfig.ConfigItem{AppID: "a", EnvID: "e", Key: "DB_PASS", Value: "plain", Type: appconfig.TypeSecret}); err != nil {
		t.Fatal(err)
	}
	if fake.lastUpsertValue != "plain" {
		t.Fatalf("nil cipher 应透传明文: %q", fake.lastUpsertValue)
	}
	// 掩码提交：仍触发查原值保护（不把掩码字面写入）。
	if _, err := repo.Upsert(ctx, appconfig.ConfigItem{AppID: "a", EnvID: "e", Key: "DB_PASS", Value: appconfig.SecretMask, Type: appconfig.TypeSecret}); err != nil {
		t.Fatal(err)
	}
	if fake.lastUpsertValue != "stored-plain" {
		t.Fatalf("nil cipher 下掩码保护应仍生效: %q", fake.lastUpsertValue)
	}
}

// memory store 冒烟：全链路无 panic，List 掩码语义不变。
func TestMemoryStoreSmoke(t *testing.T) {
	c := testCipher(t)
	repo := appconfig.NewEncryptedRepo(appcfgmemory.NewStore(), c)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	saved, err := repo.Upsert(ctx, appconfig.ConfigItem{AppID: "app-1", EnvID: "e1", Key: "K", Value: "v", Type: appconfig.TypeSecret})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Value != appconfig.SecretMask {
		t.Fatalf("Upsert 返回应仍为掩码: %q", saved.Value)
	}
	list, err := repo.List(ctx, "app-1", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Value != appconfig.SecretMask {
		t.Fatalf("List 掩码语义应不变: %+v", list)
	}
}
