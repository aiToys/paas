# 演示Workload自动部署与ImagePullBackOff修复

日期：2026-10-21（Asia/Shanghai）。用户在确认这些Pod来自PaaS演示初始化后要求修复。本次完成源码修复、K8s部署、限定清理和重启验证；未commit或push。

## 最终结果

- 默认禁止自动创建演示Workload，内存与PG启动路径共用独立门控。
- Helm release `paas` revision **383**，状态`deployed`；新增`seed.workloads=false`，原登录/模型/凭证等Helm值保持。
- 镜像：`192.168.41.122:30050/paas/paas-core:demo-seed-fix-20261021-104131`。
- Pod镜像摘要与registry一致：`sha256:37c7f213abb1f23cd6fe8361d81a2e162e3b634c0ecbc27532803ce9fe559f57`。
- 备份后通过现有管理API删除**7条**精确确认的演示Workload，关联**10个**失败Pod级联回收。
- 当前集群**84个Pod，镜像拉取失败0个**；演示数据库记录0、对应CR 0。
- 清理后再次滚动重启Core，数据库为空时没有重新灌入或创建演示资源。
- Workload配额由Acme 4、Globex 3回收到0，与实际记录一致；新增7条删除审计。原管理员会话仍可访问后台。

## 根因与源码改动

历史初始化把演示Workload写入PG，并经ApplyRepo投影为真实CR、Deployment/Job/CronJob和Pod。演示登录与真实负载共用`seed.demo`，默认开启；示例引用`nginx:stable`，仓库只有`alpine`，依赖同步清单又遗漏nginx。

本次将“允许自动创建演示Workload”单独改为显式选择：

- `PAAS_ENABLE_DEMO_WORKLOADS=true`才允许初始化；默认不灌入。
- 全局`PAAS_DISABLE_DEMO_SEED=true`优先，独立开启也不能绕过全局关闭。
- `memory.NewStore`默认空仓储，PG `seedWorkloads`使用同一判定。
- 依赖演示baseline的既有测试改为显式`WithDemoSeed(true)`夹具，不再依赖运行时默认副作用。
- chart与本集群values增加`seed.workloads=false`；Deployment显式传`PAAS_ENABLE_DEMO_WORKLOADS=false`。
- `scripts/sync-images.sh`和依赖文档补齐真正的nginx stable/alpine两个标签，供显式演示和hello-web模板使用。当前集群不启动这些演示资源，没有通过把alpine改标stable来掩盖问题。
- 同步脚本的临时镜像tar改为根目录`tmp/image-sync/`，遵守临时文件规范。

## 清理范围与恢复

精确匹配并备份以下7条：`wl-cs-api`、`wl-rec-svc`、`wl-acme-recall`、`wl-acme-report`、`wl-agent-gw`、`wl-etl-backfill`、`wl-etl-nightly`。

清理前核对租户、应用、环境、类型、名称、副本、镜像、命令、计划及统一创建时间（2026-10-21 07:06:01.108354）；确认没有镜像digest、发布引用或运维操作引用。执行前再次比较PG原始行，变化即中止；没有按命名空间、前缀或镜像批量删除。

先部署关闭门控，再通过`DELETE /api/admin/workloads/{id}`逐条清理。由既有领域路径同步删除PG与对应CR、回收配额并写真实管理员审计；没有直接改库绕过领域流程。非演示CR按UID/spec核对保留，应用、服务、租户及其他业务定义没有删除。

受限权限的原始记录、CR备份、配额快照、恢复SQL（仅生成，未执行）及执行日志保存在根目录`tmp/demo-workload-fix-2026-10-21/`。本次维护会话已注销，token文件已删除。恢复资料不入Git、不进入镜像构建上下文。

## 验证证据

| 检查 | 结果 |
|---|---|
| 全量`go test ./...` | 通过；曾依赖隐式演示baseline的发布测试改为显式夹具后通过。 |
| `go test -race ./internal/workload/... ./internal/devops/memory ./cmd/core` | 通过。 |
| 新门控回归 | 覆盖默认关闭、显式关闭、无效值、显式开启、全局关闭优先及PG Create的租户作用域；新增3个测试函数、10个用例。 |
| `helm lint deploy/charts/paas` | 通过。 |
| Helm三种开关组合 | 通过。 |
| 清单差异 | 仅Core镜像和新增的PAAS_ENABLE_DEMO_WORKLOADS环境变量；其他资源配置无变化。 |
| Docker镜像构建 | 通过，四套前端与文档复用构建缓存，Core重新编译。 |
| 脚本语法与`git diff --check` | 通过。 |
| 实际管理API清理 | 7条成功，7条审计、配额一致。 |
| 重启后再次检查 | 演示PG行/CR未重建；ImagePullBackOff、ErrImagePull等拉取失败均为0；Core Ready、`/livez`正常，原管理员身份仍可访问。 |

这是限定演示Workload的清理；不将其他演示元数据或管理后台原设计延期能力计作本次交付。
