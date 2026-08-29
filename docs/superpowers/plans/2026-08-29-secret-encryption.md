# Secret 静态加密实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 三处敏感数据（security.Secret / appconfig secret / dataservice Connection）AES-256-GCM 静态加密，存量明文零迁移兼容。

**Architecture:** `internal/crypto` 单一真源（前缀 `enc:v1:` 识别密文，无前缀=明文透传）；三模块各一个 Repository 装饰器在装配层包装（写加密/明文消费点解密）；master key 经 `PAAS_SECRET_MASTER_KEY` env 注入，生产强制。

**Tech Stack:** Go 标准库 crypto/aes + crypto/cipher（GCM），零新依赖。

**Spec:** `docs/superpowers/specs/2026-08-29-secret-encryption-design.md`

## Global Constraints

- 密文格式 `enc:v1:<base64(nonce+ciphertext+tag)>`，`Decrypt` 无前缀原样返回（存量兼容）
- cipher 为 nil（dev 未设 key）= 明文兼容模式，全链路行为与现状一致
- `PAAS_PROD=true` 且未设/非法 key → 拒绝启动；dev 未设 → 启动 WARNING
- dataservice 仅 SENSITIVE_KEYS 字段加密（password/secretKey/token/api_key/master_key/uri），host/port/user/database 明文
- appconfig Upsert 收到掩码值（`••••••`）不覆盖库中原值（顺带修既有 bug）
- 6 个 store（memory/pg × 3 模块）内部零改动；唯二例外：security pg Store 加 seed 加密选项、装配层包装
- 注释中文；`{data:T}` 契约不变；不引外部依赖
- 生产禁 BestEffort 等既有校验不动

---

### Task 1: internal/crypto 包

**Files:**
- Create: `internal/crypto/crypto.go`
- Test: `internal/crypto/crypto_test.go`

**Interfaces:**
- Produces: `crypto.Cipher`（`New([]byte) (*Cipher, error)` / `NewFromHex(string) (*Cipher, error)` / `NewFromEnv(string) (*Cipher, *Cipher, error)` 语义见下 / `Encrypt(string) (string, error)` / `Decrypt(string) (string, error)`）；常量 `CipherPrefix = "enc:v1:"`

- [ ] **Step 1: 写失败测试**

```go
package crypto

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

// 往返：加密后带前缀，解密还原。
func TestRoundTrip(t *testing.T) {
	c, err := NewFromHex(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	enc, err := c.Encrypt("sk-明文密码123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, CipherPrefix) {
		t.Fatalf("密文缺前缀: %q", enc)
	}
	if strings.Contains(enc, "明文密码") {
		t.Fatal("密文包含明文")
	}
	dec, err := c.Decrypt(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec != "sk-明文密码123" {
		t.Fatalf("解密不符: %q", dec)
	}
}

// 同明文两次加密 nonce 不同 → 密文不同（无确定性泄漏）。
func TestNonceRandomness(t *testing.T) {
	c, _ := NewFromHex(strings.Repeat("ab", 32))
	a, _ := c.Encrypt("same")
	b, _ := c.Encrypt("same")
	if a == b {
		t.Fatal("两次密文相同（nonce 复用）")
	}
}

// Decrypt 无前缀 = 明文原样返回（存量兼容核心）。
func TestDecryptPlaintextPassthrough(t *testing.T) {
	c, _ := NewFromHex(strings.Repeat("ab", 32))
	out, err := c.Decrypt("存量明文值")
	if err != nil || out != "存量明文值" {
		t.Fatalf("明文透传失败: %q %v", out, err)
	}
}

// 错 key 解密报错（GCM auth 失败）。
func TestWrongKeyFails(t *testing.T) {
	c1, _ := NewFromHex(strings.Repeat("ab", 32))
	c2, _ := NewFromHex(strings.Repeat("cd", 32))
	enc, _ := c1.Encrypt("secret")
	if _, err := c2.Decrypt(enc); err == nil {
		t.Fatal("错 key 解密应报错")
	}
}

// 非 32 字节 key 拒建。
func TestInvalidKeyLength(t *testing.T) {
	if _, err := New(bytes.Repeat([]byte{1}, 16)); err == nil {
		t.Fatal("16 字节应拒绝")
	}
	if _, err := NewFromHex("abcd"); err == nil {
		t.Fatal("短 hex 应拒绝")
	}
	if _, err := NewFromHex("zz"); err == nil {
		t.Fatal("非法 hex 应拒绝")
	}
}

// NewFromEnv：空 env → (nil, nil) 明文模式；合法 → cipher。
func TestNewFromEnv(t *testing.T) {
	t.Setenv("K", "")
	c, err := NewFromEnv("K")
	if err != nil || c != nil {
		t.Fatalf("空 env 应返 nil cipher: %v %v", c, err)
	}
	t.Setenv("K", strings.Repeat("ab", 32))
	c, err = NewFromEnv("K")
	if err != nil || c == nil {
		t.Fatalf("合法 env 应返 cipher: %v %v", c, err)
	}
	t.Setenv("K", "short")
	if _, err := NewFromEnv("K"); err == nil {
		t.Fatal("非法 env 应报错")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/crypto/ -v`
Expected: FAIL（包不存在）

- [ ] **Step 3: 实现**

```go
// Package crypto 提供敏感字段静态加密（AES-256-GCM，零外部依赖）。
// 密文带前缀 "enc:v1:"（版本位，未来算法升级留路）；Decrypt 无前缀原样返回，
// 存量明文数据零迁移兼容——升级部署后旧数据照常可读，新写入逐步密文化。
//
// nil Cipher = 明文兼容模式（Encrypt/Decrypt 原样透传），dev 未配 master key 时使用。
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// CipherPrefix 密文前缀（含版本）。
const CipherPrefix = "enc:v1:"

// keyLen AES-256 字节长度。
const keyLen = 32

// Cipher AES-256-GCM 加解密器。nil 指针 = 明文模式。
type Cipher struct{ aead cipher.AEAD }

// New 用 32 字节 raw key 构造（其他长度报错）。
func New(key []byte) (*Cipher, error) {
	if len(key) != keyLen {
		return nil, fmt.Errorf("master key 须为 %d 字节，实际 %d", keyLen, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cipher{aead: aead}, nil
}

// NewFromHex 用 64 位 hex 字符串构造（env 注入的标准形态）。
func NewFromHex(hexKey string) (*Cipher, error) {
	key, err := hex.DecodeString(strings.TrimSpace(hexKey))
	if err != nil {
		return nil, fmt.Errorf("master key 非合法 hex: %w", err)
	}
	return New(key)
}

// NewFromEnv 读 env：空值返 (nil, nil) 明文模式；非空则须合法（报错）。
// 调用方据此决定明文兼容或拒绝启动（生产）。
func NewFromEnv(name string) (*Cipher, error) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return nil, nil
	}
	return NewFromHex(v)
}

// Encrypt 加密：nil cipher 明文透传（dev 模式）；输出 CipherPrefix + base64(nonce|ct)。
// GCM 随机 nonce——同明文每次密文不同，无语义泄漏。
func (c *Cipher) Encrypt(plain string) (string, error) {
	if c == nil {
		return plain, nil
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := c.aead.Seal(nonce, nonce, []byte(plain), nil)
	return CipherPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt 解密：nil cipher / 无前缀（存量明文）原样返回；密文校验失败报错。
func (c *Cipher) Decrypt(s string) (string, error) {
	if c == nil || !strings.HasPrefix(s, CipherPrefix) {
		return s, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, CipherPrefix))
	if err != nil {
		return "", fmt.Errorf("密文 base64 解码失败: %w", err)
	}
	ns := c.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("密文长度不足")
	}
	plain, err := c.aead.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("解密失败（key 不匹配或数据损坏）: %w", err)
	}
	return string(plain), nil
}
```

- [ ] **Step 4: 跑测试通过**

Run: `go test ./internal/crypto/ -v`
Expected: PASS 全绿

- [ ] **Step 5: Commit**

```bash
git add internal/crypto/
git commit -m "feat(crypto): AES-256-GCM 静态加密基座——前缀识别存量兼容 + env 明文模式"
```

---

### Task 2: security + appconfig 装饰器

**Files:**
- Create: `internal/security/encrypted.go`
- Create: `internal/appconfig/encrypted.go`
- Test: `internal/security/encrypted_test.go`
- Test: `internal/appconfig/encrypted_test.go`

**Interfaces:**
- Consumes: Task 1 的 `crypto.Cipher`
- Produces: `security.NewEncryptedRepo(inner Repository, c *crypto.Cipher) Repository`；`appconfig.NewEncryptedRepo(inner Repository, c *crypto.Cipher) Repository`

**security 装饰器语义**（实现 `security.Repository` 全接口，Audit 方法直通）：
- `CreateSecret`：先 `c.Encrypt(s.Value)` 再透传 inner；返回值保持 inner 的掩码返回
- `Resolve`：inner 返回后 `c.Decrypt(s.Value)`
- `ListSecrets/GetSecret/ListAllSecrets`：inner 返回掩码（Value=掩码无前缀，Decrypt 透传无害），直通即可（保持接口完整）

**appconfig 装饰器语义**：
- `Upsert`：① `item.Type == TypeSecret` 时：若 `item.Value == SecretMask` → **掩码回写保护**：`inner.List(ctx, item.AppID, item.EnvID)` 找同 Key 项，用原值替换 item.Value（找不到则视为新值正常写入——用户首次建就填掩码属异常输入，按字面写入）；否则 `c.Encrypt(item.Value)`。② 非 secret 直通
- `ListPlain`：inner 返回后逐项 `if Type == TypeSecret { Decrypt(Value) }`
- `List`：inner 掩码返回，直通

- [ ] **Step 1: 写失败测试**（两文件，模式同下；security 测试用 memory store 作 inner）

security 测试核心断言：
```go
// 加密落库 + Resolve 解密 + cipher nil 透传 + 存量明文可读。
func TestSecurityEncryptedRepo(t *testing.T) {
	c, _ := crypto.NewFromHex(strings.Repeat("ab", 32))
	inner := secmemory.NewStore()
	repo := security.NewEncryptedRepo(inner, c)
	ctx := tenant.WithTenant(context.Background(), "t-acme")

	// 创建经装饰器 → 内存 store 里是密文。
	saved, err := repo.CreateSecret(ctx, security.Secret{Name: "db-pass", Type: security.TypeSecret, Value: "p@ss"})
	// ... 断言 err == nil；直接查 inner 原始（绕过装饰器 List 掩码）：
	rawList, _ := inner.ListSecrets(ctx) // 掩码——改用 pg 不可行，memory 也掩码。
	// memory store 的 List 全掩码无法直查密文——用 Resolve 反证：
	got, err := repo.Resolve(ctx, <平台级才可 Resolve>)
	// 详见下方「测试可观测性」说明：改为断言行为而非内部态。
}
```

**测试可观测性说明**（重要）：memory store 所有读路径均掩码，无法直接断言「inner 里是密文」。测试改用**可注入 fake inner**（测试文件内定义 `fakeSecStore` 实现 `security.Repository`，记录 `lastCreateValue string`，读方法返固定数据）——直接断言传给 inner 的 Value 带 `crypto.CipherPrefix`，及 Resolve/装饰器解密行为。appconfig 同理（`fakeCfgStore` 记录 `lastUpsertValue`，ListPlain 返回预置密文行断言装饰器解密）。memory store 仍用于「全链路无 panic + 掩码返回正常」的冒烟断言。

security 测试用例清单：
1. `TestCreateEncryptsValue`：fake inner 记录到 `enc:v1:` 前缀值
2. `TestResolveDecrypts`：fake inner Resolve 返回 `enc:v1:xxx`（用真 cipher 预加密）→ 装饰器返明文
3. `TestNilCipherPassthrough`：cipher nil → inner 收到原明文
4. `TestDecryptPlaintextLegacy`：fake inner 返回无前缀明文 → 原样（存量兼容）
5. `TestResolveMaskedFromInner`（用 memory store 冒烟）：全链路掩码语义不变

appconfig 测试用例清单：
1. `TestUpsertEncryptsSecret`：type=secret → inner 收到密文
2. `TestUpsertMaskPreservesOriginal`：Value=SecretMask + inner List 返回原值明文（fake 控制）→ inner 收到原值非掩码
3. `TestUpsertEnvNotEncrypted`：type=env → 直通明文
4. `TestListPlainDecrypts`：ListPlain 返回行解密
5. `TestNilCipherPassthrough`：nil 全透传 + 掩码仍触发查原值逻辑（cipher nil 也应保护掩码回写）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/security/ ./internal/appconfig/ -run Encrypted -v`
Expected: FAIL（装饰器不存在）

- [ ] **Step 3: 实现两个装饰器**

`internal/security/encrypted.go`：
```go
// encrypted.go Secret 静态加密装饰器：写路径加密 Value，Resolve 解密；
// List/Get 掩码直通（掩码无密文前缀，Decrypt 透传无害）。
// cipher nil = 明文模式（dev 未配 master key），全链路行为与现状一致。
package security

import (
	"context"

	"github.com/aitoys/paas/internal/crypto"
)

// NewEncryptedRepo 包装 Repository：CreateSecret 加密 / Resolve 解密。
func NewEncryptedRepo(inner Repository, c *crypto.Cipher) Repository {
	return &encryptedRepo{inner: inner, c: c}
}

type encryptedRepo struct {
	inner Repository
	c     *crypto.Cipher
}

func (e *encryptedRepo) ListSecrets(ctx context.Context) ([]Secret, error) {
	return e.inner.ListSecrets(ctx)
}
func (e *encryptedRepo) GetSecret(ctx context.Context, id string) (Secret, error) {
	return e.inner.GetSecret(ctx, id)
}
func (e *encryptedRepo) ListAllSecrets(ctx context.Context) ([]Secret, error) {
	return e.inner.ListAllSecrets(ctx)
}
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
// AuditStore 直通（审计不含敏感明文）。
func (e *encryptedRepo) ListAuditLogs(ctx context.Context, rt, a string) ([]AuditLog, error) {
	return e.inner.ListAuditLogs(ctx, rt, a)
}
func (e *encryptedRepo) RecordAudit(ctx context.Context, l AuditLog) error {
	return e.inner.RecordAudit(ctx, l)
}
func (e *encryptedRepo) ListAllAuditLogs(ctx context.Context) ([]AuditLog, error) {
	return e.inner.ListAllAuditLogs(ctx)
}
```

`internal/appconfig/encrypted.go`：
```go
// encrypted.go ConfigItem secret 静态加密装饰器：Upsert 加密（掩码回写保护），
// ListPlain 解密（reconciler 注入消费点）；List 掩码直通。
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

// findOriginal 按 (appID, envID, key) 查库中原值。ListPlain 拿密文原值（回写保持存储形态）。
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
```

- [ ] **Step 4: 跑测试通过**

Run: `go test ./internal/security/ ./internal/appconfig/ -run Encrypted -v && go build ./...`
Expected: PASS + 编译过

- [ ] **Step 5: Commit**

```bash
git add internal/security/encrypted.go internal/security/encrypted_test.go internal/appconfig/encrypted.go internal/appconfig/encrypted_test.go
git commit -m "feat(security,appconfig): Secret 静态加密装饰器——写加密/明文消费点解密 + 掩码回写保护"
```

---

### Task 3: dataservice Connection 字段级加密装饰器 + security seed 加密

**Files:**
- Create: `internal/dataservice/encrypted.go`
- Test: `internal/dataservice/encrypted_test.go`
- Modify: `internal/security/pg/store.go`（seed 路径）
- Test: `internal/security/pg/store_test.go`（补 seed 加密断言，`//go:build integration` 门控内或跳过——seed 直连 SQL 单测测不到则只改实现，e2e 验证）

**Interfaces:**
- Consumes: `crypto.Cipher`；`dataservice.SensitiveKeys`（若 connection.go 的 `sensitiveKeys` 未导出，导出为 `SensitiveKeys`——先 grep 确认，已导出则直接用）
- Produces: `dataservice.NewEncryptedRepo(inner Repository, c *crypto.Cipher) Repository`

**dataservice 装饰器语义**：
- `Create/Update`：Connection map 中 key ∈ SENSITIVE_KEYS 的值 `Encrypt`（仅当非空）
- `List/ListAll/Get/GetAny`：Connection 中敏感 key `Decrypt`（无前缀透传，存量兼容）
- `Delete` 直通

**security pg seed 加密**（spec 裁决：seed 直连 SQL 绕过装饰器，须 store 内处理）：
- `NewStore(db)` 后加可选 `WithCipher(c *crypto.Cipher) func(*Store)` option（或 `store.cipher` 字段 + setter）
- `seedAll`/`ensurePlatformSecrets` INSERT 前 `s.cipher.Encrypt(sec.Value)`（nil cipher 透传不破坏 dev）

- [ ] **Step 1: 写失败测试**

dataservice 测试（fake inner 同 Task 2 模式）：
1. `TestCreateEncryptsSensitiveFields`：Connection{password, host, port} → inner 收到 password=enc:v1: 前缀、host/port 原样
2. `TestGetDecryptsSensitiveFields`：fake inner 预置密文 → 装饰器返明文
3. `TestLegacyPlaintextReadable`：fake inner 预置无前缀明文 → 原样
4. `TestNilCipherPassthrough`
5. `TestUpdateEncryptsConnection`

- [ ] **Step 2: 确认失败**

Run: `go test ./internal/dataservice/ -run Encrypted -v`
Expected: FAIL

- [ ] **Step 3: 实现**

`internal/dataservice/encrypted.go`：
```go
// encrypted.go DataService Connection 字段级静态加密装饰器：仅 SENSITIVE_KEYS
// （password/secretKey/token/api_key/master_key/uri）加密，host/port/user/database
// 明文保留（排障可读）。写加密、读解密；无前缀密文透传（存量兼容）。
package dataservice

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

// encConn 加密 Connection 敏感字段（写路径）。
func (e *encryptedRepo) encConn(d DataService) DataService {
	out := make(map[string]string, len(d.Connection))
	for k, v := range d.Connection {
		if sensitiveKeys[k] && v != "" {
			if enc, err := e.c.Encrypt(v); err == nil {
				v = enc
			} // 加密失败保留原值（与 nil cipher 透传一致，不阻断主流程）
		}
		out[k] = v
	}
	d.Connection = out
	return d
}

// decConn 解密 Connection 敏感字段（读路径；无前缀明文透传）。
func (e *encryptedRepo) decConn(d DataService) DataService {
	out := make(map[string]string, len(d.Connection))
	for k, v := range d.Connection {
		if sensitiveKeys[k] && v != "" {
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
```

**注意**：先 grep `internal/dataservice/connection.go` 确认敏感 key 集合的标识符名（`sensitiveKeys`/`SENSITIVE_KEYS`），按实际名引用，不符则以上代码相应调整。

security pg seed（`internal/security/pg/store.go`）：
```go
// Store 加 cipher 字段：
type Store struct {
	db     *storagepg.DB
	cipher *crypto.Cipher // seed 路径加密（seed 直连 SQL 绕过装饰器）
}

// WithCipher 注入静态加密 cipher（seed 路径用；nil = 明文模式）。
func WithCipher(c *crypto.Cipher) func(*Store) {
	return func(s *Store) { s.cipher = c }
}

// NewStore 改为：
func NewStore(db *storagepg.DB, opts ...func(*Store)) *Store {
	s := &Store{db: db}
	for _, o := range opts {
		o(s)
	}
	return s
}
```
`seedAll` 与 `ensurePlatformSecrets` 的 INSERT 处：`sec.Value` 改为先 `v, err := s.cipher.Encrypt(sec.Value)`（err 则 fail 整个 seed——加密失败宁可不 seed 也不明文落库）；`v` 入 SQL。

- [ ] **Step 4: 跑测试通过 + 全量回归**

Run: `go test ./internal/dataservice/ ./internal/security/... -v 2>&1 | tail -20 && go build ./... && go test ./... 2>&1 | tail -5`
Expected: PASS 全绿

- [ ] **Step 5: Commit**

```bash
git add internal/dataservice/ internal/security/pg/store.go
git commit -m "feat(dataservice,security): Connection 字段级加密装饰器 + security PG seed 路径加密"
```

---

### Task 4: 装配 + 启动校验 + helm values

**Files:**
- Modify: `cmd/core/main.go`（cipher 初始化 + 三 repo 包装 + 生产校验）
- Modify: `cmd/core/persistence.go`（PG 路径包装 + security store WithCipher）
- Modify: `deploy/charts/paas/values.yaml` + `deploy/charts/paas/templates/core-deployment.yaml`（env 注入）
- Test: 现有 `cmd/core` 测试回归（装配无单测，e2e 验证）

**Interfaces:**
- Consumes: Tasks 1-3 全部产物
- Produces: env `PAAS_SECRET_MASTER_KEY`（chart `security.secretMasterKey`）

**装配语义**：
1. main.go 早期（JWT secret 校验附近）：`cipher, err := crypto.NewFromEnv("PAAS_SECRET_MASTER_KEY")`；err → 启动失败；`PAAS_PROD=true && cipher == nil` → 拒启（错误信息含 `openssl rand -hex 32` 指引）；`cipher == nil`（dev）→ log WARNING「未配置 PAAS_SECRET_MASTER_KEY，敏感数据明文存储」
2. cipher 非 nil 时包装：`stores.Security = security.NewEncryptedRepo(stores.Security, cipher)` 等（memory + PG 两路径都要）；注意 **`stores` struct 字段类型是接口，包装后重新赋值即可**；`secretResolver{store: stores.Security.(security.SecretStore)}` 等类型断言在包装后依然成立（装饰器实现接口）
3. persistence.go PG 路径：`secpg.NewStore(db, secpg.WithCipher(cipher))`——cipher 经参数/结构体传入 buildAllStores
4. **关键顺序**：包装必须在所有消费点装配（BindingInjector/reconciler/handler）之前——先看 main.go 装配顺序，buildStores 返回后立刻包装再注入各 handler
5. helm：values.yaml 加 `security.secretMasterKey: ""`（注释：`openssl rand -hex 32` 生成，生产必填）；core-deployment.yaml 加 `{{- if .Values.security.secretMasterKey }} env PAAS_SECRET_MASTER_KEY {{- end }}`

- [ ] **Step 1: 实现 main.go cipher 初始化 + 校验**

在 JWT secret 校验代码附近加（找 `PAAS_JWT_SECRET` 定位）：
```go
// 敏感数据静态加密 master key（spec 2026-08-29）：未设 = 明文模式（dev）；
// 生产必须设置（DB 泄漏面防护）。与 JWT secret 同款治理。
secCipher, err := crypto.NewFromEnv("PAAS_SECRET_MASTER_KEY")
if err != nil {
	return fmt.Errorf("PAAS_SECRET_MASTER_KEY 非法（须 64 位 hex，openssl rand -hex 32 生成）: %w", err)
}
if secCipher == nil {
	if os.Getenv("PAAS_PROD") == "true" {
		return errors.New("生产模式必须设置 PAAS_SECRET_MASTER_KEY（openssl rand -hex 32 生成）")
	}
	log.Println("WARNING: 未配置 PAAS_SECRET_MASTER_KEY，敏感数据（密钥/应用凭证/数据服务连接）明文存储")
}
```

- [ ] **Step 2: 包装三 repo（memory + PG 两路径）**

memory 路径（main.go 内 buildStores/memory 装配处）与 PG 路径（persistence.go buildAllStores 后返回前）均加：
```go
if secCipher != nil {
	stores.Security = security.NewEncryptedRepo(stores.Security, secCipher)
	stores.AppConfig = appconfig.NewEncryptedRepo(stores.AppConfig, secCipher)
	stores.DataService = dataservice.NewEncryptedRepo(stores.DataService, secCipher)
}
```
（PG 路径 secCipher 经 buildAllStores 参数或 Stores 构造器传入；`secpg.NewStore(db, secpg.WithCipher(secCipher))` 在 persistence.go 内）

**自查点**：grep `stores.DataService`/`stores.AppConfig`/`stores.Security` 全部引用，确认均发生在包装之后（main.go 装配顺序：buildStores → 包装 → handler 装配）。特别核对 `dsBindingInjector`、`appConfigLookup`、`secretResolver`、backup `dsEnvLookup`、reconciler 的 repo 来源。

- [ ] **Step 3: helm values + deployment env**

values.yaml：
```yaml
security:
  secretMasterKey: ""  # 敏感数据静态加密 master key（64 位 hex：openssl rand -hex 32）。生产必填；空 = 明文模式
```
core-deployment.yaml：在既有 env 列表（找 `PAAS_JWT_SECRET`）后加：
```yaml
{{- if .Values.security.secretMasterKey }}
- name: PAAS_SECRET_MASTER_KEY
  value: {{ .Values.security.secretMasterKey | quote }}
{{- end }}
```

- [ ] **Step 4: 全量回归**

Run: `go build ./... && go test ./... 2>&1 | tail -5`
Expected: 全绿

- [ ] **Step 5: Commit**

```bash
git add cmd/core/ deploy/charts/paas/
git commit -m "feat(core): 静态加密装配——master key 校验/三 repo 包装/helm 注入；生产强制加密"
```

---

### Task 5: e2e 验证 + CLAUDE.md 更新

**Files:**
- Modify: `CLAUDE.md`（安全章 + 基线表 + 留后续复审记录）
- Modify: `deploy/charts/paas/values-paas-k8s.yaml`（dev overlay 设一个 dev key，便于集群验证密文路径）

- [ ] **Step 1: 本地 dev 模式冒烟**

Run: `PAAS_DB_URL= ./bin/core &`（不设 key）→ curl 三模块端点行为不变。
Expected: WARNING 日志出现 + 全链路正常

- [ ] **Step 2: 本地加密模式验证**

设 `PAAS_SECRET_MASTER_KEY=$(openssl rand -hex 32)` + `PAAS_DB_URL` 指向本地 PG：
1. 启动 → airouter seed Secret 的 PG `SELECT value FROM secrets WHERE id='sec-platform-airouter'` 为 `enc:v1:` 前缀
2. `/v1/models` + `/v1/chat/completions`（airouter 真实推理）通——Resolve 解密成功
3. 创建 dataservice → PG connection JSONB 中 password 为 enc 前缀、host 明文
4. appconfig 建 secret → PG value 为 enc 前缀；再 Upsert 掩码值 → PG 原 enc 值不变（掩码回写保护）
5. 预插一条无前缀明文行 → ListPlain/注入读出明文正常（存量兼容）

- [ ] **Step 3: 生产拒启验证**

`PAAS_PROD=true` 不设 key 启动 → 拒启错误信息正确。

- [ ] **Step 4: k8s 部署验证**（[[k8s-always-latest]] 常驻授权）

`./scripts/deploy-k8s.sh`（values-paas-k8s.yaml 设 dev key）→ livez 200 + 上述 PG 密文断言 + 真实推理通。

- [ ] **Step 5: CLAUDE.md 更新**

1. 安全章（`internal/security/`）加静态加密小节
2. 基线表·秘钥管理行：`明文存储` → `静态加密已落地（2026-08-29，AES-GCM envelope）；轮转/过期缺`
3. 基线表·可观测行：`告警通知通道缺` → `已落地（后台评估引擎+webhook 出站+通知并入，00ce2ff）`（复审发现的过时记录修正）
4. CLAUDE.md:420/721 留后续行去掉「告警通知通道」（已落地）

- [ ] **Step 6: Commit + 推送**

```bash
git add CLAUDE.md deploy/charts/paas/values-paas-k8s.yaml
git commit -m "docs: 静态加密落地 + 基线表过时记录修正（告警通知已落地）"
git push origin main
```
