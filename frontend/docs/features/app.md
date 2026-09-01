# 应用与工作负载

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
