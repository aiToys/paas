# paas-examples（平台示例）

这个目录是一个**独立的 Go module**（`github.com/aitoys/paas-examples`），与平台主仓（`github.com/aitoys/paas`）的 Go 依赖**完全解耦**。

## 为什么独立

示例服务是**平台的用户/消费者**，演示「如何被平台纳管」，不属于 Platform Core。把 demo 业务代码塞进平台 `cmd/` 会模糊平台与用户的边界。判断标准见主仓 CLAUDE.md：

> 业务领域逻辑绝不进 Platform Core；判断标准："MaaS / 治理 / DevOps 都会用吗？"

订单查询、退款、流量生成是特定 demo 的业务逻辑，不是平台能力，因此隔离在此。示例只用 Go 标准库，**不引用任何 paas 内部包**。

## 包含

| 服务 | 作用 | 部署形态 |
|------|------|---------|
| `mcp-server/` | MCP（Model Context Protocol）工具服务端，提供 `query_order`/`refund_order` 供 Agent FunctionCalling 调用 | Deployment + Service |
| `traffic-gen/` | 流量生成器：常驻模式循环调微服务链 + AI Agent（带会话记忆）；`once` 模式单次调用（CronJob） | Deployment + CronJob |

## 构建

示例是独立 module，**必须在 `examples/` 目录下编译**（不是主仓根）：

```bash
cd examples
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/mcp-server ./mcp-server
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/traffic-gen ./traffic-gen
docker build -f examples/Dockerfile -t <nodeIP>:30050/paas/paas-examples:v1 /tmp
docker push <nodeIP>:30050/paas/paas-examples:v1
```

## 部署（经平台 API 纳管）

示例不直接 `kubectl apply`，而是通过平台工作负载 API 创建，由平台 reconciler 落地 K8s——这正演示了「平台如何纳管用户业务」：

```bash
./examples/scripts/create-resources.sh     # 建 AI/治理/可观测/安全等平台资源（Prompt/Tool/Agent/告警/密钥）
./examples/scripts/create-workloads.sh     # 建示例工作负载（mcp-server + traffic-gen + CronJob）+ 绑定数据服务
./examples/scripts/verify.sh               # 验证全资源可用状态
```

## 数据

- `mcp-server` 订单数据为内存演示（Pod 重启丢失）；生产应接真实订单/退款系统。
- `traffic-gen` 会话记忆为内存演示（Pod 重启丢失）；生产用 Redis 持久化。
