# 生产安全防护切片设计

> 切片目标：把「生产/测试隔离」从各切片的责任升为**平台级横切机制**，杜绝生产误操作。
> 核心认知：生产/测试隔离是横切关注点，必须平台层统一解决，否则每个功能切片（工作负载/DevOps/应用配置）重复犯搞混问题。
> 做完后后续切片在防护框架内开发，自动继承隔离能力。

## 范围

**做**：
- 后端：环境类型感知 RBAC--生产写操作需 `prod:write`，dev 角色生产只读（最硬防线）
- 前端：全局环境上下文（pinia store + 顶栏选择器，贯穿所有页面）
- 前端：生产 gated 模式（切换确认 + 超时回退测试）
- 前端：生产视觉强隔离（红主题：顶栏红条 + 整页红边框 + 警示横幅）
- 前端：统一危险操作确认（生产写操作强制确认，删除要求输入名称）

**不做**：
- K8s namespace 硬隔离（未来）
- 独立生产控制台（私有化预留，平台架构支持按环境域拆分但不默认拆）

## 后端：环境类型感知 RBAC（横切防护核心）

### 权限模型

`identity.BuiltinRoles` 新增 `prod:write`：
- `tenant-admin`：有 `prod:write`（生产可写）
- `developer`：**无 `prod:write`**（生产只读）
- `viewer`：无（全只读）

### 校验机制

生产写操作需额外校验 `prod:write`。关键是「感知目标环境类型」：

- **environment handler 写操作**（Create）：body 带 `type`，若 `type==prod` 则校验 `prod:write`（不需查 repo）
- **workload handler 写操作**（Create/Update/Delete）：目标环境类型需查--注入 `EnvTypeResolver` 接口解析 envID -> type，若 prod 则校验 `prod:write`

```go
// workload 包定义（依赖倒置，避免直接 import environment）
type EnvTypeResolver interface {
    EnvType(ctx context.Context, envID string) (string, error)
}
```

- `environment.Repository` 加 `EnvType(ctx, id) (string, error)` 方法，实现 `EnvTypeResolver`
- cmd/core 把 environment store 注入 workload handler

gateway 导出 `RequestAllowedProd(r)` 校验 `prod:write`。

### 校验流程（workload 写操作）

```
Create/Update/Delete
  -> 解析目标 envID（Create 从 body；Update/Delete 从现有 workload 的 envID）
  -> envResolver.EnvType(ctx, envID)
  -> if type==prod: 校验 prod:write（dev 角色被拦）
  -> 校验基础权限 workload:write
```

dev 角色在测试环境正常写，在生产被 `prod:write` 拦截--**权限兜底，即便界面看错也改不了生产**。

## 前端：全局环境上下文

### pinia store（`stores/env.ts`）

```ts
state: { currentEnv: Env | null, enteredProdAt: number }
actions: switchEnv(env)  // 生产需确认 + 记录时间；超时回退
getters: isProd
```

- 顶栏全局环境选择器（常驻），当前环境贯穿所有页面
- 工作负载/应用详情等页面从 store 读 currentEnv，不再各自管 activeEnv
- 切换环境触发各页面重载（已有 paas:key-changed 机制类似，加 paas:env-changed）

## 前端：生产 gated 模式

- 切换到生产环境：弹确认框「你将进入生产环境，操作请谨慎」
- 生产会话**15 分钟超时**自动回退测试环境 + 提示
- 顶栏生产时显示剩余时间倒计时

## 前端：生产视觉强隔离

- 当前环境是生产时：
  - 顶栏底部加**红色条**
  - 整页加**红色边框**（body class `env-prod`）
  - 页面顶部**警示横幅**「⚠️ 生产环境 - 操作请谨慎」
- 工作负载卡片/环境分组：生产**红边框**、测试**绿边框**（强对比，非小图标）
- CSS 变量驱动，所有页面自动应用

## 前端：统一危险操作确认

`composables/useDangerConfirm.ts`：
- `confirmDangerous(action, target, isProd)`
- 生产环境：ElMessageBox 强制确认 + 高危（删除）要求**输入名称**确认
- 测试环境：普通确认
- 工作负载的 scale/remove、环境的 delete 调用此 composable

## 验收

- 权限：`sk-acme-dev` 在测试环境能创建/扩缩容工作负载；在生产环境被 `prod:write` 拦截（403）
- `sk-acme-admin` 在生产环境可写（有 prod:write）
- 前端：切到生产需确认；生产整页红主题 + 警示横幅；15分钟超时回退
- 危险操作：生产删除工作负载要求输入名称；测试普通确认
- `go test -race` 全绿；新增单测覆盖 prod:write 校验、EnvTypeResolver
- `make lint` 0；前端三套 build 通过

## 架构约束

- 防护是**横切机制**，后续切片（DevOps/应用配置）自动继承：全局环境上下文、环境类型 RBAC、危险确认、视觉警示
- 依赖倒置：workload 定义 EnvTypeResolver 接口，不直接 import environment
- 安全不可逆，YAGNI 在此谨慎--宁可早建框架
- Apache 2.0：无新外部依赖

## 后续切片如何受益

DevOps/应用配置切片开发时：
- 自动继承全局环境上下文（不管环境切换）
- 写操作自动受 prod:write 保护（handler 注入 EnvTypeResolver 即可）
- 生产操作自动有视觉警示和确认（调用 useDangerConfirm）
- 切片只关注业务逻辑，隔离由平台层兜底
