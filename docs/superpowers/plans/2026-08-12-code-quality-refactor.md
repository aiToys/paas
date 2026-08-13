# 代码质量与开源标准优化计划（2026-08-12）

> 进度跟踪文件，跨会话恢复用。

## 背景
4 个 item（paas-shop baseline / admin 菜单 / traffic-pulse / drift 审计）已完成并部署。
进入扩展目标：代码可读性、抽取公共代码、模块关系优化、推进开源标准。

## DRY 抽取进度

### ✅ #1 tenantOrErr 统一（已完成）
- 在 `pkg/tenant/tenant.go` 加 `IDOrErr(ctx)(string,error)` + `ErrMissingContext` sentinel
- 删除 10 处通用版本地定义（appconfig/billing/configcenter/dataservice/devops/environment/governance/observability/security/workload 的 memory store）
- `storage/pg.TenantOrErr` 改为委托 `tenant.IDOrErr`（单一真源）
- **保留** 6 处特殊版（返领域 sentinel 防泄漏，不统一）：
  - devops/pipeline/store_memory.go（ErrNoTenant）
  - ai/{agent,prompt,knowledgebase,eval,tool}/memory（各 ErrXxxNotFound，跨租户不泄漏）
- 编译 + vet + 全包测试通过

### ✅ #2 admin 公共助手（已完成）
新建 `internal/web/admin` 包提供：
- `TenantCtx(r, tid) (ctx, *Request)` — 派生资源租户 ctx
- `AuditRecorder` 接口（单一真源）

删除 7 处 `adminTenantCtx` 重复定义（devops/application/security/observability/governance/workload/billing），调用点改用 `adminutil.TenantCtx`。
**特例保留**：security `tenantCtxForSecret`（平台级 Secret 空 tenant → sentinel "platform"，不复用通用 TenantCtx）。

### ✅ #3 AdminAuditRecorder 接口统一（已完成）
10 处 `type AdminAuditRecorder interface{...}` → `type AdminAuditRecorder = adminutil.AuditRecorder`（类型别名，单一真源）。
涉及：devops/application/maas/security/dataservice/observability/governance/environment/workload/billing。

### ⏳ #4 fakeAudit test 重复（待做，低优先）
8 个 admin_handler_test.go 重复 fakeAudit struct。各 test 包独立编译，共享需 testutil 包，收益小留后续。

## 留后续（CLAUDE.md 记录的低优先项）
- ResolveChannels Clone（gateway handler 锁外读 Status）
- observability/maas sync.Mutex → RWMutex（读热点）
- EnvTypeResolver DRY（pipeline/handler.go func 类型 vs 其余接口别名）

## 验收
每项抽取后：`go build ./...` + `go vet ./...` + `go test ./...` 全绿，最后 `./scripts/deploy-k8s.sh` 部署。
