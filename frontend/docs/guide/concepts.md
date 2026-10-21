# 核心概念

## 产品、服务与工作负载

新版控制台把 Application 展示为“产品”，经典版称为“应用”；两者共享同一份数据和 `/api/applications` API。

```text
租户
 ├─ 产品 / 应用 (Application)
 │   ├─ 服务 (ServiceEntity)：可部署单元，描述代码与运行配置
 │   │   └─ 工作负载 (Workload)：某环境、泳道中的实际运行声明
 │   ├─ 交付：仓库、流水线、变更、批次、发布记录
 │   ├─ 配置：应用 × 环境的静态注入
 │   └─ 数据服务绑定：连接信息按实例所属环境注入
 └─ 环境 (Environment)：产品可绑定多个环境
     └─ 泳道 (Lane)：环境内的流量隔离单元
```

已有 API/脚本也能直接创建工作负载，没有 ServiceEntity 的工作负载可从工作负载入口管理。流量治理中的 Service 是注册发现和路由对象，需与运行服务区分。

- **成员权限**归属应用：owner / maintainer / developer / viewer；受限应用启用成员校验。
- **基础配额与账单**主要按租户管理；应用级 Key 另提供模型用量的应用归因。
- **环境**区分 `test` / `prod`；生产写需 `prod:write`，晋升受发布阶序约束。
- 顶栏环境是浏览范围，写操作以资源实际环境和流水线目标为准。

## 泳道

泳道处在一个环境内，不是复制整套环境：只部署变更服务，未覆盖服务可回退基线。需要业务 SDK/中间件接入染色、透传和服务发现，不能只添加请求头就认定全链路隔离成立。

standard 泳道按 TTL 回收，permanent 常驻；canary 阶段建立临时验证泳道，由人工确认发布或终止。共享数据服务和消息链路仍需单独设计隔离。见[泳道](../features/lane)。

## 流水线与发布

模板定义阶段，流水线绑定模板及参数；触发时解析并固化执行参数。当前支持 build / deploy / test / approve / release / promote / baseline / canary / agent 九类阶段，`agent` 需额外的 AgentRun 执行域。

- **deploy** 改变目标环境部署，产生可回滚记录。
- **release** 创建版本里程碑，不等于部署。
- **promote** 按环境阶序推进基线版本。
- **agent** 生成代码分支，可接后续构建；与平台内 Agent/Workflow 的业务运行区分。

见[交付指南](../features/devops)与[智能研发](../features/agent-delivery)。

## 模型、通道与供应商

Model 是对外模型目录，包含多个 Channel；Channel 描述具体上游模型和端点，可引用 Vendor 预设。Provider 是运行时协议适配器。它们是平台级目录，应用和 Key 是租户资源。见[MaaS](../features/maas)。

## 身份与交付形态

控制台用 cookie 会话，程序用 API Key；都绑定租户、用户和角色。跨租户资源访问通常返回 404，生产写操作另受权限限制。

平台支持 Helm 安装和 airsync 离线包。离线交付仍要准备模型供应商、观测、知识库等实际使用能力的依赖，见[部署方式](../deploy/)。
