# 数据服务真实化 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把数据服务（mysql/redis/nats/minio）从「占位 StatefulSet」升级为「真实可连引擎 + 应用绑定自动注入连接信息 + Pod 级监控告警」。

**Architecture:** Connection 全程控制面生成（纯函数，dev/PG/K8s 三模式一致），reconciler 只落地（Secret+headless/ClusterIP Service+StatefulSet env 注入）不回流；应用绑定经依赖倒置的 BindingInjector 钩子自动写 appconfig 连接条目；observability 加 targetType=dataservice，配 Prometheus 取真实 Pod 指标、未配走 memory 惰性兜底。

**Tech Stack:** Go 1.26 + controller-runtime v0.24.1 + k8s.io v0.36.0；PostgreSQL（pgxpool + golang-migrate）；Vue 3 + Element Plus + TS。Apache 2.0，无新依赖。

## Global Constraints

- 主语言 Go；业务领域逻辑绝不进 Platform Core；多租户隔离由 Core 统一治理（Repository 强制 tenant 过滤）。
- Secret 值：list 掩码 `••••••`（不泄漏长度/内容），详情明文（dataservice:read 权限者）；**密码不进日志**（reconciler 只记 dsName/phase）。
- 响应合约 `{data:T}`/`{error:msg}` 不破坏（经 internal/httputil）；写操作裸对象 shape 保持（前端/测试依赖）。
- 凭证 crypto/rand 强随机（24 字符 base62）；K8s Secret 已存在不覆盖（幂等）。
- 依赖倒置：application 不 import dataservice/appconfig（BindingInjector 接口）；dataservice 不依赖 K8s（纯函数）；observability 不依赖 dataservice（targetType 字符串约定）。
- credentials 必须持久化（Create 生成一次，Secret 引用，重启不变）；host/port/uri 是纯函数（d.Name+ns+credentials）。
- 所有依赖 Apache 2.0 兼容；不碰 git（用户未要求不 commit/分支）；注释中文与代码库一致。
- 现有 4 类聚焦：db→mysql(3306) / cache→redis(6379) / mq→nats(4222) / storage→minio(9000)；vector/search 保持占位。

---

## File Structure

| 文件 | 动作 | 职责 |
|------|------|------|
| `internal/dataservice/connection.go` | 新建 | GenerateCredentials/EnginePort/BuildConnection/MaskConnection（纯函数） |
| `internal/dataservice/connection_test.go` | 新建 | 纯函数单测 |
| `internal/dataservice/model.go` | 改 | +Connection 字段；KindMetas mq 加 nats 默认；NamespaceResolver 接口 |
| `internal/dataservice/memory/store.go` | 改 | Create 填 Connection + map 深拷（修引用泄漏） |
| `internal/dataservice/pg/store.go` | 改 | Create/List/Get/Update 读写 connection JSONB + map 深拷 |
| `internal/storage/pg/migrations/0012_dataservice_connection.up/down.sql` | 新建 | dataservices 加 connection JSONB |
| `internal/dataservice/handler.go` | 改 | list 掩码 password / 详情明文 |
| `api/core/v1alpha1/dataservice_types.go` | 改 | Spec 加 Credentials/Connection |
| `config/crds/` + `deploy/charts/paas/templates/crds.yaml` | 重新生成 | `make manifests` |
| `internal/controller/dataservice_controller.go` | 改 | Secret+Svc+STS+env 注入+nats |
| `internal/controller/dataservice_controller_test.go` | 改 | fake client 测 Secret/Svc/STS/env/幂等 |
| `deploy/charts/paas/templates/rbac.yaml` | 改 | +secrets create/get/update |
| `internal/core/application/handler.go` | 改 | BindingInjector 接口 + OnBind/OnUnbind 调用 |
| `cmd/core/binding_injector.go` | 新建 | dsBindingInjector 桥接 dataservice+appconfig |
| `cmd/core/main.go` | 改 | 注入 NamespaceResolver + BindingInjector |
| `internal/observability/model.go` | 改 | TargetType 注释 +dataservice |
| `internal/observability/memory/store.go` | 改 | seed +dataservice series |
| `internal/observability/real/metrics.go` | 改 | targetType=dataservice 走 cadvisor pod 查询 |
| `frontend/console-user/src/views/resources/DataServiceDetail.vue` | 新建 | 详情+连接+监控+告警 |
| `frontend/console-user/src/views/resources/DataServices.vue` | 改 | 点行跳详情 |
| `frontend/console-user/src/router/index.ts` | 改 | +`/resources/:kind/:id` 路由 |
| `CLAUDE.md` + `CHANGELOG.md` | 改 | 文档 |

---

### Task 1: 连接信息纯函数（connection.go）

**Files:**
- Create: `internal/dataservice/connection.go`
- Test: `internal/dataservice/connection_test.go`

**Interfaces:**
- Consumes: `dataservice.DataService`（model.go，已存在；本 task 不改它）
- Produces:
  - `func GenerateCredentials(kind string) map[string]string`
  - `func EnginePort(kind string) int32`
  - `func BuildConnection(name, kind, namespace string, spec, credentials map[string]string) map[string]string`
  - `func MaskConnection(conn map[string]string) map[string]string`
  - 常量 `passwordKeys`（需掩码的 key 集：password/secretKey/token）

**设计要点：**
- 凭证用 `crypto/rand` 生成 24 字符 base62（`[0-9a-zA-Z]`）。读随机字节循环取模，拒绝越界字节避免偏置。
- `EnginePort`：db=3306, cache=6379, mq=4222, storage=9000；未知=0。
- `BuildConnection`：先复制 credentials（深拷），加 `host=<name>.<ns>.svc.cluster.local`、`port=<EnginePort>`，再按 kind 拼 `uri`：
  - db: `mysql://<user>:<password>@<host>:<port>/<database>`（database 缺省 "appdb"，user 缺省 "root"）
  - cache: `redis://:<password>@<host>:<port>`
  - mq: `nats://<token>@<host>:<port>`
  - storage: 不拼 uri，设 `endpoint=http://<host>:<port>`（accessKey/secretKey 已在 credentials）
- `MaskConnection`：深拷 conn，把 password/secretKey/token 替换 `dataservice` 包不存在的掩码常量 → 复用 `appconfig.SecretMask`？跨包引用不好。本包新建 `const SecretMask = "••••••"`。

- [ ] **Step 1: 写失败测试** `internal/dataservice/connection_test.go`

```go
package dataservice_test

import (
	"strings"
	"testing"

	"github.com/aitoys/paas/internal/dataservice"
)

func TestGenerateCredentials(t *testing.T) {
	for _, kind := range []string{dataservice.KindDB, dataservice.KindCache, dataservice.KindMQ, dataservice.KindStorage} {
		c := dataservice.GenerateCredentials(kind)
		switch kind {
		case dataservice.KindDB:
			if c["user"] == "" || c["password"] == "" || c["database"] == "" {
				t.Fatalf("db credentials missing: %v", c)
			}
		case dataservice.KindCache:
			if c["password"] == "" {
				t.Fatalf("cache password missing")
			}
		case dataservice.KindMQ:
			if c["token"] == "" {
				t.Fatalf("mq token missing")
			}
		case dataservice.KindStorage:
			if c["accessKey"] == "" || c["secretKey"] == "" {
				t.Fatalf("storage keys missing: %v", c)
			}
		}
		if len(c["password"])+len(c["token"])+len(c["secretKey"]) > 0 {
			// 至少一个 24 字符随机
		}
	}
	// 随机性：两次不同
	a := dataservice.GenerateCredentials(dataservice.KindDB)
	b := dataservice.GenerateCredentials(dataservice.KindDB)
	if a["password"] == b["password"] {
		t.Fatalf("password not random: %s", a["password"])
	}
	if len(a["password"]) != 24 {
		t.Fatalf("password length want 24 got %d", len(a["password"]))
	}
}

func TestEnginePort(t *testing.T) {
	cases := map[string]int32{
		dataservice.KindDB:      3306,
		dataservice.KindCache:   6379,
		dataservice.KindMQ:      4222,
		dataservice.KindStorage: 9000,
		"unknown":               0,
	}
	for k, want := range cases {
		if got := dataservice.EnginePort(k); got != want {
			t.Fatalf("EnginePort(%s) = %d want %d", k, got, want)
		}
	}
}

func TestBuildConnection(t *testing.T) {
	cred := dataservice.GenerateCredentials(dataservice.KindDB)
	conn := dataservice.BuildConnection("acme-db", dataservice.KindDB, "paas", map[string]string{"engine": "mysql"}, cred)
	if conn["host"] != "acme-db.paas.svc.cluster.local" {
		t.Fatalf("host = %s", conn["host"])
	}
	if conn["port"] != "3306" {
		t.Fatalf("port = %s", conn["port"])
	}
	uri := conn["uri"]
	if !strings.HasPrefix(uri, "mysql://root:") || !strings.Contains(uri, "@acme-db.paas.svc.cluster.local:3306/") {
		t.Fatalf("db uri malformed: %s", uri)
	}
	// storage 无 uri，有 endpoint
	connS := dataservice.BuildConnection("mc", dataservice.KindStorage, "paas", nil, dataservice.GenerateCredentials(dataservice.KindStorage))
	if connS["uri"] != "" || connS["endpoint"] != "http://mc.paas.svc.cluster.local:9000" {
		t.Fatalf("storage connection malformed: %v", connS)
	}
}

func TestMaskConnection(t *testing.T) {
	conn := map[string]string{"host": "h", "password": "secret", "token": "tk", "secretKey": "sk", "user": "u"}
	m := dataservice.MaskConnection(conn)
	if m["password"] != dataservice.SecretMask || m["token"] != dataservice.SecretMask || m["secretKey"] != dataservice.SecretMask {
		t.Fatalf("masked wrong: %v", m)
	}
	if m["host"] != "h" || m["user"] != "u" {
		t.Fatalf("non-secret fields changed: %v", m)
	}
	// 原始未被改
	if conn["password"] != "secret" {
		t.Fatalf("MaskConnection mutated input")
	}
}
```

- [ ] **Step 2: 运行测试验证失败**

Run: `go test ./internal/dataservice/ -run TestGenerateCredentials -v`
Expected: FAIL（connection.go 不存在 / 函数未定义）

- [ ] **Step 3: 实现 `internal/dataservice/connection.go`**

```go
package dataservice

import "crypto/rand"

// SecretMask 是连接信息中敏感字段的固定掩码（list 返回用，不泄漏长度/内容）。
const SecretMask = "••••••" //nolint:gosec // G101 误报：固定掩码占位符，非凭据

// passwordKeys 是连接信息中需要掩码的 key（password/secretKey/token）。
var passwordKeys = map[string]struct{}{
	"password": {}, "secretKey": {}, "token": {},
}

const randLen = 24
const randChars = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// randString 用 crypto/rand 生成 base62 随机串（拒绝越界字节避免模偏置）。
func randString(n int) string {
	out := make([]byte, n)
	max := byte(256 - (256 % len(randChars)))
	for i := 0; i < n; {
		b := make([]byte, 1)
		if _, err := rand.Read(b); err != nil {
			return "" //nolint:nilerr // crypto/rand 失败概率极低；返回空让上游 Validate 拦截
		}
		if b[0] >= max {
			continue
		}
		out[i] = randChars[int(b[0])%len(randChars)]
		i++
	}
	return string(out)
}

// GenerateCredentials 按 Kind 生成强随机凭证（user/password/database 或 accessKey/secretKey 或 token）。
// 纯函数 + crypto/rand；每次调用生成新随机，调用方负责持久化（Create 时一次性）。
func GenerateCredentials(kind string) map[string]string {
	switch kind {
	case KindDB:
		return map[string]string{
			"user":     "root",
			"password": randString(randLen),
			"database": "appdb",
		}
	case KindCache:
		return map[string]string{"password": randString(randLen)}
	case KindMQ:
		return map[string]string{"token": randString(randLen)}
	case KindStorage:
		return map[string]string{
			"accessKey": "minio",
			"secretKey": randString(randLen),
		}
	}
	return map[string]string{}
}

// EnginePort 返回 Kind 对应引擎默认端口（未知返 0）。
func EnginePort(kind string) int32 {
	switch kind {
	case KindDB:
		return 3306
	case KindCache:
		return 6379
	case KindMQ:
		return 4222
	case KindStorage:
		return 9000
	}
	return 0
}

// BuildConnection 算完整连接信息：host(FQDN)/port/credentials 合并/uri。
// 纯函数，dev/PG/K8s 三模式一致。
func BuildConnection(name, kind, namespace string, spec, credentials map[string]string) map[string]string {
	if namespace == "" {
		namespace = "paas"
	}
	host := name + "." + namespace + ".svc.cluster.local"
	port := EnginePort(kind)
	conn := map[string]string{
		"host": host,
		"port": itoa(port),
	}
	for k, v := range credentials { // 深拷凭证，不共享入参 map
		conn[k] = v
	}
	switch kind {
	case KindDB:
		user := conn["user"]
		if user == "" {
			user = "root"
		}
		db := conn["database"]
		if db == "" {
			db = "appdb"
		}
		conn["uri"] = "mysql://" + user + ":" + conn["password"] + "@" + host + ":" + itoa(port) + "/" + db
	case KindCache:
		conn["uri"] = "redis://:" + conn["password"] + "@" + host + ":" + itoa(port)
	case KindMQ:
		conn["uri"] = "nats://" + conn["token"] + "@" + host + ":" + itoa(port)
	case KindStorage:
		conn["endpoint"] = "http://" + host + ":" + itoa(port)
	}
	return conn
}

// MaskConnection 返回掩码副本（password/secretKey/token → SecretMask）。list 返回用。
func MaskConnection(conn map[string]string) map[string]string {
	out := make(map[string]string, len(conn))
	for k, v := range conn {
		if _, secret := passwordKeys[k]; secret {
			out[k] = SecretMask
		} else {
			out[k] = v
		}
	}
	return out
}

// itoa 是 strconv.Itoa 的 int32 本地包装（避免 connection.go 引 strconv 仅此一处也行，直接 import strconv 可接受）。
// 实现时直接用 strconv.Itoa(int(port)) 并 import "strconv"，删除此 helper。
func itoa(v int32) string { /* 用 strconv 实现 */ return "" }
```

> 实现注意：删除 `itoa` 占位，直接 `import "strconv"` 用 `strconv.Itoa(int(port))`。

- [ ] **Step 4: 运行测试验证通过**

Run: `go test ./internal/dataservice/ -run 'TestGenerateCredentials|TestEnginePort|TestBuildConnection|TestMaskConnection' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/dataservice/connection.go internal/dataservice/connection_test.go
git commit -m "feat(dataservice): 连接信息纯函数（凭证生成/FQDN/uri/掩码）"
```

---

### Task 2: domain 模型 + Create 填 Connection（memory + pg + migration）

**Files:**
- Modify: `internal/dataservice/model.go`（+Connection 字段、KindMetas mq 加 nats、NamespaceResolver 接口、DefaultNs 常量）
- Modify: `internal/dataservice/memory/store.go`（Create 填 Connection + map 深拷）
- Modify: `internal/dataservice/pg/store.go`（Create/List/Get/Update 读写 connection JSONB）
- Create: `internal/storage/pg/migrations/0012_dataservice_connection.up.sql` + `.down.sql`
- Modify: `internal/dataservice/handler.go`（list 掩码 / 详情明文）

**Interfaces:**
- Consumes: Task 1 的 `GenerateCredentials/BuildConnection/MaskConnection/SecretMask`
- Produces:
  - `type NamespaceResolver interface{ Namespace() string }`
  - `const DefaultNamespace = "paas"`
  - `DataService.Connection map[string]string` 字段
  - store 构造函数接收可选 NamespaceResolver（签名见下）

- [ ] **Step 1: 改 `model.go`**

1. 加字段（`DataService` struct，~L118-129 内）：
```go
Connection map[string]string `json:"connection,omitempty"` // 平台生成（host/port/credentials/uri），Create 时填
```
2. `KindMetas` MQ 项（~L70-73）engine 选项加 nats 并设默认：
```go
{Key: "engine", Label: "引擎", Type: FieldSelect, Options: []string{"nats", "kafka", "rabbitmq", "rocketmq"}, Default: "nats"},
```
3. 文件末尾加：
```go
// DefaultNamespace 是未注入 NamespaceResolver 时的兜底 K8s namespace。
const DefaultNamespace = "paas"

// NamespaceResolver 提供 K8s namespace（控制面生成 FQDN 用）。由 cmd/core 注入 PAAS_K8S_NAMESPACE。
type NamespaceResolver interface {
	Namespace() string
}

// FillConnection 为 DataService 生成并填充 Connection（凭证 + host/port/uri）。
// 已有 Connection（password 非空）则保留（幂等：不重新生成密码，避免重启变密码）。
func (d *DataService) FillConnection(ns string) {
	if d.Connection == nil || d.Connection["password"] == "" && d.Connection["token"] == "" && d.Connection["secretKey"] == "" {
		cred := GenerateCredentials(d.Kind)
		d.Connection = BuildConnection(d.Name, d.Kind, ns, d.Spec, cred)
		return
	}
	// 凭证已存在：仅重算 host/port/uri（namespace 可能变）
	d.Connection = BuildConnection(d.Name, d.Kind, ns, d.Spec, d.Connection)
}
```

- [ ] **Step 2: 改 `memory/store.go`**

1. `Store` 加字段 `nsResolver dataservice.NamespaceResolver`；`NewStore` 加可变参：
```go
func NewStore(opts ...Option) *Store {
	s := &Store{services: map[string]dataservice.DataService{}}
	for _, o := range opts {
		o(s)
	}
	s.seed()
	return s
}
type Option func(*Store)
func WithNamespaceResolver(r dataservice.NamespaceResolver) Option {
	return func(s *Store) { s.nsResolver = r }
}
func (s *Store) namespace() string {
	if s.nsResolver != nil {
		return s.nsResolver.Namespace()
	}
	return dataservice.DefaultNamespace
}
```
2. `Create`（~L72-98）存入前调：
```go
d.FillConnection(s.namespace())
d.Connection = cloneStrMap(d.Connection) // 深拷入口 map
d.Spec = cloneStrMap(d.Spec)
```
3. `Get`/`List` 返回前深拷 `Connection`+`Spec`（修引用泄漏，与 billing cloneIntMap 同款）：
```go
func cloneStrMap(m map[string]string) map[string]string {
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[k] = v
	}
	return cp
}
```
Get 返回前 `d.Connection = cloneStrMap(d.Connection); d.Spec = cloneStrMap(d.Spec)`；List 每条同。
4. `seed()` 数据（SeedDataServices）补 Connection：用 `d.FillConnection(DefaultNamespace)`（在 mk 闭包或 seed 循环里调）——但 seed 凭证每次启动随机不理想，KISS 接受（mock seed）。

- [ ] **Step 3: 写 migration `internal/storage/pg/migrations/0012_dataservice_connection.up.sql`**

```sql
ALTER TABLE dataservices ADD COLUMN IF NOT EXISTS connection JSONB NOT NULL DEFAULT '{}'::jsonb;
```
`0012_dataservice_connection.down.sql`:
```sql
ALTER TABLE dataservices DROP COLUMN IF EXISTS connection;
```

- [ ] **Step 4: 改 `pg/store.go`**

`Create`（~L121）：INSERT 加 connection 列（`JSON marshal(d.Connection)`）；插入前若 `d.Connection` 空调 `d.FillConnection(dataservice.DefaultNamespace)`（PG 模式 ns 由 env 决定，cmd/core 注入 resolver 覆盖；PG store 兜底用 DefaultNamespace）。
`Get`/`List`/`Update`：SELECT 加 `connection` 列，scan 到 `[]byte` 后 `json.Unmarshal` 到 `d.Connection`；返回前 `cloneStrMap`。
> 复用 `internal/storage/pg/helpers.go` 的 `RowScanner`；JSONB 读写参照 `dataservice.Spec` 现有模式（Spec 已是 JSONB）。

- [ ] **Step 5: 改 `handler.go`**

1. `serveCollection` GET list（~L101-106）：返回前对每条掩码：
```go
for i := range list {
	list[i].Connection = dataservice.MaskConnection(list[i].Connection)
}
```
2. `serveItem` GET 详情（~L146-151）：保持返回明文 `d`（read 权限者可见，已有 allow 门控）。

- [ ] **Step 6: 写/改测试**

`internal/dataservice/handler_test.go` 加：
- list 返回的 password == SecretMask
- 详情返回的 password != SecretMask（明文）

`memory` store 测：Create 后 Connection.host 含 namespace；Get 返回的 Connection 改动不影响 store 内部（深拷）。

- [ ] **Step 7: 运行测试**

Run:
```bash
go build ./...
go test ./internal/dataservice/... -v
PAAS_TEST_PG_URL=postgres://paas:pwd@localhost:5432/paas?sslmode=disable go test -tags=integration ./internal/dataservice/pg/ -v
```
Expected: PASS（PG 测需 docker-compose postgres；若不可用跳过集成测，内存测必过）

- [ ] **Step 8: Commit**

```bash
git add internal/dataservice/ internal/storage/pg/migrations/
git commit -m "feat(dataservice): domain+store 填充连接信息（凭证持久化/memory+pg/深拷修泄漏/list 掩码）"
```

---

### Task 3: CRD spec 加 Credentials/Connection + make manifests

**Files:**
- Modify: `api/core/v1alpha1/dataservice_types.go`
- Regenerate: `config/crds/` + `deploy/charts/paas/templates/crds.yaml`（`make manifests`）

**Interfaces:**
- Produces: `DataServiceSpec.Credentials map[string]string` + `DataServiceSpec.Connection map[string]string`

- [ ] **Step 1: 改 `dataservice_types.go`**（DataServiceSpec ~L12-20）

```go
Credentials map[string]string `json:"credentials,omitempty"` // 控制面生成（user/password/...），reconciler 读建 Secret
Connection  map[string]string `json:"connection,omitempty"`  // 控制面算（host/port/uri），可观测
```

- [ ] **Step 2: 重新生成 CRD + deepcopy**

Run: `make manifests`
Expected: `config/crds/core.aitoys.com_dataservices.yaml` 含 credentials/connection；`zz_generated.deepcopy.go` 含 map DeepCopyInto（`+kubebuilder:object:generate=true` 已在 dataservice_types.go:7，自动）。

- [ ] **Step 3: 同步 chart CRD**

`config/crds/` 生成物拷贝/同步到 `deploy/charts/paas/templates/crds.yaml`（手动 diff 同步，或 chart 引用 config/crds——按现状 crds.yaml 是手维护的副本，同步两个字段）。

- [ ] **Step 4: 构建验证**

Run: `go build ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add api/ config/crds/ deploy/charts/paas/templates/crds.yaml
git commit -m "feat(dataservice): CRD spec 加 credentials/connection 字段"
```

---

### Task 4: Reconciler 增强（Secret+Svc+STS+env 注入+nats）

**Files:**
- Modify: `internal/controller/dataservice_controller.go`
- Modify: `internal/controller/dataservice_controller_test.go`
- Modify: `deploy/charts/paas/templates/rbac.yaml`（+secrets）

**Interfaces:**
- Consumes: Task 3 的 `DataServiceSpec.Credentials`；Task 1 的 `dataservice.EnginePort`
- Produces: reconciler 创建 Secret `<name>-secret`、headless Svc `<name>-headless`、ClusterIP Svc `<name>`、StatefulSet

- [ ] **Step 1: 改 `rbac.yaml`** 补 secrets（core reconciler 建 Secret 需权限）。在 events 规则后加：
```yaml
  # DataService reconciler：为 mysql/redis/nats/minio 创建凭证 Secret。
  - apiGroups: [""]
    resources: ["secrets"]
    verbs: ["get", "list", "watch", "create", "update", "patch"]
```

- [ ] **Step 2: 写/改测试 `dataservice_controller_test.go`**

用 fake client（参照现有 test 模式）：
```go
func TestReconcileMySQLCreatesSecretServiceSTS(t *testing.T) {
	// 建 DataService CR (kind=db, engine=mysql, credentials={password:rand})
	// reconcile
	// 断言：Secret <name>-secret 存在 + data[MysqlRootPasswordKey] 非空
	//      headless Svc + ClusterIP Svc 存在
	//      StatefulSet 存在 + container env MYSQL_ROOT_PASSWORD secretKeyRef 指向 secret
	//      OwnerRef 指向 CR（3 资源）
}
func TestReconcileIdempotentDoesNotOverrideSecret(t *testing.T) {
	// 预置 Secret 改 password -> reconcile -> password 不变
}
func TestReconcileRedisCommandHasRequirepass(t *testing.T) {
	// container command 含 --requirepass
}
func TestReconcileNATS(t *testing.T) {
	// engine=nats -> image nats:2-alpine + args -auth
}
func TestReconcileUnknownEngineFailed(t *testing.T) {
	// kind=vector 未知 engine -> status.phase=failed
}
```

- [ ] **Step 3: 运行测试验证失败**

Run: `go test ./internal/controller/ -run TestReconcileMySQL -v`
Expected: FAIL（Secret/Svc 未创建）

- [ ] **Step 4: 重写 `dataservice_controller.go`**

1. `engineImage` mq case 加：`case "nats": return "nats:2-alpine"`
2. 新增容器/env 构造（按 Kind，env 用 secretKeyRef）：
```go
const secretNameSuffix = "-secret"

// secretFor 构造凭证 Secret（stringData from spec.Credentials）。
func secretFor(d *v1alpha1.DataService) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: d.Name + secretNameSuffix, Namespace: d.Namespace},
		StringData: d.Spec.Credentials,
	}
}

// envFor 按 Kind 返回容器 env（secretKeyRef 引用 <name>-secret）+ 启动参数。
func containerFor(d *v1alpha1.DataService, image string) corev1.Container {
	ref := func(key string) *corev1.SecretKeySelector {
		return &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: d.Name + secretNameSuffix}, Key: key}
	}
	c := corev1.Container{Name: "main", Image: image, Resources: defaultResources()}
	switch d.Spec.Kind {
	case "db": // mysql
		c.Env = []corev1.EnvVar{
			{Name: "MYSQL_ROOT_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: ref("password")}},
			{Name: "MYSQL_DATABASE", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: ref("database")}},
		}
	case "cache": // redis
		c.Command = []string{"redis-server", "--requirepass", "$(REDIS_PASSWORD)"}
		c.Env = []corev1.EnvVar{{Name: "REDIS_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: ref("password")}}}
	case "mq": // nats
		c.Args = []string{"-auth", "$(NATS_TOKEN)"}
		c.Env = []corev1.EnvVar{{Name: "NATS_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: ref("token")}}}
	case "storage": // minio
		c.Command = []string{"server", "/data", "--console-address", ":9001"}
		c.Env = []corev1.EnvVar{
			{Name: "MINIO_ROOT_USER", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: ref("accessKey")}},
			{Name: "MINIO_ROOT_PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: ref("secretKey")}},
		}
	}
	return c
}
```
3. Reconcile 主流程：取 CR → image=engineImage（空→failed 返回）→ CreateOrUpdate Secret（mutate 仅 CreationTimestamp.IsZero 时写 StringData，幂等不覆盖）+ headless Svc + ClusterIP Svc + STS（ServiceName=headless，container=containerFor，OwnerRef 全设 CR）→ 回写 status.phase/ready/image。
4. SetupWithManager 加 `Owns(&corev1.Secret{})` + `Owns(&corev1.Service{})`。
5. apply 失败 best-effort 回写 phase=failed（与 workload_controller 同款）。

- [ ] **Step 5: 运行测试验证通过**

Run: `go test ./internal/controller/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/controller/ deploy/charts/paas/templates/rbac.yaml
git commit -m "feat(dataservice): reconciler 落地 Secret+Svc+STS env 注入（mysql/redis/nats/minio 真实可连）"
```

---

### Task 5: 应用绑定注入 BindingInjector

**Files:**
- Modify: `internal/core/application/handler.go`（接口 + OnBind/OnUnbind 调用）
- Create: `cmd/core/binding_injector.go`
- Modify: `cmd/core/main.go`（注入）

**Interfaces:**
- Consumes: Task 2 的 `dataservice.DataService.Connection`；`appconfig.Repository.Upsert/List/Delete`
- Produces: `application.BindingInjector` 接口；`cmd/core.dsBindingInjector`

- [ ] **Step 1: 改 `application/handler.go`**

1. 加接口（文件顶部 const 后）：
```go
// BindingInjector 在应用绑定/解绑资源时触发副作用注入（依赖倒置，application 不依赖具体资源模块）。
// 典型：绑定 dataservice 时自动向 appconfig 注入连接信息（DATABASE_URL 等）。
type BindingInjector interface {
	OnBind(ctx context.Context, appID, bindingType, bindingName string) error
	OnUnbind(ctx context.Context, appID, bindingType, bindingName string) error
}
```
2. Handler 加字段 `binder BindingInjector` + opt：
```go
func WithBindingInjector(b BindingInjector) HandlerOpt { return func(h *Handler) { h.binder = b } }
```
3. POST bindings（~L121-140）成功 BindResource 后：
```go
if h.binder != nil && body.Type == "dataservice" {
	if err := h.binder.OnBind(r.Context(), id, body.Type, body.Name); err != nil {
		log.Printf("binding injector OnBind 失败（不阻断绑定）: %v", err)
	}
}
```
4. DELETE bindings（~L145-152）成功 Unbind 后：
```go
if h.binder != nil && parts[2] == "dataservice" {
	if err := h.binder.OnUnbind(r.Context(), id, parts[2], parts[3]); err != nil {
		log.Printf("binding injector OnUnbind 失败（best-effort）: %v", err)
	}
}
```
（注：import "log"。OnBind 失败不阻断绑定主体——绑定是主操作，注入是增强。）

- [ ] **Step 2: 写 `cmd/core/binding_injector.go`**

```go
package main

import (
	"context"
	"log"

	"github.com/aitoys/paas/internal/appconfig"
	"github.com/aitoys/paas/internal/dataservice"
)

// dsBindingInjector 把数据服务连接信息注入应用配置（应用×环境级）。
// 依赖倒置：application 包只知 application.BindingInjector 接口，本实现桥接 dataservice+appconfig。
type dsBindingInjector struct {
	dsRepo  dataservice.Repository
	cfgRepo appconfig.Repository
}

// injectKeys 按 Kind 返回要注入的 appconfig (key, value) 对（value 取自 ds.Connection）。
func injectKeys(ds dataservice.DataService) []struct{ Key, Value string } {
	switch ds.Kind {
	case dataservice.KindDB:
		return []struct{ Key, Value string }{{"DATABASE_URL", ds.Connection["uri"]}}
	case dataservice.KindCache:
		return []struct{ Key, Value string }{{"REDIS_URL", ds.Connection["uri"]}}
	case dataservice.KindMQ:
		return []struct{ Key, Value string }{{"NATS_URL", ds.Connection["uri"]}}
	case dataservice.KindStorage:
		return []struct{ Key, Value string }{
			{"MINIO_ENDPOINT", ds.Connection["endpoint"]},
			{"MINIO_ACCESS_KEY", ds.Connection["accessKey"]},
			{"MINIO_SECRET_KEY", ds.Connection["secretKey"]},
		}
	}
	return nil
}

func (b dsBindingInjector) OnBind(ctx context.Context, appID, btype, name string) error {
	if btype != "dataservice" {
		return nil
	}
	ds, err := b.dsRepo.Get(ctx, name)
	if err != nil {
		return err
	}
	for _, kv := range injectKeys(ds) {
		if kv.Value == "" {
			continue
		}
		_, _ = b.cfgRepo.Upsert(ctx, appconfig.ConfigItem{
			AppID: appID, EnvID: ds.EnvID, Key: kv.Key, Value: kv.Value, Type: appconfig.TypeSecret,
		})
	}
	return nil
}

func (b dsBindingInjector) OnUnbind(ctx context.Context, appID, btype, name string) error {
	if btype != "dataservice" {
		return nil
	}
	ds, err := b.dsRepo.Get(ctx, name)
	if err != nil {
		log.Printf("OnUnbind: 数据服务 %s 已不存在，跳过清理（best-effort）: %v", name, err)
		return nil // ds 已删则无 key 可清
	}
	items, err := b.cfgRepo.List(ctx, appID, ds.EnvID)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, kv := range injectKeys(ds) {
		want[kv.Key] = true
	}
	for _, it := range items {
		if want[it.Key] {
			_ = b.cfgRepo.Delete(ctx, it.ID) // Delete by id（appconfig.Repository 签名）
		}
	}
	return nil
}
```

- [ ] **Step 3: 改 `cmd/core/main.go`**（application handler 构造处）

```go
dsBindingInj := &dsBindingInjector{dsRepo: stores.DataService, cfgRepo: stores.AppConfig}
appHandler := application.NewHandler(stores.Application, application.WithAuthorize(...), application.WithQuotaCheck(...), application.WithBindingInjector(dsBindingInj))
```
（定位：grep `application.NewHandler` in cmd/core/main.go）

- [ ] **Step 4: 测试**

`cmd/core/binding_injector_test.go`：用 mock `dataservice.Repository` + `appconfig.Repository`（接口可直接 mock），断言 OnBind 各 Kind 调 Upsert 正确条目；OnUnbind 调 Delete 正确 id。

`application/handler_test.go` 加：POST bindings type=dataservice 调 mock injector.OnBind。

- [ ] **Step 5: 运行测试**

Run: `go build ./... && go test ./internal/core/application/... ./cmd/core/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/core/application/handler.go cmd/core/binding_injector.go cmd/core/main.go internal/core/application/handler_test.go cmd/core/binding_injector_test.go
git commit -m "feat(application): 绑定数据服务自动注入 appconfig 连接信息（DATABASE_URL 等）"
```

---

### Task 6: observability +dataservice targetType

**Files:**
- Modify: `internal/observability/model.go`（TargetType 注释）
- Modify: `internal/observability/memory/store.go`（seed +dataservice series）
- Modify: `internal/observability/real/metrics.go`（dataservice 走 cadvisor pod 查询）

**Interfaces:**
- Consumes: 无（targetType 字符串约定）
- Produces: `TargetDataservice = "dataservice"` 常量

- [ ] **Step 1: 改 `model.go`**

```go
const (
	TargetApp         = "app"
	TargetWorkload    = "workload"
	TargetEnv         = "env"
	TargetDataservice = "dataservice"
)
```
MetricSeries.TargetType 注释改 `// app | workload | env | dataservice`（2 处：L63、AlertRule L77）。

- [ ] **Step 2: 改 `memory/store.go` seed**

参照 `seed()` 现有 series 模式（~L487），为 `TargetDataservice` 加 2 条 series（ds-mysql / ds-redis，4 指标 cpu/mem/rps/latency）。复用现有 seed 循环结构（targetType+targetID+metric name 组合）。

- [ ] **Step 3: 改 `real/metrics.go`** dataservice 分支

`ListMetrics` 内，当 `targetType == "dataservice"` 时，改用 cadvisor Pod 指标查询（不带 target_type/target_id label，按 pod 名过滤）：
```go
if targetType == observability.TargetDataservice {
	// StatefulSet pod 名 = <targetID>-0；查 cadvisor container 指标
	pod := targetID + "-0"
	promQ := map[string]string{
		observability.MetricCPU: fmt.Sprintf("sum(rate(container_cpu_usage_seconds_total{pod=%q,container=\"main\"}[5m]))*100", pod),
		observability.MetricMem: fmt.Sprintf("container_memory_working_set_bytes{pod=%q,container=\"main\"}", pod),
	}
	// 查 promQ，映射为 MetricSeries{TargetType:"dataservice", TargetID:targetID}
	// （实现一个 queryOne 闭包，结构同现有 query_range 查询）
}
```
> 未配 PAAS_PROM_URL：compose 不注入 real reader，退回 memory（现状不变，无需改 compose）。

- [ ] **Step 4: 测试**

`real/metrics_test.go` 加：targetType=dataservice 时 query 串含 `pod="ds-mysql-0"`。
`memory` 测：seed 后 ListMetrics(targetType=dataservice) 返回非空。

- [ ] **Step 5: 运行测试**

Run: `go test ./internal/observability/... -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/observability/
git commit -m "feat(observability): +dataservice targetType（Pod 级真实 + memory 惰性兜底）"
```

---

### Task 7: 前端 DataServiceDetail + 监控 + 路由

**Files:**
- Create: `frontend/console-user/src/views/resources/DataServiceDetail.vue`
- Modify: `frontend/console-user/src/views/resources/DataServices.vue`（点行跳详情）
- Modify: `frontend/console-user/src/router/index.ts`（+路由）

**Interfaces:**
- Consumes: `GET /api/dataservices/{id}`（含 Connection）、`GET /api/observability/metrics?targetType=dataservice&targetId=<dsName>`
- Produces: 详情页 UI

- [ ] **Step 1: 加路由 `router/index.ts`**

```ts
{ path: '/resources/:kind/:id', name: 'dataServiceDetail', component: () => import('@/views/resources/DataServiceDetail.vue'), props: true }
```
（参照现有 `/resources/:kind` 路由模式）

- [ ] **Step 2: 写 `DataServiceDetail.vue`**

结构（参照 ApplicationDetail.vue 的监控卡 + fetchJSON 模式）：
- 基本信息：Kind/Engine/Status/EnvID/CreatedAt
- 连接信息卡（el-descriptions）：host/port/user/database(或 accessKey)/uri，password/secretKey/token 默认掩码 + 「👁 显示/隐藏」+「📋 复制」（复制时若处于显示态复制明文，掩码态提示先显示）
- 监控卡：4 指标（CPU/内存/RPS/延迟）当前值 + CSS sparkline（复用 observability 现有 sparkline 样式），10s 轮询 `fetchJSON('/api/observability/metrics?targetType=dataservice&targetId=' + encodeURIComponent(ds.name))`
- 告警规则 section：targetType 下拉含 dataservice（复用 observability 告警 CRUD 组件，如有；否则简化展示当前告警）

```vue
<script setup lang="ts">
import { ref, onMounted, onUnmounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import { fetchJSON } from '@/api/http'
let timer: number | undefined
let alive = true
const ds = ref<any>(null)
const showSecret = ref(false)
const route = useRoute()
const metrics = ref<any[]>([])
async function load() {
  const r = await fetchJSON<any>(`/api/dataservices/${route.params.id}`)
  ds.value = r
  const m = await fetchJSON<any[]>(`/api/observability/metrics?targetType=dataservice&targetId=${encodeURIComponent(r.name)}`)
  if (alive) metrics.value = m
}
onMounted(() => { load(); timer = window.setInterval(load, 10000) })
onUnmounted(() => { alive = false; if (timer) clearInterval(timer) })
</script>
```
（模板含连接卡 + 监控卡 + 告警 section，密码字段 `showSecret ? conn.password : '••••••'`）

- [ ] **Step 3: 改 `DataServices.vue`** 列表行点击跳详情

el-table `@row-click="(row) => router.push(`/resources/${row.kind}/${row.id}`)"`。

- [ ] **Step 4: 构建 + 手测**

Run: `cd frontend && pnpm build`
Expected: 构建通过

- [ ] **Step 5: Commit**

```bash
git add frontend/console-user/src/
git commit -m "feat(console-user): 数据服务详情页（连接信息+监控告警可视化）"
```

---

### Task 8: 文档 + 集群 e2e 验证清单

**Files:**
- Modify: `CLAUDE.md`（dataservice 章节 + DevOps/binding 说明）
- Modify: `CHANGELOG.md`（Feature 条目）

- [ ] **Step 1: 更新 `CLAUDE.md`** 数据服务章节，补充：真实引擎落地（Secret+Svc+STS+env）、Connection 控制面生成、应用绑定自动注入 appconfig、observability targetType=dataservice。更新「完成度约 96%」措辞。

- [ ] **Step 2: 更新 `CHANGELOG.md`** Added：数据服务真实化（mysql/redis/nats/minio 可连 + 应用绑定注入连接信息 + Pod 级监控告警）。

- [ ] **Step 3: 全量回归**

Run: `go build ./... && go test ./... && make lint`
Expected: PASS

- [ ] **Step 4: 集群 e2e 验证清单**（部署后人工执行，不在本 plan 自动化）

```bash
# 部署
./scripts/deploy-k8s.sh
# 1. 建 mysql 数据服务
curl -X POST -H "Authorization: Bearer sk-acme-admin" -H "Content-Type: application/json" \
  -d '{"kind":"db","name":"test-mysql","engine":"mysql","envId":"env-acme-test","spec":{"engine":"mysql","version":"8","size_gb":"20"}}' \
  http://paas.k8s.dd/api/dataservices
# 2. 验证 Pod Running + 可登录
kubectl -n paas get pod -l paas.aitoys/dataservice=test-mysql
DSID=<上一步返回 id>
PASS=$(curl -s -H "Authorization: Bearer sk-acme-admin" http://paas.k8s.dd/api/dataservices/$DSID | jq -r .connection.password)
kubectl -n paas exec deploy/test-mysql -- mysql -uroot -p"$PASS" -e "SELECT 1"
# 3. 应用绑定 -> appconfig 出现 DATABASE_URL
curl -X POST -H "Authorization: Bearer sk-acme-admin" -H "Content-Type: application/json" \
  -d '{"type":"dataservice","name":"'$DSID'"}' \
  http://paas.k8s.dd/api/applications/<app-id>/bindings
curl -s -H "Authorization: Bearer sk-acme-admin" "http://paas.k8s.dd/api/applications/<app-id>/configs?envId=env-acme-test" | jq '.data[] | select(.key=="DATABASE_URL")'
# 4. redis
curl -X POST ... '{"kind":"cache","name":"test-redis","engine":"redis","envId":"env-acme-test","spec":{"engine":"redis"}}'
kubectl -n paas exec deploy/test-redis -- redis-cli -a "$RPASS" ping  # PONG
# 5. 监控（未配 Prometheus 走 memory 兜底，应返回 series）
curl -s -H "Authorization: Bearer sk-acme-admin" "http://paas.k8s.dd/api/observability/metrics?targetType=dataservice&targetId=test-mysql" | jq .
```

- [ ] **Step 5: Commit**

```bash
git add CLAUDE.md CHANGELOG.md
git commit -m "docs: 数据服务真实化文档 + e2e 验证清单"
```

---

## Self-Review

**1. Spec coverage：** A 数据面（Task 1-4：纯函数/domain/CRD/reconciler）✓；B 连接返回+注入（Task 2 handler 掩码 + Task 5 BindingInjector）✓；C 监控（Task 6 后端 + Task 7 前端）✓；文档（Task 8）✓。RBAC secrets 补（Task 4 Step1）✓；PG migration（Task 2 Step3）✓。

**2. Placeholder scan：** Task 1 itoa 占位已注明改 strconv；其余均给完整代码块或精确定位。无 TBD/TODO。

**3. Type consistency：** `GenerateCredentials(kind)` / `BuildConnection(name, kind, ns, spec, cred)` / `EnginePort(kind)` / `MaskConnection(conn)` 签名跨 Task 一致；`NamespaceResolver.Namespace() string` / `DefaultNamespace` 一致；`BindingInjector.OnBind/OnUnbind(ctx, appID, type, name)` 跨 Task 5 一致；appconfig `Upsert(ctx, ConfigItem)` / `List(ctx, appID, envID)` / `Delete(ctx, id)` 与现有签名一致；`TargetDataservice` 常量 Task 6 定义 Task 7（前端字符串）一致。

**风险点（实施时注意）：**
- Task 2 PG store：connection JSONB 读写参照 Spec 现有模式；migration 0012 启动自动 up（embed）。
- Task 4 reconciler：Secret mutate 幂等（仅创建时写 StringData）是关键，测试覆盖。
- Task 4 redis command 用 `$(REDIS_PASSWORD)` —— K8s env expansion 在 command/args 仅对 `$(VAR)` 形式生效，确认容器内 env 先于 command 解析（K8s 行为：env 注入后 command 展开 `$(VAR)`，✓）。
- Task 5 OnUnbind：appconfig Delete by id，需先 List 找 key→id（已实现）。
- Task 6 real cadvisor 查询：`container_cpu_usage_seconds_total` 需集群装 cadvisor（kubelet 内置或 prometheus-node-exporter）；未装则查空返空（降级，不 panic）。

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-08-01-dataservice-real-engine.md`. 本 plan 是 8 个串行 task（依赖链：1→2→3→4 数据面；5 依赖 2；6 独立；7 依赖后端 API；8 最后）。鉴于 task 间有强依赖且共享 domain/CRD 上下文，**推荐 Inline Execution（executing-plans，本会话批量执行 + checkpoint review）**，避免 subagent 跨 task 上下文丢失。
