# 六专项完成情况复核：第二轮

后续用户已授权修复，最新实现与验证见 [修复记录](repairs-2026-10-19.md)。本文件保留为修复前的问题快照。

日期：2026-10-19（Asia/Shanghai）。对照 [第一轮复核](completion-check-2026-10-19.md) 与 [六专项设计](../../superpowers/specs/2026-10-17-console-module-improvements-design.md)。

## 结论

**比第一轮继续推进，但仍未全部完成。** API Key 明文清理已在源码补齐；Secret 关闭清理、Billing 非 2xx 错误与口径展示继续改善；数据服务已新增 PostgreSQL 快照/恢复 Job、状态观察器与能力接口，不能再笼统称为“仅有模拟记录”。

配置版本冲突、Playground 入口能力约束、Agent 运行依赖透明度仍未闭环；新增恢复路径还存在配额、完整性与失败重试方面的缺口。真实恢复、PG 迁移、浏览器交互与跨角色验收仍未取得本轮通过证据。

检查对象仍为 `console-v2`、HEAD `bd7e51f` 加工作区中的后续开发修改。当前有其他开发者未提交的实现，本轮只检查并补充文档，不改业务源码、不提交或部署。源码修复不直接等同真实环境验收通过。

## 与上一轮相比

| 上一轮问题 | 本轮新状态 | 判定 |
|---|---|---|
| Key 弹窗关闭保留明文 | ApiKeys 增加 clearCreated、@close、onUnmounted；Secret 创建弹窗也增加 resetForm | 源码已修复；交互压力场景尚未浏览器实测 |
| 备份仅记录/模拟大小 | 新增 postgres pg_dump/pg_restore 执行器、PVC、Job、Pacer、capabilities、恢复新实例、migration 0053；记录模式不再生成假大小 | 已接入执行代码，不能继续按旧缺项描述；恢复验收未完成 |
| Billing 非 2xx 无明确状态 | loadUsage/loadBills 增加各自错误文案，区分强制拦截/未采集资源并强化当前快照估算口径 | 基础语义已有改进；网络/格式错误及真实时段计量仍需完善 |
| SSE 完成/中断/停止未区分 | Msg 增加 stopped/interrupted；未遇 DONE 的结束与用户停止已有标记 | 部分修复；错误帧、损坏 JSON 和后续历史使用仍需处理 |
| 配置仅 count 校验 | 仍为 expectedCount；同数量值变化无法识别，读取失败传 -1 可跳过检查 | 未解决 |
| Playground 其他能力仍发 Chat | 下拉仍包含所有模型；仅 embedding 单独识别，其他进入 Chat 分支 | 未解决 |
| Agent 依赖静默降级 | Runtime 仍跳过失效 Skill/检索失败；只调用 MCP，选择器仍允许 enabled 的 HTTP/builtin | 未解决 |
| 工作流拓扑/运行深链、资产生命周期 | 定义快照等既有修改保留；路由仍无独立工作流运行深链，完整图编辑与生命周期尚未完成 | 部分完成 |

## 本轮优先问题

### P1：恢复创建绕过已有配额检查

证据：`cmd/core/backup_adapters.go:38-55` 的 CreateRestored 直接调用 repo.Create；`internal/dataservice/applier.go:25` 只保存并投影；常规创建路径的 quotaCheck 在 `internal/dataservice/handler.go:253`，由 `cmd/core/main.go` 给 DataService handler 注入。

恢复路径没有调用同一配额检查/递增服务。由代码调用关系可判断，创建恢复目标没有受常规创建额度闸门约束，也没有同源更新用量；这是源码确认的路径差异，本轮未在集群实际制造超额实例。

应复用共享的创建用例，统一权限、实际环境、配额预占与失败回退；不能只在前端隐藏恢复按钮。补超配额拒绝、创建失败回退、用量一致性测试。

### P1：恢复完整性与结果证据不足

证据：`internal/backup/k8s_executor.go:97-113`；`internal/backup/pacer.go:116-133`；`internal/backup/handler.go:260-303`。

当前恢复脚本等待 pg_isready 后直接 pg_restore，没有比对源快照已记录的 checksum；脚本输出 `sha256=restored`，它不是 SHA-256 校验和。Pacer 在 Job 成功时统一写 completed/VerifiedAt。源码注释已说明校验和记录不等于可恢复验证，但当前实现仍缺源文件完整性比对和目标数据读回证据。

新增测试使用 fake executor/目标/恢复器，验证受理与状态推进，不是实际执行 pg_dump→pg_restore→数据校验。应区分执行完成、文件完整性已验证、恢复数据验证通过；恢复前按不可变源元数据校验，恢复后执行明确的数据验证或保持“数据验证未执行”。

### P1：恢复新实例创建与作业登记缺少幂等及失败善后

证据：`internal/backup/handler.go:282-305`。

当前先创建目标实例，再登记恢复记录，之后解析目标并启动 Job。登记失败或目标解析失败时，目标实例可能已存在；响应中没有稳定的重试键/恢复操作定位，重复提交会再次创建目标。应先生成稳定 operation/idempotency 身份并返回可追踪结果，定义失败实例保留/清理和重试语义；不能用“请稍后重试”掩盖已创建资源。

### P1：第一轮三项关键行为仍保留

1. **配置并发**：`internal/configcenter/pg/store.go:791` 仍 SELECT count(*)；前端 `SharedConfigEditor.vue:331` 提交时读数量。需改预览 revision/hash 与原子校验，不能在预检失败时跳过保护。
2. **模型协议**：`Playground.vue:60` 仅区分 embedding，`:77` 仍加入所有模型，`:167` 发送 chat；目录动作分流不能保护直接 URL 与下拉入口。需发送前验证能力/端点。
3. **Agent 能力**：`Agents.vue:542` 仅禁用未 enabled 的工具；`internal/ai/agent/runtime.go:96-153` 仍直接模板文本、静默跳过依赖并只支持 MCP。需要真实可调用限制、运行 warnings 与采用资产证据。

### P2：新增反馈与契约还需收尾

- stopped/interrupted 消息虽然可见，但 `Playground.vue:145-147` 历史筛选仍只排除 error；不完整回答默认仍会进入后续上下文。SSE 解析异常仍被忽略，需明确错误帧与不完整输出策略。
- capabilities 目前只按引擎与接入模式推导，不能确认执行器/集群凭证/实际权限；没有执行器时真实操作仍返回 503。界面应准确说明这一层能力边界。
- 新批次、采集质量、恢复端点未在当前生成的 `api/types.gen.ts` 找到，不能认定契约生成同步完成。
- 真实时段计量、Key scope/期限/轮换、Secret 引用影响、工作流图与独立运行链接等仍需按原专项范围继续完成。

## 本轮重新执行的检查

| 项目 | 结果 |
|---|---|
| 前端 build | `pnpm --filter @paas/console-user-v2 build` 通过（包含 vue-tsc）；日志 `/tmp/paas-recheck-v2-build.log`，临时日志不是长期验收件 |
| 前端 UX | `pnpm --filter @paas/console-user-v2 test:ux` 14/14 通过；日志 `/tmp/paas-recheck-v2-ux.log`；不包含完整新增备份、配额、SSE 与并发漂移行为验证 |
| 后端相关测试 | 使用 `go test -count=1` 重跑 backup、dataservice、ai/agent、ai/workflow、ai/prompt、configcenter、observability、governance、environment 的子包及 cmd/core，全通过；不是沿用上轮缓存结果 |
| 本机预览 | 沙箱外只读请求 `127.0.0.1:5176/next/` 仍连接失败 HTTP=000，未做该地址的浏览器视觉/交互验收；不推断线上故障 |
| PG 迁移/真实恢复/集群/角色矩阵 | 本轮未执行，未标通过；测试命令未启用 PG integration tags |

本轮结论：**设计交付完整，基础修正继续落地，真实备份恢复从无执行器推进到了代码接入；全专项完成与真实环境验收仍不成立。**
