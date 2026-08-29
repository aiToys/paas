package dataservice_test

import (
	"context"
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/crypto"
	"github.com/aitoys/paas/internal/dataservice"
)

// fakeDSStore 可注入 fake inner：记录写请求、读返回预置数据，
// 直接断言装饰器传给 inner 的 Connection 形态（memory/pg store 读路径掩码无法直查密文）。
type fakeDSStore struct {
	// lastCreate/lastUpdate 记录最近一次写请求收到的对象。
	lastCreate dataservice.DataService
	lastUpdate dataservice.DataService
	// preset 预置读返回（Get/GetAny/List/ListAll 均返回它）。
	preset dataservice.DataService
}

func (f *fakeDSStore) List(ctx context.Context, kind string) ([]dataservice.DataService, error) {
	return []dataservice.DataService{f.preset}, nil
}
func (f *fakeDSStore) ListAll(ctx context.Context) ([]dataservice.DataService, error) {
	return []dataservice.DataService{f.preset}, nil
}
func (f *fakeDSStore) Get(ctx context.Context, id string) (dataservice.DataService, error) {
	return f.preset, nil
}
func (f *fakeDSStore) GetAny(ctx context.Context, id string) (dataservice.DataService, error) {
	return f.preset, nil
}
func (f *fakeDSStore) Create(ctx context.Context, d dataservice.DataService) (dataservice.DataService, error) {
	f.lastCreate = d
	return d, nil
}
func (f *fakeDSStore) Update(ctx context.Context, d dataservice.DataService) (dataservice.DataService, error) {
	f.lastUpdate = d
	return d, nil
}
func (f *fakeDSStore) Delete(ctx context.Context, id string) error { return nil }

// dsTestCipher 测试 cipher（64 位 hex）。
func dsTestCipher(t *testing.T) *crypto.Cipher {
	t.Helper()
	c, err := crypto.NewFromHex(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// sampleConn 样例连接信息（敏感 + 非敏感混合）。
func sampleConn() map[string]string {
	return map[string]string{
		"password": "p@ss",
		"host":     "db.paas.svc.cluster.local",
		"port":     "5432",
	}
}

// 用例 1：Create 经装饰器后 inner 收到 password 带 CipherPrefix 的密文，host/port 原样明文。
func TestCreateEncryptsSensitiveFields(t *testing.T) {
	fake := &fakeDSStore{}
	repo := dataservice.NewEncryptedRepo(fake, dsTestCipher(t))

	_, err := repo.Create(context.Background(), dataservice.DataService{ID: "ds1", Connection: sampleConn()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fake.lastCreate.Connection["password"], crypto.CipherPrefix) {
		t.Fatalf("inner 收到未加密 password: %q", fake.lastCreate.Connection["password"])
	}
	if fake.lastCreate.Connection["host"] != "db.paas.svc.cluster.local" {
		t.Fatalf("host 应明文保留: %q", fake.lastCreate.Connection["host"])
	}
	if fake.lastCreate.Connection["port"] != "5432" {
		t.Fatalf("port 应明文保留: %q", fake.lastCreate.Connection["port"])
	}
}

// 用例 2：fake inner 预置密文 → Get 返回解密后的明文。
func TestGetDecryptsSensitiveFields(t *testing.T) {
	c := dsTestCipher(t)
	enc, err := c.Encrypt("real-pass")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeDSStore{preset: dataservice.DataService{
		ID: "ds2", Connection: map[string]string{"password": enc, "host": "h"},
	}}
	repo := dataservice.NewEncryptedRepo(fake, c)

	got, err := repo.Get(context.Background(), "ds2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Connection["password"] != "real-pass" {
		t.Fatalf("Get 未解密 password: %q", got.Connection["password"])
	}
	if got.Connection["host"] != "h" {
		t.Fatalf("host 不应被动: %q", got.Connection["host"])
	}
}

// 用例 3：fake inner 预置无前缀明文（存量数据）→ 原样透出。
func TestLegacyPlaintextReadable(t *testing.T) {
	fake := &fakeDSStore{preset: dataservice.DataService{
		ID: "ds3", Connection: map[string]string{"password": "legacy-plain"},
	}}
	repo := dataservice.NewEncryptedRepo(fake, dsTestCipher(t))

	got, err := repo.Get(context.Background(), "ds3")
	if err != nil {
		t.Fatal(err)
	}
	if got.Connection["password"] != "legacy-plain" {
		t.Fatalf("存量明文应原样透出: %q", got.Connection["password"])
	}
}

// 用例 4：cipher nil（dev 明文模式）→ 写读全透传。
func TestNilCipherPassthrough(t *testing.T) {
	fake := &fakeDSStore{preset: dataservice.DataService{
		ID: "ds4", Connection: sampleConn(),
	}}
	repo := dataservice.NewEncryptedRepo(fake, nil)

	if _, err := repo.Create(context.Background(), dataservice.DataService{ID: "ds4", Connection: sampleConn()}); err != nil {
		t.Fatal(err)
	}
	if fake.lastCreate.Connection["password"] != "p@ss" {
		t.Fatalf("nil cipher 写应透传明文: %q", fake.lastCreate.Connection["password"])
	}
	got, err := repo.Get(context.Background(), "ds4")
	if err != nil {
		t.Fatal(err)
	}
	if got.Connection["password"] != "p@ss" {
		t.Fatalf("nil cipher 读应透传明文: %q", got.Connection["password"])
	}
}

// 用例 5：Update 经装饰器后 inner 收到加密 Connection。
func TestUpdateEncryptsConnection(t *testing.T) {
	fake := &fakeDSStore{}
	repo := dataservice.NewEncryptedRepo(fake, dsTestCipher(t))

	if _, err := repo.Update(context.Background(), dataservice.DataService{ID: "ds5", Connection: sampleConn()}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fake.lastUpdate.Connection["password"], crypto.CipherPrefix) {
		t.Fatalf("inner 未收到加密 password: %q", fake.lastUpdate.Connection["password"])
	}
	if fake.lastUpdate.Connection["host"] != "db.paas.svc.cluster.local" {
		t.Fatalf("host 应明文保留: %q", fake.lastUpdate.Connection["host"])
	}
}
