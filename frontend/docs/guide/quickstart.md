# 快速开始

本页带你 5 分钟走通：登录 → 建应用 → 部署工作负载 → 查看可观测。

## 1. 登录控制台

打开 `http://<平台地址>/console/`，使用租户账号登录。

开发环境预置三套演示 API Key（生产已关闭）：

| Key | 租户 | 角色 |
|-----|------|------|
| `sk-acme-admin` | Acme | 管理员 |
| `sk-globex-admin` | Globex | 管理员 |
| `sk-acme-dev` | Acme | 开发者 |

程序化调用直接带 Key：

```bash
curl -H "Authorization: Bearer sk-acme-admin" http://<平台地址>/api/applications
```

## 2. 创建应用

控制台「应用」→「新建应用」。应用是资源绑定的真源：工作负载、配置、数据服务、流水线都挂在应用下。

## 3. 部署工作负载

进入应用详情 → 「工作负载」→ 新建：

- 类型：Service（常驻服务）/ Job（一次性任务）/ CronJob（定时任务）
- 资源：生产环境必须配 CPU / 内存 requests+limits
- 镜像：来自构建产物的镜像仓库，或外部镜像

创建后控制面下发 CRD 期望状态，数据面 reconciler 自动拉起 Pod。

## 4. 查看可观测

「可观测」页：指标（Prometheus）、日志（Loki）、链路（Jaeger）三支柱按维度过滤下钻；告警规则持久化，firing / resolved 历史可回看。

## 5. 调一次模型（可选）

```bash
curl -N -H "Authorization: Bearer sk-acme-dev" -H "Content-Type: application/json" \
  -d '{"model":"glm-5.2","messages":[{"role":"user","content":"你好"}],"stream":true}' \
  http://<平台地址>/v1/chat/completions
```

OpenAI 兼容协议，多供应商通道自动故障转移，token 用量按应用归因计量。
