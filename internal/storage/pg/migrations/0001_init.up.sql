-- PaaS Platform Core 初始化 schema（全 11 模块 + 行级安全）。
-- 项目未上线，由原 0001-0015 增量 migration 合并而来（2026-08-02），首装只跑本文件。
-- 多租户隔离：所有业务表带 tenant_id，查询层显式 WHERE 过滤；RLS 作第二道防线（见末尾）。
-- JSONB 列存多值字段（nil 安全由仓储层保证：读出空 map/切片非 nil）。

-- ===== identity：租户 / 用户 / 角色 / API Key =====
CREATE TABLE IF NOT EXISTS tenants (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    is_admin      BOOLEAN NOT NULL DEFAULT FALSE,
    email         TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL DEFAULT '',          -- bcrypt；空=不可密码登录（仅 API Key）
    status        TEXT NOT NULL DEFAULT 'active',    -- active | disabled
    created_at    TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_users_tenant ON users(tenant_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_name ON users(name);  -- 登录入口全局唯一

-- 用户角色多值：一行一角色（identity.User.Roles []string）。
CREATE TABLE IF NOT EXISTS user_roles (
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role    TEXT NOT NULL,
    PRIMARY KEY (user_id, role)
);

CREATE TABLE IF NOT EXISTS api_keys (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    -- user_id 不加 FK：内存实现中 api_key 自带 Roles，鉴权不依赖独立 user 记录，
    -- 且 seed 仅建 tenants + api_keys（不建 users）。保持松耦合与现状一致。
    user_id    TEXT NOT NULL,
    key        TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_apikeys_tenant ON api_keys(tenant_id);
CREATE INDEX IF NOT EXISTS idx_apikeys_key ON api_keys(key);

-- API Key 角色多值：鉴权按 Key 上的角色判定（identity.APIKey.Roles []string）。
CREATE TABLE IF NOT EXISTS api_key_roles (
    api_key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,
    PRIMARY KEY (api_key_id, role)
);

-- ===== application：应用主线 + 绑定项 =====
-- ResourceCount 计数不入库，读时由 Bindings Recount 派生（与内存实现一致）。
CREATE TABLE IF NOT EXISTS applications (
    id        TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    name      TEXT NOT NULL,
    initial   TEXT NOT NULL DEFAULT '',
    env       TEXT NOT NULL DEFAULT '',
    status    TEXT NOT NULL DEFAULT '',
    gradient  TEXT NOT NULL DEFAULT '',
    "desc"    TEXT NOT NULL DEFAULT '',
    replicas  TEXT NOT NULL DEFAULT '',
    rps       TEXT NOT NULL DEFAULT '',
    UNIQUE (tenant_id, name)
);
CREATE INDEX IF NOT EXISTS idx_apps_tenant ON applications(tenant_id);

-- 绑定项（真源）；ord 保插入顺序，列表展示稳定。
CREATE TABLE IF NOT EXISTS application_bindings (
    app_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
    ord    INTEGER NOT NULL,
    type   TEXT NOT NULL,
    name   TEXT NOT NULL,
    note   TEXT NOT NULL DEFAULT '',
    UNIQUE (app_id, type, name)
);
CREATE INDEX IF NOT EXISTS idx_bindings_app ON application_bindings(app_id);

-- ===== environment：物理环境（生产/测试），独立一等公民 =====
CREATE TABLE IF NOT EXISTS environments (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,
    cluster    TEXT NOT NULL DEFAULT '',
    "desc"     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX IF NOT EXISTS idx_env_tenant ON environments(tenant_id);

-- ===== appconfig：工作负载级 env/Secret（应用 × 环境） =====
-- secret 值后端明文存储，API 返回掩码（在仓储层 Masked）。
CREATE TABLE IF NOT EXISTS app_configs (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    app_id     TEXT NOT NULL,
    env_id     TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL,
    type       TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, app_id, env_id, key)
);
CREATE INDEX IF NOT EXISTS idx_appconfig_lookup ON app_configs(tenant_id, app_id, env_id);

-- ===== dataservice：数据服务资源（DB/缓存/MQ/存储/向量/搜索，按 kind 区分） =====
-- spec 用 JSONB 存 map[string]string；connection 为平台生成（host/port/credentials/uri）。
CREATE TABLE IF NOT EXISTS data_services (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    kind       TEXT NOT NULL,
    name       TEXT NOT NULL,
    spec       JSONB NOT NULL DEFAULT '{}'::jsonb,
    connection JSONB NOT NULL DEFAULT '{}'::jsonb,    -- 平台生成：host/port/credentials/uri
    status     TEXT NOT NULL,
    env_id     TEXT NOT NULL,
    app_id     TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX IF NOT EXISTS idx_ds_tenant_kind ON data_services(tenant_id, kind);

-- ===== workload：应用运行形态（Service/Job/CronJob） =====
-- lane_id 默认 'default'（基线单例）；port>0 才建 K8s Service。
CREATE TABLE IF NOT EXISTS workloads (
    id             TEXT PRIMARY KEY,
    tenant_id      TEXT NOT NULL,
    app_id         TEXT NOT NULL DEFAULT '',
    env_id         TEXT NOT NULL DEFAULT '',
    lane_id        TEXT NOT NULL DEFAULT 'default',
    type           TEXT NOT NULL,
    name           TEXT NOT NULL,
    image          TEXT NOT NULL DEFAULT '',
    image_ref      TEXT NOT NULL DEFAULT '',
    replicas       INTEGER NOT NULL DEFAULT 0,
    ready          INTEGER NOT NULL DEFAULT 0,
    status         TEXT NOT NULL DEFAULT '',
    schedule       TEXT NOT NULL DEFAULT '',
    command        TEXT NOT NULL DEFAULT '',
    port           INTEGER NOT NULL DEFAULT 0,        -- Service 对外端口（>0 才建 Service）
    container_port INTEGER NOT NULL DEFAULT 0,        -- Pod 监听端口（0 取 port）
    created_at     TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_wl_lookup ON workloads(tenant_id, env_id, app_id, type);

-- ===== devops：代码→构建→镜像→发布（4 表，跨模块不建外键） =====
CREATE TABLE IF NOT EXISTS code_repos (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    app_id        TEXT NOT NULL,
    git_url       TEXT NOT NULL,
    branch        TEXT NOT NULL DEFAULT '',
    dockerfile    TEXT NOT NULL DEFAULT '',
    build_context TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_repos_app ON code_repos(tenant_id, app_id);

CREATE TABLE IF NOT EXISTS build_runs (
    id          TEXT PRIMARY KEY,
    tenant_id   TEXT NOT NULL,
    app_id      TEXT NOT NULL,
    repo_id     TEXT NOT NULL,
    trigger     TEXT NOT NULL,
    commit      TEXT NOT NULL,
    branch      TEXT NOT NULL DEFAULT '',
    message     TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,                        -- pending|running|success|failed
    image_id    TEXT NOT NULL DEFAULT '',
    log         TEXT NOT NULL DEFAULT '',
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_builds_app ON build_runs(tenant_id, app_id);

CREATE TABLE IF NOT EXISTS images (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    app_id       TEXT NOT NULL,
    registry     TEXT NOT NULL,
    tag          TEXT NOT NULL,
    digest       TEXT NOT NULL,
    source       TEXT NOT NULL DEFAULT '',
    branch       TEXT NOT NULL DEFAULT '',
    build_run_id TEXT NOT NULL DEFAULT '',
    built_at     TIMESTAMPTZ NOT NULL,
    status       TEXT NOT NULL DEFAULT 'ready'
);
CREATE INDEX IF NOT EXISTS idx_images_app ON images(tenant_id, app_id);

CREATE TABLE IF NOT EXISTS releases (
    id                TEXT PRIMARY KEY,
    tenant_id         TEXT NOT NULL,
    app_id            TEXT NOT NULL,
    env_id            TEXT NOT NULL,
    image_id          TEXT NOT NULL,
    image_digest      TEXT NOT NULL DEFAULT '',
    strategy          TEXT NOT NULL DEFAULT 'rolling',
    status            TEXT NOT NULL,
    workload_id       TEXT NOT NULL DEFAULT '',
    previous_image_id TEXT NOT NULL DEFAULT '',
    is_rollback       BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL,
    created_by        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_releases_app ON releases(tenant_id, app_id);

-- ===== governance：注册中心 / API 网关 / 熔断器（4 表） =====
-- Instance.Meta 与 Route.Methods 用 JSONB 列存。DeleteService 级联清 instances/routes/breakers（仓储层事务）。
CREATE TABLE IF NOT EXISTS gov_services (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    app_id     TEXT NOT NULL DEFAULT '',
    env_id     TEXT NOT NULL,
    protocol   TEXT NOT NULL,
    port       INTEGER NOT NULL,
    "desc"     TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX IF NOT EXISTS idx_svc_tenant_env ON gov_services(tenant_id, env_id, app_id);

CREATE TABLE IF NOT EXISTS gov_instances (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    service_id TEXT NOT NULL,
    addr       TEXT NOT NULL,
    status     TEXT NOT NULL,
    lane_id    TEXT NOT NULL DEFAULT 'default',
    meta       JSONB NOT NULL DEFAULT '{}'::jsonb,    -- map[string]string
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_inst_service ON gov_instances(service_id);

CREATE TABLE IF NOT EXISTS gov_routes (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    path       TEXT NOT NULL,
    service_id TEXT NOT NULL DEFAULT '',
    methods    JSONB NOT NULL DEFAULT '[]'::jsonb,    -- []string (GET|POST|PUT|DELETE|ANY)
    strip_path BOOLEAN NOT NULL DEFAULT FALSE,
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX IF NOT EXISTS idx_routes_service ON gov_routes(service_id);

CREATE TABLE IF NOT EXISTS gov_breakers (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    name         TEXT NOT NULL,
    service_id   TEXT NOT NULL DEFAULT '',
    strategy     TEXT NOT NULL,
    threshold    INTEGER NOT NULL,
    min_requests INTEGER NOT NULL,
    window_secs  INTEGER NOT NULL,
    enabled      BOOLEAN NOT NULL DEFAULT TRUE,
    updated_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);
CREATE INDEX IF NOT EXISTS idx_breakers_service ON gov_breakers(service_id);

-- ===== configcenter：namespace + draft 配置项 + 发布快照 =====
CREATE TABLE IF NOT EXISTS cc_namespaces (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    name       TEXT NOT NULL,
    "desc"     TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL,
    UNIQUE (tenant_id, name)
);

CREATE TABLE IF NOT EXISTS cc_items (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    namespace_id TEXT NOT NULL,
    key          TEXT NOT NULL,
    value        TEXT NOT NULL DEFAULT '',
    type         TEXT NOT NULL DEFAULT 'text',        -- text | json | yaml
    updated_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (namespace_id, key)
);
CREATE INDEX IF NOT EXISTS idx_ccitems_ns ON cc_items(namespace_id);

CREATE TABLE IF NOT EXISTS cc_publishes (
    id           TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    namespace_id TEXT NOT NULL,
    version      INTEGER NOT NULL,
    snapshot     JSONB NOT NULL DEFAULT '{}'::jsonb,  -- map[string]string 不可变快照
    status       TEXT NOT NULL,                       -- active | rolled-back
    created_at   TIMESTAMPTZ NOT NULL,
    UNIQUE (namespace_id, version)
);
CREATE INDEX IF NOT EXISTS idx_ccpub_ns ON cc_publishes(namespace_id);

-- ===== billing：配额 / 用量 / 账单 =====
-- CheckAndInc 横切配额拦截：事务内 SELECT ... FOR UPDATE 串行化同租户并发，超 limit 回滚。
-- GenerateBill 同 period unpaid 覆盖：ON CONFLICT (tenant_id, period) DO UPDATE。
-- PayBill 状态机 unpaid -> paid：WHERE status='unpaid'，RowsAffected==0 拒绝重复支付。
CREATE TABLE IF NOT EXISTS billing_quotas (
    tenant_id  TEXT PRIMARY KEY,
    limits     JSONB NOT NULL DEFAULT '{}'::jsonb,    -- map[string]int，-1=无限
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS billing_usages (
    tenant_id  TEXT PRIMARY KEY,
    counts     JSONB NOT NULL DEFAULT '{}'::jsonb,    -- map[string]int
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS billing_records (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NOT NULL,
    period     TEXT NOT NULL,                         -- YYYY-MM
    items      JSONB NOT NULL DEFAULT '[]'::jsonb,    -- []BillItem
    total      DOUBLE PRECISION NOT NULL,
    status     TEXT NOT NULL,                         -- unpaid | paid
    created_at TIMESTAMPTZ NOT NULL,
    paid_at    TIMESTAMPTZ NULL,
    UNIQUE (tenant_id, period)
);
CREATE INDEX IF NOT EXISTS idx_billing_records_tenant ON billing_records(tenant_id);

-- ===== security：密钥/证书（平台级 + 租户级） + 审计日志 =====
-- 平台级 tenant_id 为 NULL，全租户可见（ListSecrets OR scope='platform'，Resolve 仅 platform 返明文）。
-- 两个 partial unique index 互不干扰：platform 按 name 全局唯一；tenant 按 (tenant_id,name) 租户内唯一。
-- Secret 值明文存储，API 返回掩码。真实加密（KMS/Vault）留后续。AuditLog 只增不删（合规）。
CREATE TABLE IF NOT EXISTS secrets (
    id         TEXT PRIMARY KEY,
    tenant_id  TEXT NULL,                             -- 平台级为 NULL
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,                         -- secret | certificate
    scope      TEXT NOT NULL,                         -- tenant | platform
    value      TEXT NOT NULL,                         -- 明文存储，API 返回掩码
    "desc"     TEXT NOT NULL DEFAULT '',              -- desc 是 PG 保留字，强制引用
    updated_at TIMESTAMPTZ NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS uniq_secret_platform ON secrets(name) WHERE scope = 'platform';
CREATE UNIQUE INDEX IF NOT EXISTS uniq_secret_tenant ON secrets(tenant_id, name) WHERE scope = 'tenant';
CREATE INDEX IF NOT EXISTS idx_secrets_tenant ON secrets(tenant_id) WHERE scope = 'tenant';

CREATE TABLE IF NOT EXISTS audit_logs (
    id            TEXT PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    actor         TEXT NOT NULL,
    action        TEXT NOT NULL,                      -- create | update | delete | login | login_failed | logout
    resource_type TEXT NOT NULL,
    resource_id   TEXT NOT NULL,
    detail        TEXT NOT NULL DEFAULT '',
    at            TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_tenant_time ON audit_logs(tenant_id, at DESC);

-- ===== maas：模型目录（平台级，全租户共享；无 tenant_id，不走 RLS） =====
-- Model/Channel 三层抽象持久化。channel.model_id FK CASCADE 随模型级联清。
-- capabilities 用 JSONB 存 []string；通道 impl 不入库（运行时 BuildProvider 按 type 构造）。
CREATE TABLE IF NOT EXISTS maas_models (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    vendor         TEXT NOT NULL DEFAULT '',
    context_window INTEGER NOT NULL DEFAULT 0,
    capabilities   JSONB NOT NULL DEFAULT '[]'::jsonb,    -- []string
    input_price    DOUBLE PRECISION NOT NULL DEFAULT 0,
    output_price   DOUBLE PRECISION NOT NULL DEFAULT 0,
    "desc"         TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS maas_channels (
    id             TEXT PRIMARY KEY,
    model_id       TEXT NOT NULL REFERENCES maas_models(id) ON DELETE CASCADE,
    type           TEXT NOT NULL,                          -- echo | mock | openai-compatible
    priority       INTEGER NOT NULL DEFAULT 0,
    status         TEXT NOT NULL DEFAULT 'healthy',
    endpoint       TEXT NOT NULL DEFAULT '',
    vendor         TEXT NOT NULL DEFAULT '',
    upstream_model TEXT NOT NULL DEFAULT '',
    credential_ref TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_maas_ch_model ON maas_channels(model_id);

-- ===== 行级安全（RLS）：第二道防线（查询层仍强制 tenant 过滤） =====
-- POLICY 语义：tenant_id = current_setting('app.tenant_id', true)
--   未设 app.tenant_id → current_setting 返 NULL → 放行（不破坏查询层过滤路径）
--   已设 app.tenant_id → DB 强制按租户过滤（纵深防御，绕过查询层也安全）
ALTER TABLE applications ENABLE ROW LEVEL SECURITY;
CREATE POLICY apps_tenant_isolation ON applications
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE workloads ENABLE ROW LEVEL SECURITY;
CREATE POLICY wl_tenant_isolation ON workloads
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE users ENABLE ROW LEVEL SECURITY;
CREATE POLICY users_tenant_isolation ON users
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE data_services ENABLE ROW LEVEL SECURITY;
CREATE POLICY ds_tenant_isolation ON data_services
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);

ALTER TABLE environments ENABLE ROW LEVEL SECURITY;
CREATE POLICY env_tenant_isolation ON environments
  USING (tenant_id = current_setting('app.tenant_id', true) OR current_setting('app.tenant_id', true) IS NULL);
