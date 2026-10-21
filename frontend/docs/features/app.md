# 产品、应用与工作负载

新版“产品”与经典版“应用”均对应 Application。服务是可部署单元，工作负载描述服务在某环境和泳道中的运行。见[核心概念](../guide/concepts)。

## 应用

应用是资源组织的真源：

- **绑定**（Bindings）：数据服务、流水线挂载点，绑定即注入连接串
- **资源**（Resources）：从绑定派生的展示视图
- **成员权限**：owner / maintainer / developer / viewer；开启「受限应用」后非成员 fail-closed
- **级联删除**：删应用自动回收工作负载 / 配置 / 配额

## 工作负载

三种类型：

| 类型 | 语义 | 说明 |
|------|------|------|
| Service | 常驻服务 | 多服务字段；Deployment 名 = 工作负载 ID |
| Job | 一次性任务 | 跑完即止 |
| CronJob | 定时任务 | schedule 表达式 |

**资源规格**：CPU / 内存的 requests + limits；**生产环境禁止 BestEffort**（不配 requests 拒绝创建）。

**实例与日志**：Pod 级实例列表、实时日志、上次终止日志（previous）、越权校验防跨租户。

## 命名约定

```
Deployment = 工作负载 ID
Pod        = <id>-<rsHash>-<podHash>
Service 名 = 工作负载名（治理发现靠此对齐）
```

## 实例运维

新版「工作负载」或产品服务运行视图可进入运行详情，查看实例、日志与事件，以及按权限执行重启、重建、终端。具体前提和完成判断见[服务运行与排障](./runtime)。
