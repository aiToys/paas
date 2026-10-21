# Git 隐私与文件卫生检查（2026-10-21）

## 结论

**发现真实凭证暴露，当前不宜直接将全部改动提交。** 公开 GitHub 仓库的已推送提交包含六类与当前 Helm release 配置一致的敏感凭证。此结论表示凭证已经公开，不表示已发现第三方使用或入侵。

本次只检查和新增脱敏报告，没有提交、推送、删除原有文件、轮换集群凭证或改写 Git 历史。

## Git 状态与范围

- 分支：`console-v2`；HEAD：`c395e2eb00d8f997ada55994e0c459f07eea5e9c`。
- 检查开始时：208 个已跟踪文件有未暂存修改、116 个未跟踪文件、0 个暂存文件；本报告另新增 1 个未跟踪文件。
- 当前索引跟踪 1,405 个文件；HEAD 历史 402 个提交，所有本地可达引用共 404 个提交。
- 经 GitHub 元数据确认仓库为 PUBLIC；远端 `console-v2` 的提交与上述 HEAD 一致。
- 检查工作区文本、已提交文件、所有本地可达引用的历史增量，以及 9 张已跟踪 JPG 和 1 张未跟踪 PNG；对已提交的敏感值与当前 Helm 配置作内部比对，只保留位置及是否匹配的结果。
- 临时扫描结果位于根目录 `tmp/git-privacy-audit-2026-10-21/`，由 Git 忽略；临时读取的明文 Helm 配置和凭证比对文件在检查结束时删除。

## 已确认的敏感凭证

以下行号均对应本次检查的工作区文件；六类值均存在于 HEAD，且与当前集群 Helm 配置一致。本报告不记录明文或密钥片段。

| 优先级 | 内容 | 主要位置 | 影响及处理要点 |
| --- | --- | --- | --- |
| P0 | 数据加密主密钥 `security.secretMasterKey` | `deploy/charts/paas/values-paas-k8s.yaml:92` | 保护数据库中的敏感字段。需保留旧密钥完成存量密文迁移，再切换新密钥；直接替换会造成现有密文无法解密。 |
| P0 | JWT 签名密钥 `auth.jwtSecret` | 同文件 `:98`、`:101`，包括注释中的命令 | 签名安全失效风险；需协调 PaaS 与共用该密钥的 AgentRun，同时处理已有会话。 |
| P1 | AgentRun 调用令牌 `agentrun.token` | 同文件 `:104` | 应在服务端轮换后更新调用方配置。 |
| P1 | Gitea 密码 `devops.gitea.password` | 同文件 `:57` | 已分散复制到部署清单、研发说明和实施计划，应一起替换并清理。 |
| P1 | MinIO secret key `kb.minioSecretKey` | 同文件 `:86` | 对象存储凭证，应轮换并检查访问权限。 |
| P1 | Qdrant API key `kb.qdrantAPIKey` | 同文件 `:83` | 向量数据库访问凭证，应轮换并同步客户端配置。 |

需要同步处理的副本：

- Gitea：`AGENTS.md:958`、`deploy/devops/gitea.yaml:32`、`docs/superpowers/plans/2026-08-25-code-review.md:921`、`:922`、`docs/superpowers/plans/2026-09-05-paas-shop-full-demo.md:1931`。
- MinIO：`deploy/kb/minio.yaml:49`、`docs/deploy/knowledge-base.md:56`。
- Qdrant：`deploy/kb/qdrant.yaml:44`、`docs/deploy/knowledge-base.md:53`。

上述六类值在历史增量中共命中 25 个新增位置，涉及 8 个提交。历史最早命中：Gitea 为 `54d1c04`（2026-08-04），MinIO/Qdrant 为 `0b35ea8`（2026-08-07），主密钥为 `bb591dc`（2026-08-29），JWT/AgentRun 为 `720e3ac`（2026-10-15）。这些日期是本地可达历史中的记录日期，不能据此推断首次公网暴露时间。

**只删除当前文件或补一个脱敏提交，不能使已经公开的凭证恢复安全。** 应先使旧凭证失效；涉及历史清理时，需要另行安排协调，因为改写远端历史会影响其他开发者，且无法保证收回第三方已有副本。

## 其他隐私和默认凭证

- 48 个已跟踪文件和 5 个未跟踪文件包含内网 IP、内部域名或个人绝对目录路径的规则命中。这些不等同于密钥，但开源提交前应改成可配置值、示例域名或仓库相对路径。
- 个人目录路径示例：`docs/superpowers/plans/2026-08-02-zeus-dataplane-governance-plan.md:253`、`docs/superpowers/plans/2026-08-09-pipeline-deploy-release-lane.md:908`、`docs/superpowers/plans/2026-08-14-paas-shop-stage1-backend-mq-job.md:1159`、`docs/superpowers/plans/2026-10-17-console-module-improvements-implementation.md:93`、`docs/superpowers/specs/2026-08-02-zeus-dataplane-governance-design.md:122`。这些路径暴露开发者用户名及本地目录结构。
- 内部环境信息集中在 `AGENTS.md`、集群 values、部署说明、部分实施计划及部署验收报告。需区分通用部署示例与实际环境记录，后者公开前应脱敏。
- 文档中的演示 API Key 也出现在当前集群 `dataplane.token` 配置中。演示值本身不是随机私密凭证；一旦用于实际部署，仍应换成专用凭证。Chart 数据库默认密码及 `examples/paas-shop/setup.sh:32` 的固定示例 Action token 也应限定开发用途。
- 扫描命中的 `sk-` 长字符串位于 Secret Resolve、身份权限等测试夹具中；未据此认定存在新的真实供应商 Key 泄露。个人资料页的姓名、示例邮箱和手机号来自基座固定演示资料，未发现可确认的真实用户资料导出。
- 本次模式扫描未发现 PEM 私钥块或完整 JWT token；这不改变 JWT **签名密钥** 已暴露的结论，也不构成全仓安全保证。

## 无用文件与应保留文件

| 分类 | 文件或范围 | 判断 |
| --- | --- | --- |
| 清理候选 | `docs/reviews/2026-10-16-console-next/overview-dark.jpg` | 在当前已跟踪和未跟踪文本中均未找到引用；与同目录最终效果图并存。可作为旧截图清理候选，本次未删除。 |
| 应保留 | 其余 8 张已跟踪 JPG | 有评审或实施文档引用，属于正式证据资产，不能仅按截图后缀删除。 |
| 应保留 | `docs/reviews/2026-10-20-delivery-p0/target-preview-local.png` | 虽未跟踪，但被同目录 `implementation.md` 引用；应随正式验收材料统一审查。 |
| 应保留 | `docs/reviews/2026-10-19-admin-design/prototype.html` | README 明确引用的设计交付原型；不能当作普通临时 HTML 删除。 |
| 应保留 | `internal/apiroute/static/scalar-standalone.js`，约 3.7 MB | 由 `internal/apiroute/docs.go` 嵌入，用于同源、离线 API 文档。 |
| 暂保留 | `frontend/console-user-v2/src/api/types.gen.ts`，约 654 KB | OpenAPI 类型生成产物，存在明确生成命令；本次未找到直接类型导入，后续可结合契约消费策略决定是否继续入库。 |
| 应保留 | `frontend/console-user/` 与 `frontend/console-user-v2/` | 当前 Dockerfile 同时构建，分别供 `/console/` 与 `/next/` 使用；不能因新旧两套并存就删除旧端。 |
| 应保留 | 大多数未跟踪文件 | 包含功能模块、测试、数据库迁移和设计文档，未跟踪不等于无用。 |

上述 10 张当前截图未含 EXIF 标签；缩略总览及终端截图复核未见明显明文凭证。该人工复核不替代逐像素 OCR 或真实业务数据来源核验。

当前已跟踪文件清单中未发现 `node_modules`、`dist` 构建目录、日志、备份压缩包、私钥文件或根目录散落截图；`git ls-files -ci --exclude-standard` 为空，没有已跟踪但被当前忽略规则遮蔽的文件。

补充：`git diff --check` 检出既有未提交改动中的 `frontend/console-admin/src/lib/router/types.ts:6` 存在空格与 Tab 混用；本次未修改该业务文件。

## 建议处理顺序

1. 先安排凭证轮换，主密钥迁移和共用 JWT 密钥切换需专门验证；将当前发现当作已公开凭证处理。
2. 将真实凭证从 Git 管理的 values、部署清单、注释及文档副本中移除，改为已有 K8s Secret 引用或部署时注入；提交可用的无敏感值示例。
3. 对待提交文件按功能分组审查，脱敏内部环境信息；确认无引用旧截图的保留需求后再清理。
4. 提交临时文件规范相关的 `.gitignore`、`.dockerignore`、`AGENTS.md`、`CONTRIBUTING.md` 改动：当前规则已在本地生效，但仍未提交，远端 Agent 尚不能依赖这些改动。
5. 加入提交前及 CI 密钥扫描；如需清理远端历史，再明确范围并协调所有使用者。现有工作流未发现专门的密钥扫描步骤。

## 检查限制

本次使用自定义规则及人工复核，没有运行专业密钥扫描器。当前文本扫描跳过大体积 vendored 文件、生成类型及锁文件，历史增量扫描也排除了这些文件；未检查不可达 Git 对象、远端未获取引用、GitHub Issues/Actions 日志或外部副本。Helm 配置匹配证明值仍配置在 release 中，没有使用这些凭证逐一请求外部服务。未运行应用测试，因为本次未修改业务代码。
