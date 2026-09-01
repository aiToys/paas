# 核心概念

## 应用 × 环境 × 工作负载

```
应用 (Application)
 ├── 环境绑定: 测试 (test) ←→ 生产 (prod)     # 多对多，发布阶序可配
 ├── 工作负载 (Workload)                       # Service / Job / CronJob
 ├── 配置 (AppConfig)                          # 应用 × 环境静态注入
 └── 数据服务绑定 → 连接串自动注入
```

- **应用**是主线：配额、计费、成员权限都挂在应用维度
- **环境**是一等公民（`test` / `prod`），生产环境写操作需要 `prod:write` 权限
- **发布阶序**：测试 → 生产按 PromoteOrder 晋升，禁止跳级

## 泳道（Lane）

环境内的**流量隔离单元**：

- 联调泳道 `feature-x`：只部署变更服务，未命中流量降级到基线服务
- 染色透传：SDK `LaneMiddleware` 入口染色，trace / 日志 / 指标全链路带 lane 标签
- standard 泳道 TTL 自动回收；permanent 泳道常驻（大项目 / 火车分支）
- 金丝雀发布 = `canary-<runID>` 临时泳道 1 副本并行验证

## 流水线（Pipeline）

模板 + 绑定模型：

- **模板**定义阶段编排（内置 CI / CD 模板，Version 升级自动覆盖）
- **流水线**绑定模板 + 参数覆盖，占位符（如 `app.env.prod` 双花括号语法）在触发时解析固化
- 8 种阶段：build / deploy / test / approve / release / promote / baseline / canary
- 单实例串行（并发触发 409），生产流静态预演防越权组合

## 多租户与权限

| 层 | 模型 |
|----|------|
| 租户级 RBAC | tenant-admin / developer / viewer |
| 应用级 | owner / maintainer / developer / viewer（受限应用 AppGuard 强制） |
| API 认证 | cookie 会话（控制台）/ API Key（程序化），双通道 |

跨租户访问统一 404 不泄漏存在性；生产写操作 fail-closed。

## 交付形态

- **SaaS**：Helm 公网安装
- **私有化**：`airsync bundle` 打离线包 → 物理介质 → `airsync install`（sha256 校验 → 镜像导入 → helm 安装）
