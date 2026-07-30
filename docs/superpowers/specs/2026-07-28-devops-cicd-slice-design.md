# DevOps CI/CD 切片设计

> 切片目标：补齐「代码 -> 构建 -> 镜像 -> 发布 -> 部署 -> 回滚」主链路。
> 当前 `Workload.Image` 是裸字符串，无来源/无 digest/无历史--构建产物缺失导致链路断裂。
> 本切片把 Image 做成不可变一等公民，串起 Repository（代码仓库绑定）-> BuildRun（构建）-> Image（制品）-> Release（发布编排）-> 回滚。
> 蓝图优先级：DevOps 模块（`platform-modules-blueprint.md` 第 55 行「CI/CD 流水线 / 制品镜像仓库 / 发布编排 / 回滚」）。
>
> 阶段约束：当前为进程内 mock 期（无真实 K8s/Git/Docker/Registry）。本切片与已有切片一致--
> **接口先行 + mock 实现 + 为未来接真实基础设施铺路**，不引入真实构建依赖。

## 范围

**做**：
- `internal/devops/` 包，四实体：
  - `Repository`：代码仓库绑定（Git URL/分支/Dockerfile 路径），归属应用
  - `BuildRun`：构建运行实例（触发来源/commit/状态/日志/产出镜像），mock CI runner 异步流转
  - `Image`：构建产物（digest 不可变真源 + tag + 来源 + 构建记录），归属应用
  - `Release`：发布单（应用×环境×镜像 + 策略 + 状态 + 回滚指针），编排目标 Workload
- `Workload` 加 `ImageRef`（digest 锁定），保留 `Image` 字符串兼容 display
- REST API：仓库绑定 / 构建触发 / 镜像列表 / 发布 / 回滚
- console-user 应用详情新增 tab：代码仓库 / 构建 / 镜像 / 发布
- 生产安全：发布到 prod 环境 + prod 回滚受 `prod:write`（横切框架自动生效）
- 多租户：四实体均带 TenantID，Repository 强制 tenant 过滤

**不做（YAGNI/后续切片）**：
- 真实 Git clone / docker build / push registry（mock CI runner 模拟产出）
- 真实 K8s 部署（Release 更新 Workload mock，不真调度）
- 蓝绿/金丝雀流量切分实现（`Strategy` 字段支持，起步只实现 rolling，其余降级 rolling + 标注）
- 灰度发布（与服务治理泳道耦合，归后续服务治理切片）
- GitOps（Release 真源 CRD 化，归后续）
- 构建缓存/并行/分布式构建（单机 mock）
- 镜像漏洞扫描/签名（归安全切片）

## 领域模型

包 `internal/devops/`，复用 `pkg/tenant` 多租户隔离。

### Repository（代码仓库绑定）

```go
// Repository 是应用绑定的代码仓库（Git）。归属应用，一个应用可绑多个仓库。
type Repository struct {
    ID           string    // repo-acme-order
    TenantID     string    // ctx 写入，请求体忽略
    AppID        string    // 归属应用
    GitURL       string    // https://github.com/acme/order-svc.git
    Branch       string    // main（默认分支，触发构建的分支）
    Dockerfile   string    // Dockerfile 相对路径，默认 "Dockerfile"
    BuildContext string    // 构建上下文，默认 "."
    Status       string    // active | disabled
    CreatedAt    time.Time
}
```

### BuildRun（构建运行实例）

```go
// BuildRun 是一次构建运行。mock CI runner 创建后异步流转 pending->running->success，
// 成功产出 Image；失败（mock 不触发，预留）产出空 ImageID。
type BuildRun struct {
    ID         string    // build-xxx
    TenantID   string
    AppID      string
    RepoID     string    // 触发来源仓库
    Trigger    string    // push | manual | pr
    Commit     string    // git commit SHA（mock 随机生成）
    Branch     string    // 分支
    Message    string    // commit message（mock）
    Status     string    // pending | running | success | failed
    ImageID    string    // 构建成功产出的镜像 ID；失败/进行中为空
    Log        string    // 构建日志（mock 预设文本）
    StartedAt  time.Time
    FinishedAt time.Time // 零值表示未结束
}
```

### Image（构建产物，不可变一等公民）

```go
// Image 是构建产物。digest 是不可变真源（生产部署锁这个），tag 可变。
// 来源记录 BuildRun + commit，可追溯。归属应用，跨环境复用（test 验证通过晋升 prod）。
type Image struct {
    ID        string    // img-xxx
    TenantID  string
    AppID     string
    Registry  string    // 仓库地址，如 registry.acme.com/order-svc
    Tag       string    // main-abc12345（可变标签）
    Digest    string    // sha256:...（不可变真源，生产部署锁定）
    Source    string    // commit SHA
    Branch    string
    BuildRunID string   // 产出此镜像的构建
    BuiltAt   time.Time
    Status    string    // ready
}
```

### Release（发布单）

```go
// Release 是一次发布：把某镜像以某策略部署到某环境，编排目标环境的基线 Workload。
// 记录 PreviousImageID 用于回滚。生产发布受 prod:write 保护。
type Release struct {
    ID              string    // rel-xxx
    TenantID        string
    AppID           string
    EnvID           string    // 目标环境
    ImageID         string    // 部署的镜像
    ImageDigest     string    // 冗余快照（部署时镜像 digest）
    Strategy        string    // rolling | blue-green | canary（起步只实现 rolling）
    Status          string    // pending | deploying | succeeded | failed | rolled-back
    WorkloadID      string    // 编排目标工作负载
    PreviousImageID string    // 回滚指针：发布前的镜像
    IsRollback      bool      // 是否为回滚发布
    CreatedAt       time.Time
    CreatedBy       string    // 触发者用户 ID
}
```

### Workload 改动

```go
type Workload struct {
    ... 现有字段
    Image    string `json:"image"`        // display 字符串（兼容）
    ImageRef string `json:"imageRef,omitempty"` // 不可变 digest（生产锁定，Release 写入）
}
```
- Release 编排 = 找/建目标环境基线 Workload，更新 `ImageRef`（digest）+ `Image`（display）
- 部署该校验：生产 Workload 的 `ImageRef` 非空（锁 digest），测试可空（兼容旧手填）

## mock CI runner

`internal/devops/memory/store.go` 内建 mock 构建器：

- `BuildRun` 创建时 `Status=pending`，启动 goroutine：
  - 置 `running` -> `time.Sleep(800ms)`（模拟构建耗时）-> 置 `success`
  - 产出 `Image`：`Digest = "sha256:" + sha256(commit+appID+timestamp)`、`Tag = "{branch}-{commit[:8]}"`、`Source = commit`、`BuildRunID = buildrun.ID`
  - 回填 `BuildRun.ImageID`
  - `Log` 预设多行文本（Step 1/5 ... Built）
- 并发安全：store 用 `sync.Mutex` 保护
- 失败路径：mock 不主动触发（预留 `Trigger` 语义）；测试可注入失败

## REST API

| 方法 路径 | 权限 | 说明 |
|---|---|---|
| GET `/api/applications/{id}/repositories` | repository:read | 应用下仓库列表 |
| POST `/api/applications/{id}/repositories` | repository:write | 绑定仓库 |
| DELETE `/api/applications/{id}/repositories/{rid}` | repository:write | 解绑 |
| POST `/api/applications/{id}/buildruns` | build:write | 触发构建（body: repoId, trigger, commit?） |
| GET `/api/applications/{id}/buildruns` | build:read | 构建记录列表 |
| GET `/api/buildruns/{id}` | build:read | 构建详情（含日志） |
| GET `/api/applications/{id}/images` | image:read | 镜像列表 |
| GET `/api/images/{id}` | image:read | 镜像详情 |
| POST `/api/applications/{id}/releases` | release:write + prod:write(若prod) | 创建发布 |
| GET `/api/applications/{id}/releases` | release:read | 发布历史 |
| POST `/api/releases/{id}/rollback` | release:write + prod:write(若prod) | 回滚到上一镜像 |

权限并入 `identity.BuiltinRoles`：
- 新增 `repository:read/write`、`build:read/write`、`image:read`、`release:read/write`
- `tenant-admin`：全有 + `prod:write`
- `developer`：读写（生产写受 `prod:write` 拦）+ `image:read`
- `viewer`：全部只读

## 发布编排流程

```
POST /api/applications/{id}/releases {envId, imageId, strategy}
  -> 校验 release:write
  -> 校验 prod:write（若目标环境 prod，dev 被拦）
  -> 取 Image（校验同租户 + 属本应用）
  -> 找目标环境基线 Workload（tenant, app, env, lane=default, type=service）
     -> 不存在则创建（基线，Replicas=1）
  -> 记录 Workload 当前 ImageRef 为 PreviousImageID
  -> 更新 Workload.ImageRef = image.digest + Workload.Image = registry:tag
  -> Release.Status = succeeded
```

回滚：
```
POST /api/releases/{id}/rollback
  -> 校验 release:write + prod:write（若 prod）
  -> 取 Release.PreviousImageID（空则 409 无法回滚）
  -> 更新 Workload 回 PreviousImage 对应 digest/image
  -> 原 Release.Status = rolled-back
  -> 创建新 Release（IsRollback=true, ImageID=PreviousImageID, Status=succeeded）
```

## 前端

应用详情新增四 tab（与现有「概览/资源/部署」并列）：

1. **代码仓库**：已绑仓库列表 + 绑定表单（GitURL/分支/Dockerfile）+ 解绑
2. **构建**：BuildRun 列表（状态徽标/commit/分支/产出镜像/时间）+「触发构建」按钮（选仓库）+ 点行展开日志（mock 文本，轮询状态直到 success）
3. **镜像**：Image 列表（digest 短显示/tag/来源 commit/构建时间）+ 「发布」快捷按钮（跳发布 tab 预选该镜像）
4. **发布**：Release 历史（环境/镜像/策略/状态/回滚标记）+「创建发布」表单（选镜像+环境+策略）+ 回滚按钮（生产环境走 useDangerConfirm 输入名称确认）

环境上下文：发布表单的目标环境默认取全局 env store（顶栏当前环境），可改。

## 和已有切片的契合

- **环境**：Release 选目标环境；同一镜像可从 test 晋升 prod（不重复 build）
- **泳道**：联调泳道挂某 commit 镜像（LaneID 非 default），本期不实现泳道发布，预留
- **生产安全防护**（横切）：
  - 发布到 prod / prod 回滚受 `prod:write`（注入 EnvTypeResolver）
  - 前端生产发布/回滚走 `useDangerConfirm`（输入名称确认）
  - 生产环境整页红边框视觉自动继承
- **Workload**：Release 编排目标，加 `ImageRef`
- **多租户**：四实体 Repository 强制 tenant 过滤，跨租户 not found 不泄漏

## seed

- 仓库：`app-etl` 绑 `https://github.com/acme/etl-pipeline.git`（main 分支）
- 构建记录：2 条（1 success 产出 img-001，1 running 演示中态）
- 镜像：`img-001`（digest sha256:abc...，tag main-abc12345，来源 commit）
- 发布：1 条（`app-etl` -> `env-prod-sh` -> `img-001`，succeeded）

## 验收

- 链路：绑仓库 -> 触发构建 -> 等 success -> 见 Image -> 发起到 test 环境 -> Release succeeded + Workload.ImageRef 更新
- 回滚：发布后回滚 -> Workload.ImageRef 回退 + 原 Release rolled-back + 新 Release 标记
- 隔离：`sk-acme-admin` 仅见 acme 仓库/镜像/发布；跨租户 404
- 生产安全：`sk-acme-dev`（无 prod:write）发起到 `env-prod-sh` 403；admin 200；dev 发起到 `env-test` 200
- mock 流转：BuildRun pending -> running -> success（前端轮询可见状态变化 + 日志）
- `go test -race` 全绿；新增单测覆盖：Image 隔离、BuildRun 状态流转 + 产出 Image、Release 编排更新 Workload.ImageRef、回滚、prod 权限
- `make lint` 0；`gofmt` 干净；前端三套 build 通过

## 架构约束

- 业务领域逻辑（构建/发布）在 `internal/devops/` 插件化子系统，**不进 Platform Core**
- 多租户隔离复用 `pkg/tenant`，Repository 强制 tenant 过滤
- 生产安全横切复用已有 `EnvTypeResolver` + `prod:write` + `useDangerConfirm`，不重复实现
- mock 期不引入真实 Git/Docker/Registry 依赖；接口为未来接真实 OCI registry / Tekton / Argo 铺路
- Apache 2.0：无新外部依赖（`crypto/sha256`、`encoding/hex` 均标准库）
- 发布策略接口开放（`Strategy` 字段），实现 YAGNI（只 rolling），蓝绿/金丝雀归后续

## 任务分解

| 任务 | 内容 |
|---|---|
| DEV-T1 | `Image` 领域 + Repository 接口 + 内存实现 + seed |
| DEV-T2 | `Repository`（代码仓库）领域 + 内存实现 + seed |
| DEV-T3 | `BuildRun` 领域 + mock CI runner（异步流转 + 产出 Image）+ 单测 |
| DEV-T4 | `Release` 领域 + 编排（更新 Workload.ImageRef）+ 回滚 + Workload 加 ImageRef |
| DEV-T5 | handler（REST 全套）+ 权限 + prod:write 校验（EnvTypeResolver）+ 单测 |
| DEV-T6 | cmd/core 装配（注入 EnvTypeResolver + Workload Repository）+ composite 路由 |
| DEV-T7 | 前端应用详情四 tab（仓库/构建/镜像/发布）+ 发布回滚交互 + 危险确认 |
| DEV-T8 | 文档同步（CLAUDE.md / 蓝图 / README）+ 端到端验证 |
