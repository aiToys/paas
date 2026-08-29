# Secret 静态加密（envelope encryption）设计

**日期**：2026-08-29
**定位**：留后续复审裁决的最后一笔 A 类基线缺口（基线表·秘钥管理「明文存储」）。B3（maas/engine admin 审计）与 A2（告警通知通道）复审确认已落地，本 spec 仅覆盖 A1。

## 1. 问题

三处敏感数据后端明文存储，DB 泄漏（备份落盘/SQL 注入面/误配只读账号）即凭证全泄：

| 位置 | 明文字段 | 明文消费点 |
|------|---------|-----------|
| `security.Secret.Value` | 平台/租户密钥（含 airouter api_key） | `Resolve`（MaaS 通道运行时解析） |
| `appconfig.ConfigItem.Value`（type=secret） | 应用凭证（DATABASE_URL 等） | `BindingInjector`（绑定注入） |
| `dataservice.DataService.Connection` | password/secretKey/token/uri | reconciler（建 K8s Secret）、`dsBindingInjector` |

API 返回掩码（`Masked()`/`MaskConnection`）只挡 HTTP 面，不挡存储面。

## 2. 方案：AES-256-GCM + 装饰器 + 前缀识别存量兼容

### 2.1 internal/crypto 包（单一真源，零外部依赖）

```go
// Cipher 加解密接口。nil cipher = 明文兼容模式（Encrypt/Decrypt 原样透传）。
type Cipher struct{ aead cipher.AEAD }

func New(key []byte) (*Cipher, error)          // 32 字节 AES-256；其他长度报错
func NewFromHex(hexKey string) (*Cipher, error)
func NewFromEnv(envKey string) (*Cipher, error) // env 空 → (nil, nil) 明文模式

func (c *Cipher) Encrypt(plain string) (string, error)  // "enc:v1:" + base64(nonce|ct)
func (c *Cipher) Decrypt(s string) (string, error)      // 识别前缀；无前缀 = 明文原样返回（存量兼容）
```

- **密文格式** `enc:v1:<base64(nonce + ciphertext + tag)>`：前缀自带版本位（未来算法升级留路）；GCM 随机 nonce（同明文每次密文不同，无语义泄漏）。
- **`Decrypt` 无前缀原样返回**：存量明文数据零迁移即可读——升级部署后旧数据照常工作，写路径新数据逐步密文化（Upsert/Update 自然覆盖）。
- **nil cipher 语义**：`PAAS_SECRET_MASTER_KEY` 未设（dev）时全链路明文，行为与现状完全一致。

### 2.2 主密钥治理（与 PAAS_JWT_SECRET 同款）

- env `PAAS_SECRET_MASTER_KEY`：64 位 hex 字符（32 字节）。
- `PAAS_PROD=true` 时：未设或长度非法 → **拒绝启动**（防生产明文裸奔）。
- dev 未设：明文模式 + 启动日志 WARNING（提示设置后自动加密新写入）。
- helm chart `security.secretMasterKey`（values.yaml 注释指引 `openssl rand -hex 32`）；key 轮转（re-wrap 全量数据）留后续。

### 2.3 装饰器切入（security/appconfig）+ 持久层切入（dataservice）

security 与 appconfig 用装饰器（Repository 接口不变，`cmd/core` 装配层按 cipher 非空包装）：

```
security.NewEncryptedRepo(inner, cipher)   // Write 加密 Value；Resolve 解密
appconfig.NewEncryptedRepo(inner, cipher)  // Write(Upsert) type=secret 加密；ListRead 解密供注入
```

**dataservice 不用装饰器**（2026-08-29 实现审查裁决）：managed 模式凭证由 store 内部
`FillConnection` 生成（handler 清空 Connection 传入），装饰器在 store 外先跑无值可加密、
后跑则密文被 FillConnection 重建进 uri（密文内嵌破坏连接串）。加密下沉 **dspg.Store 持久层**
（Connection JSONB 序列化点，maskKeys 字段级加解密，抽纯函数单测可测）。memory store 不加密
——威胁模型是 DB at-rest 泄漏，进程内存非泄漏面。此方案下 applier/reconciler/BindingInjector
天然拿明文（CRD/K8s Secret 本就需明文），managed/external 两模式全覆盖。

关键语义辨析（读路径）：

- **List/Get 返回本就 Masked**（掩码替代明文）——装饰器解不解密结果相同，为防未来掩码遗漏仍统一解密后再交给 store 的 Masked 逻辑（纵深）。
- **明文消费点必须解密**：`security.Resolve`、appconfig `BindingInjector`（经 repo 读明文注入）、dataservice reconciler（读 Connection 建 K8s Secret）与 `dsBindingInjector`。消费点经装饰后的 repo 读取即自动解密——**装配顺序保证**：handler/reconciler/注入器注入的都是包装后的 repo。
- **appconfig 掩码回写陷阱**：前端编辑 secret 不回填值（提交掩码 `••••••`）。Upsert 收到掩码值时**跳过加密并保留库中原值**（检测 `value == SecretMask` 则不更新 value 列）——此语义在装饰器之前的既有 store 层已处理则保持，未处理则在装饰器补。

### 2.4 dataservice.Connection 字段级加密

复用 `connection.go` 的 SENSITIVE_KEYS（password/secretKey/token/api_key/master_key/uri）：仅敏感 key 加密，host/port/user/database 明文保留（排障可读、PG 侧可直接查结构）。

### 2.5 覆盖路径清单

| 路径 | 处理 |
|------|------|
| security Create/Update | 加密 Value |
| security Resolve | 解密 |
| security seed（airouter env 注入 `ensurePlatformSecrets`） | 经包装 repo → 自动加密 |
| appconfig Upsert（type=secret） | 加密（掩码值跳过） |
| appconfig List/Get | 解密后 Masked（双保险） |
| BindingInjector 读 | 解密 |
| dataservice Create/Update | Connection 敏感字段加密 |
| dataservice 读（list/detail/Get） | 解密后 MaskConnection |
| reconciler 读 Connection | 解密（经包装 repo） |
| dsBindingInjector | 解密 |
| memory 模式 | 同样过装饰器（dev 内存路径行为一致） |

### 2.6 不做（YAGNI）

- KMS/Vault 外部密钥管理（接口已留 `Cipher` 抽象，未来接 KMS 只换实现）。
- 密钥轮转 re-wrap 工具。
- 已加密数据审计（哪些行还是明文）——前缀识别天然支持后续写脚本盘点。
- 审计日志内容加密（不含敏感明文，现状脱敏已够）。

## 3. 测试

- `internal/crypto` 单测：往返/无前缀透传/错 key 解密报错/非 32 字节拒建。
- 装饰器单测（三模块各一）：cipher 非 nil 时 Create 后 inner 落库值为 `enc:v1:` 前缀；Resolve/注入点返明文；cipher nil 全透传；掩码回写不覆盖。
- 存量兼容：库中预插明文行，包装 repo 读取正常。
- 既有全部单测不回归（cipher nil 默认路径行为不变）。

## 4. 验收（e2e）

1. dev 启动（未设 key）：全链路行为与现状一致（明文模式）。
2. 设 `PAAS_SECRET_MASTER_KEY` 重启：airouter seed Secret 落库为 enc:v1 前缀；`/v1/chat/completions` 真实推理仍通（Resolve 解密成功）；应用绑定 dataservice 注入的 `DATABASE_URL` 含真实明文密码；PG 直接 `SELECT value` 看不到明文。
3. 存量明文行（升级前数据）读取注入正常。
4. `PAAS_PROD=true` 且未设 key → 启动拒绝。
