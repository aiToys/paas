# 安全切片设计：密钥/证书管理 + 审计日志

> 平台能力（横切）。安全范畴：密钥 Secret / 证书 / IAM 访问控制 / 网络防火墙 / 合规审计。
> 本切片聚焦 **密钥与证书管理（KMS 抽象）+ 审计日志** 闭环。IAM 因已有 RBAC 身份骨架（identity），网络防火墙依赖数据面接入，二者留后续。

## 定位与区分

安全模块的 Secret/证书是**租户级平台资产**（独立 KMS），供应用引用，**不绑定具体应用**——这与 `appconfig`（应用×环境级、工作负载启动注入的 env/Secret）是两个层面：

| | appconfig（应用配置） | security.Secret（平台密钥） |
|---|---|---|
| 归属 | 应用详情→配置 tab | 平台能力→安全 |
| 绑定 | 应用 + 环境 | 租户级，应用引用 |
| 用途 | 工作负载启动注入 | 集中密钥/证书管理（DB 密码、TLS 证书、第三方 token） |
| 形态 | env / Secret 键值 | 命名密钥资产（secret / certificate） |

侧栏「平台能力 → 安全」，独立菜单。

## 范围

### 实体

```
Secret（密钥/证书资产，租户级）
  ID, TenantID, Name（租户内唯一）, Type（secret | certificate）,
  Value（后端明文存储，API 掩码返回）, Desc, UpdatedAt

AuditLog（审计日志，写操作自动记录）
  ID, TenantID, Actor（用户 ID，ctx 取）, Action（create/delete/...）,
  ResourceType（secret | ...）, ResourceID, Detail, At
```

### Repository（单 Store，带前缀方法）

- secret：`ListSecrets / GetSecret / CreateSecret / DeleteSecret`
- audit：`ListAuditLogs(filter) / RecordAudit(log)`
- 全方法租户强制过滤；Secret `Value` 在 `List/Get` 返回掩码（与 appconfig 一致，不泄漏长度/内容）。

### 关键行为

- **Secret 掩码**：后端明文存储，`List/Get` 返回 `SecretMask = "••••••"`（与 appconfig 掩码机制一致）。
- **审计自动记录**：handler 层在 Create/Delete Secret 成功后自动 `RecordAudit`（actor 从 ctx 的 userID 取，复用 `gateway.UserIDFrom`）。
- 审计日志只增不删（合规要求），`ListAuditLogs` 支持按 resourceType/action 过滤，按时间倒序。

### REST API

```
GET    /api/security/secrets             密钥列表（掩码）
POST   /api/security/secrets             创建密钥（记审计）
DELETE /api/security/secrets/{id}        删除密钥（记审计）
GET    /api/security/audit-logs?resourceType=&action=  审计日志
```

### 权限

- `security:read` / `security:write`（admin/dev 读写，viewer 只读）。并入 BuiltinRoles。
- 不接 prod:write（密钥是租户级资产，不按物理环境隔离）。
- 所有写操作自动记审计（无论成功/角色），便于合规追溯。

## 不做（YAGNI / 后续）

- IAM 细粒度访问控制（已有 RBAC，更细的资源级策略后续）。
- 网络防火墙 / 安全组 —— 依赖数据面接入。
- 密钥轮转 / 过期提醒 / 版本 —— 后续。
- 真实加密存储（KMS 集成 / Vault）—— 后续，本期明文存储 + 掩码返回。
- 证书签发（ACME / 自签）—— 后续，本期证书作为 Value 存储资产。
- 审计日志导出 / 告警 —— 后续。
