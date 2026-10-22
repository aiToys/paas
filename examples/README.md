# paas-examples（平台示例）

这个目录是一个**独立的 Go module**（`github.com/aitoys/paas-examples`），与平台主仓（`github.com/aitoys/paas`）的 Go 依赖**完全解耦**。

## 为什么独立

示例服务是**平台的用户/消费者**，演示「如何被平台纳管」，不属于 Platform Core。把 demo 业务代码塞进平台 `cmd/` 会模糊平台与用户的边界。判断标准见主仓 CLAUDE.md：

> 业务领域逻辑绝不进 Platform Core；判断标准："MaaS / 治理 / DevOps 都会用吗？"

订单查询、退款、流量生成是特定 demo 的业务逻辑，不是平台能力，因此隔离在此。示例依赖仅由自身 module 管理（如 pgx、OTel），**不引用任何 paas 内部包**。

## 包含

| 服务 | 作用 | 部署形态 |
|------|------|---------|
| `mcp-server/` | MCP（Model Context Protocol）工具服务端，提供 `query_order`/`refund_order` 供 Agent FunctionCalling 调用 | Deployment + Service |
| `traffic-gen/` | 流量生成器：常驻模式循环调微服务链 + AI Agent（带会话记忆）；`once` 模式单次调用（CronJob） | Deployment + CronJob |

## 构建

示例是独立 module，**必须在 `examples/` 目录下编译**（不是主仓根）：

```bash
mkdir -p tmp/examples-build
(cd examples && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ../tmp/examples-build/mcp-server ./mcp-server)
(cd examples && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o ../tmp/examples-build/traffic-gen ./traffic-gen)
docker build -f examples/Dockerfile -t <nodeIP>:30050/paas/paas-examples:v1 tmp/examples-build
docker push <nodeIP>:30050/paas/paas-examples:v1
```

## 部署（经平台 API 纳管）

示例不直接 `kubectl apply`，而是通过平台工作负载 API 创建，由平台 reconciler 落地 K8s——这正演示了「平台如何纳管用户业务」：

```bash
./examples/scripts/create-resources.sh     # 建 AI/治理/可观测/安全等平台资源（Prompt/Tool/Agent/告警/密钥）
./examples/scripts/create-workloads.sh     # 建示例工作负载（mcp-server + traffic-gen + CronJob）+ 绑定数据服务
./examples/scripts/verify.sh               # 验证全资源可用状态
```

## PaasShop Skill 演示

`paas-shop/skills/` 提供商品导购与订单查询两个标准 `SKILL.md`，不是为每个基础组件分别创建技能。初始化两个 setup 入口均调用同一安装器；也可在基础设施准备后单独运行：

```bash
# PAAS_BASE、PAAS_TOKEN、PAAS_SHOP_MCP_URL 由操作者提供，不在技能包中保存凭证。
python3 examples/paas-shop/install_ai.py
```

重复运行按名称复用 Skill、工具与 Agent，保留 Skill 指令和 Agent 的模型、Prompt、知识库；演示 Agent 的工具绑定由安装器管理为三个只读方法。输出 `agentId` 后，可在控制台查看技能绑定并到 Playground 输入 `paas-shop/skills/scenarios.json` 中的用例。订单用例需替换真实测试订单号，安装成功不等于真实推理或业务验收通过。安装器不会发起退款或模型调用。

本地回归：`python3 -B -m unittest discover -s examples/paas-shop -p test_install_ai.py -v`。临时文件或缓存请显式放到仓库根目录 `tmp/`。

## 数据

- `mcp-server` 订单数据为内存演示（Pod 重启丢失）；生产应接真实订单/退款系统。
- `traffic-gen` 会话记忆为内存演示（Pod 重启丢失）；生产用 Redis 持久化。
