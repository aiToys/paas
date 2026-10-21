# 管理后台修复：K8s部署记录

时间：2026-10-21 08:40（Asia/Shanghai）。用户明确要求部署到K8s后执行。

## 部署结果

- 目标：`kubernetes-admin@kubernetes`，命名空间`paas`，Helm release `paas`。
- Helm revision：381 → **382**，状态`deployed`；`paas-core` rollout成功。
- 镜像：`192.168.41.122:30050/paas/paas-core:admin-fix-20261021-083541`。
- 仓库与Pod一致的镜像摘要：`sha256:ea68e09a6dc44919b9687f76c866a4d47dd843a3bc61948ae6bf37f403a39408`。
- Pod：`paas-core-794b74c8f8-jxvj4`，Ready，重启次数0；`/livez`返回`{"status":"ok"}`。
- 后台地址：[PaaS管理后台](http://paas.k8s.dd/admin/)。

## 来源与配置

本次按`console-v2`当前未提交工作树构建完整Core镜像（HEAD `c395e2eb00d8`），包含前面的R01–R05修复和TagsView的Close图标导入。保留既有工作树改动，没有commit或push。

构建前记录1233份源文件哈希，构建期间检查未发生变化。采用仓库Dockerfile，四套前端与文档先构建，再嵌入linux/amd64 Core；使用独立镜像标签。

部署前按当前Helm值渲染比对：11份资源清单中只有`Deployment/paas-core`的容器镜像变化。升级使用`--reuse-values`；升级后比较全部计算值，仅`image.tag`变化。未执行部署脚本中额外的基础设施apply、CRD更新或镜像清理。

数据库部署前后均为迁移59、dirty=false；当前源码没有新增迁移。启动日志未发现panic、fatal error或迁移失败。

## 验证

| 检查 | 结果 |
|---|---|
| TagsView选中及未选中标签的关闭回归 | 2项通过；真实SVG渲染和关闭后的路由行为均有断言。 |
| 全量`go test ./...` | 通过。首次扫描到tmp中的源码备份失败，随后以`tmp/go.mod`隔离临时目录后正式源码全量回归通过；没有把首次失败忽略为成功。 |
| 容器构建 | 用户端、用户端v2、管理后台、官网与文档构建通过；Core静态交叉编译通过。 |
| Helm与Pod | revision 382、rollout成功、Ready、镜像摘要与仓库一致。 |
| 制品一致性 | 从相同构建的frontend阶段导出后台dist，线上HTML及全部196份JS/CSS逐一进行字节/哈希比对通过。 |
| TagsView编译结果 | `index-BvWvrhCl.js`包含关闭图标使用，未遗留动态解析未注册Close的调用。 |
| 配置与数据库 | Helm值仅image.tag变化；迁移59 clean。 |
| 浏览器点击验收 | 工具两次绑定页面超时；可见浏览器标签到达后台登录地址，但没有完成登录后的独立点击验收。不将HTTP或制品比对称为UI点击验收。 |

日志、受限权限的配置快照、构建产物和验证JSON均在根目录`tmp/admin-deploy-2026-10-21/`，Git/Docker忽略生效。未执行真实供应商调用、业务结算、备份恢复或其他业务写操作。

本记录确认部署和上述检查；原完整管理后台方案的延期能力仍见实施交接清单。
