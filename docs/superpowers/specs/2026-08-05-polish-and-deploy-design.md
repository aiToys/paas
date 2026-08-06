# 开源打磨 + 体验优化 + 深度检测 + k8s 部署 设计

日期：2026-08-05
范围：后台菜单重构 / 用户控制台模块互链 / 架构精简 / 10 轮深度代码检测 / k8s 部署

## 背景

项目完成度约 96%，core + 三套前端 + 15 模块已落地。本期是开源发布前的最后一轮"打磨 + 加固 + 交付"：
不新增大功能，聚焦**菜单合理性、模块互链体验、死代码精简、深度正确性检测、最终部署**。

## 决策记录（已定，不再逐项征询）

遵循用户目标指令「不要询问、中断后继续」，以下决策基于探索事实直接敲定：

### D1. 后台菜单（console-admin）重构
**问题**：图标撞车（Key×2 / Odometer×2 / Cpu×3）；注释承诺 5 组实际只 4 组（缺"平台运维"）；mock 仍是旧三级结构。
**方案**：
- 修正图标全局唯一（Key→Lock/Key、Odometer 拆、Cpu 收敛）。
- 资源总览 14 项按业务域二次分组（应用运行态 / DevOps 链路 / 平台能力 / 计费安全），或保持平铺但加分隔标题——选**平铺 + 图标修正**（YAGNI，避免多级嵌套过深）。
- mock/handlers/menu.ts 与后端 staticMenus() 对齐，或干脆改为透传后端（选**对齐**，保留 dev 离线可用）。
- 真源仍在 `internal/core/auth/menus.go`。

### D2. 用户控制台（console-user）模块互链
**问题**：13 处孤岛；应用↔工作负载/数据服务/可观测/DevOps 几乎不互链；工作负载详情是抽屉不可深链。
**方案**（按价值排序，分批做）：
1. 应用详情「部署」tab 工作负载行可点 → 打开工作负载详情（仍用抽屉，但跨页面联动）。
2. 应用详情「资源绑定」卡片可点 → 跳 `/resources/:kind/:id` 数据服务详情。
3. 应用详情新增「监控」入口 → `/platform/observability?app=:id`。
4. 应用详情 DevOps tabs 加「查看跨应用总览」→ `/devops`。
5. 工作负载表格 `appId` 列可点 → 跳应用详情。
6. 数据服务详情新增「绑定此资源的应用」反查面板（前端聚合：拉全部应用 bindings 过滤）。
7. 环境详情「在此环境工作」补 jobs/cronjobs 入口 + 环境内数据服务清单。
- **工作负载详情抽屉是否改路由**：保持抽屉（YAGNI，深链价值低于改造成本），但补深链 query 支持。

### D3. 架构精简 / 死代码清理
**方案**：grep + agent 扫描未引用文件、未使用导出、过期 mock、重复类型；逐个确认后删。
**原则**：只删确认无引用的，存量手写 interface 渐进迁移（不一次性重写，与既有 YAGNI 决策一致）。

### D4. 10 轮深度代码检测
**方案**：每轮一个聚焦维度，用 code-review skill / 审计 agent 驱动：
1. 多租户隔离正确性（ListAll vs List、跨 store 调用）
2. 并发安全（锁、深拷贝、goroutine 退出）
3. 资源泄漏（Body.Close / channel / ticker）
4. 错误处理与脱敏（WriteError vs WriteServiceError）
5. SQL 注入 / 参数化
6. 鉴权边界（adminGuard / prod:write / 越权）
7. 前端 reactivity / race / 轮询清理
8. API 契约一致性（{data:T} 解包、OpenAPI 登记）
9. 死代码 / 未引用导出
10. 部署清单与 chart 一致性
**修复原则**：只修确认的 bug，不引入新功能；每轮修完跑 `make test`。

### D5. k8s 部署
**方案**：`./scripts/deploy-k8s.sh`（构建前端 embed 镜像 + push 集群内 registry + helm upgrade）。遵循 [[k8s-always-latest]] 记忆——常驻授权，无需询问。部署后 e2e 验证关键端点。

## 实施顺序（Phase）

1. **Phase A**：console-admin 菜单重构（menus.go + mock 对齐 + 图标）。
2. **Phase B**：console-user 模块互链（D2 全部 7 项）。
3. **Phase C**：死代码清理（D3）。
4. **Phase D**：10 轮深度检测（D4），每轮修 + 测。
5. **Phase E**：前端 build 验证（vue-tsc + vite build）+ 后端 `make test`。
6. **Phase F**：k8s 部署 + e2e 验证。

## 非目标（YAGNI）
- 不新增业务模块。
- 不重写既有的 49 个手写 interface（渐进迁移）。
- 工作负载详情不改成独立路由（抽屉够用）。
- 不做菜单 path 前缀化重构（破坏稳定契约）。

## 验证
- `make test` 全绿。
- `pnpm build` 三套前端通过。
- k8s 部署后：landing/console/admin 可访问 + 关键 API 端点 200。
