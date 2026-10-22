---
name: onepaas-app-delivery
description: 帮助外部 Agent 检查 OnePaaS 应用的交付配置、预演并跟踪已授权的流水线，不将一般查询视为发布授权。
license: Apache-2.0
metadata:
  version: 1.0.0
  runtime: external-agent
---

# OnePaaS 应用交付

在用户指定的实例和凭证范围内操作。先读取 `/openapi.json`，使用实例当前定义的参数和状态，不依据此文件猜测未核对的接口。凭证从环境或客户端凭证管理器读取，不记录在交付资产中，也不跟随跨主机重定向转发。

确定应用 ID、流水线 ID、代码分支或版本以及实际部署环境。查询 `/api/applications/{id}/pipelines`，核对仓库、构建器、镜像和目标配置。需要预演时使用当前契约中的 `POST /api/applications/{id}/pipelines/{pid}/preview`；预演是计算计划，不代表流水线已经运行。

仅在用户授权触发相应任务后调用运行接口。读操作、预演或一般“检查问题”的请求不等于部署、生产发布、回滚或跳过审批授权。保留服务端审批和权限约束；生产被拒时解释阻塞，不换管理员凭证重试。

记录实际返回的运行 ID，通过 `/api/pipelineruns/{id}` 跟踪结果。超时或连接失败后先查是否已创建运行，不盲目重复触发有副作用的任务。构建、镜像产出、部署就绪、业务验收分别汇报，只给有实际证据的阶段结论。
