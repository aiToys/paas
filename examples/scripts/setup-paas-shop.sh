#!/usr/bin/env bash
# 为 paas-shop 示例应用一次性创建全模块平台资源（治理/告警/安全/计费/备份/MQ/KB/Agent/Tool）。
# 幂等：已存在的资源按 name 查重跳过。依赖：core 已部署 + paas-shop 应用与工作负载已建（deploy-paas-shop.sh）。
#
# 每段对应一项能力补齐：
#   §1 服务治理（Service+Route+Breaker）   [能力 2]
#   §2 告警规则                            [能力: 可观测]
#   §3 安全密钥（凭证）                    [能力 8]
#   §4 配额                                [能力 7]
#   §5 数据服务备份                        [能力 9]
#   §6 消息队列 topic+消费组              [能力 10]
#   §7 知识库 + 文档上传                   [能力 3]
#   §8 Prompt + Tool + Agent              [能力 4]
set -uo pipefail
H="Authorization: Bearer ${PAAS_TOKEN:?请设置 PAAS_TOKEN（API Key，dev 默认 sk-acme-admin）}"
B="${PAAS_BASE:?请设置 PAAS_BASE（core 地址，dev 默认 http://paas.k8s.dd）}"
APP="paas-shop"

# 取 test 环境 envId（治理/告警等逻辑资源归属测试环境）。
ENV_TEST=$(curl -s -H "$H" "$B/api/environments" | python3 -c "import sys,json;d=json.load(sys.stdin);es=d.get('data',d if isinstance(d,list) else []);print(next((e['id'] for e in es if e.get('type')=='test'),es[0]['id'] if es else ''))" 2>/dev/null)
echo "ENV_TEST=$ENV_TEST  APP=$APP"

echo "=== §1 服务治理：注册 paas-shop 微服务（Instance 从 K8s Endpoints 经 readiness probe 真实同步）==="
for svc in \
  "{\"name\":\"paas-shop-product\",\"appId\":\"$APP\",\"envId\":\"$ENV_TEST\",\"protocol\":\"http\",\"port\":8081,\"desc\":\"商品服务\"}" \
  "{\"name\":\"paas-shop-recommend\",\"appId\":\"$APP\",\"envId\":\"$ENV_TEST\",\"protocol\":\"http\",\"port\":8082,\"desc\":\"推荐服务\"}" \
  "{\"name\":\"paas-shop-chatbot\",\"appId\":\"$APP\",\"envId\":\"$ENV_TEST\",\"protocol\":\"http\",\"port\":8083,\"desc\":\"AI 客服\"}" \
  "{\"name\":\"paas-shop-bff\",\"appId\":\"$APP\",\"envId\":\"$ENV_TEST\",\"protocol\":\"http\",\"port\":8080,\"desc\":\"聚合层\"}"; do
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/services" -d "$svc" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  service:',d.get('id'),d.get('name'))" 2>/dev/null
done

echo "=== §1.2 API 网关路由（外部经治理网关访问 paas-shop 各微服务）==="
for r in \
  '{"name":"shop-products-route","path":"/api/shop/products/*","serviceId":"paas-shop-product","methods":["GET","POST"],"stripPath":true,"enabled":true}' \
  '{"name":"shop-recommend-route","path":"/api/shop/recommend/*","serviceId":"paas-shop-recommend","methods":["GET"],"stripPath":true,"enabled":true}' \
  '{"name":"shop-chat-route","path":"/api/shop/chat/*","serviceId":"paas-shop-chatbot","methods":["POST"],"stripPath":true,"enabled":true}'; do
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/routes" -d "$r" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  route:',d.get('id'),d.get('path'))" 2>/dev/null
done

echo "=== §1.3 熔断器（error_rate 策略，样本不足不熔断）==="
for bkr in \
  '{"name":"shop-product-breaker","serviceId":"paas-shop-product","strategy":"error_rate","threshold":50,"minRequests":10,"windowSecs":60,"enabled":true}' \
  '{"name":"shop-recommend-breaker","serviceId":"paas-shop-recommend","strategy":"error_rate","threshold":50,"minRequests":10,"windowSecs":60,"enabled":true}' \
  '{"name":"shop-chatbot-breaker","serviceId":"paas-shop-chatbot","strategy":"slow_call","threshold":80,"minRequests":5,"windowSecs":60,"enabled":true}'; do
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/breakers" -d "$bkr" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  breaker:',d.get('id'),d.get('name'))" 2>/dev/null
done

echo "=== §1 治理资源创建完成 ==="

echo "=== §7 知识库（平台 KB retrieve，真向量检索 + score 排序）==="
# 7.1 查 vector（shop-kb）+ storage（shop-object）dataservice id
VECTOR_DS=$(curl -s -H "$H" "$B/api/dataservices?kind=vector" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);print(next((x['id'] for x in d if 'shop' in x.get('name','').lower()),d[0]['id'] if d else ''))" 2>/dev/null)
# objectStoreRef 占位：retriever 用 core env PAAS_KB_MINIO_ENDPOINT 共享实例（不查 dataservice storage）。
STORAGE_DS="shared-minio"
echo "  vector=$VECTOR_DS  storage=$STORAGE_DS"
if [ -n "$VECTOR_DS" ]; then
  # 7.2 创建 KB（embedding 走 airouter text-embedding-v4 1024 维，topK=3）
  KB_ID=$(curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/knowledgebases" -d \
    "{\"name\":\"shop-faq\",\"vectorStoreRef\":\"$VECTOR_DS\",\"objectStoreRef\":\"$STORAGE_DS\",\"embeddingModel\":\"text-embedding-v4\",\"embeddingDim\":1024,\"retrieverConfig\":{\"topK\":3}}" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print(d.get('id',''))" 2>/dev/null)
  echo "  kb: $KB_ID"
  # 7.3 生成 FAQ markdown 文档（python3 写文件，避免 heredoc 限制）
  python3 -c "
content='''# PaasShop 常见问题

## 退货政策
支持 7 天无理由退货，商品需保持完好。

## 发货时间
下单后 24 小时内发货，顺丰包邮。

## 保修政策
整机保修 1 年，外设保修 6 个月。

## 支付方式
支持微信、支付宝、银行卡，支持花呗分期。

## 发票
支持电子发票和纸质发票，下单时可备注。
'''
open('/tmp/shop-faq.md','w').write(content)
"
  # 7.4 上传文档（multipart，平台异步解析 + embedding + 入库）
  DOC_ID=$(curl -s -X POST -H "$H" -F "file=@/tmp/shop-faq.md" -F "name=shop-faq.md" "$B/api/knowledgebases/$KB_ID/documents" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print(d.get('id',''))" 2>/dev/null)
  echo "  doc: $DOC_ID"
  # 7.5 轮询文档状态到 indexed（embedding 异步）
  for i in $(seq 1 30); do
    STATUS=$(curl -s -H "$H" "$B/api/knowledgebases/$KB_ID/documents/$DOC_ID" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print(d.get('status',''))" 2>/dev/null)
    echo "  doc status: $STATUS"
    [ "$STATUS" = "indexed" ] && break
    [ "$STATUS" = "failed" ] && break
    sleep 2
  done
  echo "PAAS_KB_ID=$KB_ID"
else
  echo "  跳过：需先创建 vector（shop-kb）+ storage（shop-object）数据服务（见 deploy-paas-shop.sh）"
fi

echo "=== §7 知识库资源创建完成 ==="

echo "=== §8 AI Agent + MCP 工具（平台 Agent runtime 多轮 function calling）==="
# 8.1 Prompt：无占位符人设（平台不渲染模板变量，问题描述由 user 消息携带）；已存在则 PUT 更新，否则 POST 创建
PROMPT_ID=$(curl -s -H "$H" "$B/api/prompts" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);items=d if isinstance(d,list) else d.get('items',[]);print(next((p['id'] for p in items if p.get('name')=='shop-cs'),''))" 2>/dev/null)
PROMPT_BODY='{"name":"shop-cs","template":"你是 PaasShop 智能客服，友好专业。规则：\n1. 回答商品、订单、售后相关问题\n2. 找商品/推荐/比价用 search_products；已知商品 ID 查详情用 query_product；查订单用 query_order；退款用 refund_order\n3. 不确定时诚实告知，不编造\n4. 回答简洁","variables":[]}'
if [ -n "$PROMPT_ID" ]; then
  curl -s -X PUT -H "$H" -H "Content-Type: application/json" "$B/api/prompts/$PROMPT_ID" -d "$PROMPT_BODY" >/dev/null && echo "  prompt updated: $PROMPT_ID"
else
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/prompts" -d "$PROMPT_BODY" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  prompt:',d.get('id'))" 2>/dev/null
fi

# 8.2 4 个 Tool 实体（已验证 runtime.go:141-146 按 mt.Name == t.Name 匹配——
#     Tool 实体 name 必须与 MCP server 工具名逐字一致，不能加 shop- 前缀）
MCP_URL="http://paas-shop-mcp.paas-t-acme.svc.cluster.local"
TOOL_IDS=""
for t in \
  "query_product:查商品详情（按商品 ID 返回名称/价格/库存）" \
  "search_products:搜索商品（按关键字/分类，用于找商品/推荐/比价）" \
  "query_order:查询订单状态（按订单号返回详情）" \
  "refund_order:对订单发起退款" ; do
  TNAME="${t%%:*}"; TDESC="${t#*:}"
  TID=$(curl -s -H "$H" "$B/api/tools" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);items=d if isinstance(d,list) else d.get('items',[]);print(next((x['id'] for x in items if x.get('name')=='$TNAME'),''))" 2>/dev/null)
  BODY="{\"name\":\"$TNAME\",\"description\":\"$TDESC\",\"type\":\"mcp\",\"config\":{\"serverURL\":\"$MCP_URL\",\"apiKey\":\"\"},\"enabled\":true}"
  if [ -n "$TID" ]; then
    curl -s -X PUT -H "$H" -H "Content-Type: application/json" "$B/api/tools/$TID" -d "$BODY" >/dev/null
  else
    TID=$(curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/tools" -d "$BODY" | python3 -c "import sys,json;print(json.load(sys.stdin).get('data',{}).get('id',''))" 2>/dev/null)
  fi
  echo "  tool: $TNAME -> $TID"
  TOOL_IDS="$TOOL_IDS \"$TID\""
done
# 旧 shop-tools 单实体删除（实体名 shop-tools 匹配不到 MCP 工具，已废）
OLD_TOOL=$(curl -s -H "$H" "$B/api/tools" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);items=d if isinstance(d,list) else d.get('items',[]);print(next((x['id'] for x in items if x.get('name')=='shop-tools'),''))" 2>/dev/null)
[ -n "$OLD_TOOL" ] && curl -s -X DELETE -H "$H" "$B/api/tools/$OLD_TOOL" >/dev/null && echo "  deleted old tool: shop-tools"

# 8.3 shop-agent：已存在则 PUT 更新 tools，否则 POST 创建（虚拟模型 model=agent:{id} 经平台 runtime 推理）
AGENT_ID=$(curl -s -H "$H" "$B/api/agents" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);items=d if isinstance(d,list) else d.get('items',[]);print(next((a['id'] for a in items if a.get('name')=='shop-agent'),''))" 2>/dev/null)
KB_ARG="[]"; [ -n "$KB_ID" ] && KB_ARG="[\"$KB_ID\"]"
AGENT_BODY="{\"name\":\"shop-agent\",\"description\":\"PaasShop 商品客服 Agent（RAG+MCP 工具）\",\"model\":\"glm-5.2\",\"promptRef\":\"shop-cs\",\"tools\":[$TOOL_IDS],\"knowledgeBases\":$KB_ARG,\"maxSteps\":5,\"enabled\":true}"
if [ -n "$AGENT_ID" ]; then
  curl -s -X PUT -H "$H" -H "Content-Type: application/json" "$B/api/agents/$AGENT_ID" -d "$AGENT_BODY" >/dev/null && echo "  agent updated: $AGENT_ID"
else
  AGENT_ID=$(curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/agents" -d "$AGENT_BODY" | python3 -c "import sys,json;print(json.load(sys.stdin).get('data',{}).get('id',''))" 2>/dev/null)
  echo "  agent: $AGENT_ID"
fi
echo "PAAS_AGENT_ID=$AGENT_ID"
echo "=== §8 Agent 资源创建完成 ==="

echo "=== §8.4 chatbot env 注入（PAAS_AGENT_MODEL）==="
curl -s -o /dev/null -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/configs" -d \
  "{\"key\":\"PAAS_AGENT_MODEL\",\"value\":\"agent:$AGENT_ID\",\"type\":\"env\",\"envId\":\"$ENV_TEST\"}" && echo "  cfg: PAAS_AGENT_MODEL=agent:$AGENT_ID"

echo "=== §4 消息队列（shop-mq NATS 数据服务 + topic + consumer group）==="
# 4.1 创建 shop-mq 数据服务（NATS，reconciler 建 StatefulSet + Service）
MQ_ID=$(curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/dataservices" -d \
  "{\"kind\":\"mq\",\"name\":\"shop-mq\",\"engineId\":\"nats-managed\",\"spec\":{\"partitions\":\"3\"},\"storageGb\":1,\"envId\":\"$ENV_TEST\"}" \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print(d.get('id',''))" 2>/dev/null)
echo "  mq: $MQ_ID"
# 4.2 等 shop-mq running（reconciler 建 NATS STS，readiness probe 驱动 status）
for i in $(seq 1 30); do
  STATUS=$(curl -s -H "$H" "$B/api/dataservices/$MQ_ID" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print(d.get('status',''))" 2>/dev/null)
  echo "  mq status: $STATUS"
  [ "$STATUS" = "running" ] && break
  sleep 3
done
# 4.3 创建 topic（shop-events，product 发订单/商品事件到此，recommend 消费刷新缓存）
TOPIC_ID=$(curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/mq-topics" -d \
  "{\"mqId\":\"$MQ_ID\",\"name\":\"shop-events\",\"partitions\":3,\"retention\":\"7d\"}" \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print(d.get('id',''))" 2>/dev/null)
echo "  topic: $TOPIC_ID"
# 4.4 创建 consumer group（recommend-consumer，recommend 服务订阅消费）
curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/consumer-groups" -d \
  "{\"topicId\":\"$TOPIC_ID\",\"name\":\"recommend-consumer\",\"mode\":\"clustering\"}" \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  group:',d.get('id'),d.get('name'))" 2>/dev/null
echo "PAAS_MQ_ID=$MQ_ID  TOPIC_ID=$TOPIC_ID"
echo "=== §4 消息队列创建完成 ==="

echo "=== §8.5 数据服务绑定（shop-db/cache/mq 注入 env 到 paas-shop workload）==="
# 注：shop-mq 数据服务已在前段 §4 创建，绑定可直接解析
# binding_injector 按 type 注入：db->DATABASE_URL, cache->REDIS_URL, mq->NATS_URL
# 应用级绑定，注入到应用所有 workload（product/recommend/statsworker 各取所需 env）
for ds in \
  '{"type":"db","name":"shop-db"}' \
  '{"type":"cache","name":"shop-cache"}' \
  '{"type":"mq","name":"shop-mq"}'; do
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/bindings" -d "$ds" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});bs=d.get('bindings',[]);print('  binding:',bs[-1].get('type'),bs[-1].get('name') if bs else 'exists')" 2>/dev/null
done
echo "=== §8.5 数据服务绑定完成 ==="

echo "=== §9 DevOps CI/CD（CodeRepo -> BuildRun -> Image，平台构建链路）==="
# 9.1 创建 external CodeRepo（指向 aitoys/paas GitHub，含 examples/paas-shop 代码）
REPO_ID=$(curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/repositories" -d \
  '{"gitUrl":"https://github.com/aitoys/paas.git","branch":"main","dockerfile":"examples/paas-shop/Dockerfile.backend","buildContext":".","source":"external"}' \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print(d.get('id',''))" 2>/dev/null)
echo "  repo: $REPO_ID"
# 9.2 多服务构建：paas-shop 是 monorepo（bff/product/recommend/chatbot 4 个 Go 后端），
#     共用 Dockerfile.backend + buildContext=examples/，靠 buildArgs.SERVICE 区分构建目标。
#     每个 SERVICE 一次 BuildRun → 各自独立 tag（buildArgs 哈希区分，见 builder.buildTag）→ 独立 digest 镜像。
#     frontend 是 nginx SPA（独立 Dockerfile），单独构建。
for SVC in product recommend chatbot bff statsworker mcp; do
  echo "  build service: $SVC"
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/buildruns" -d \
    "{\"repoId\":\"$REPO_ID\",\"branch\":\"main\",\"trigger\":\"manual\",\"buildArgs\":{\"SERVICE\":\"$SVC\"}}" \
    | python3 -c "import sys,json;d=json.load(sys.stdin);print('   ',d.get('status',d))" 2>/dev/null
done
# 9.2b frontend 构建（独立 Dockerfile.frontend，无 SERVICE buildArgs）
curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/repositories" -d \
  '{"gitUrl":"https://github.com/aitoys/paas.git","branch":"main","dockerfile":"examples/paas-shop/frontend/Dockerfile","buildContext":".","source":"external"}' \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  repo(frontend):',d.get('id',''))" 2>/dev/null >/tmp/shop-fe-repo
FE_REPO_ID=$(cat /tmp/shop-fe-repo 2>/dev/null | awk '{print $2}')
if [ -n "$FE_REPO_ID" ]; then
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/buildruns" -d \
    "{\"repoId\":\"$FE_REPO_ID\",\"branch\":\"main\",\"trigger\":\"manual\"}" \
    | python3 -c "import sys,json;d=json.load(sys.stdin);print('  build frontend:',d.get('status',d))" 2>/dev/null
fi
# 9.3 轮询所有 BuildRun 到终态（多服务并行构建，mock 快速；k8s 真实构建需更长 timeout）
for i in $(seq 1 30); do
  PENDING=$(curl -s -H "$H" "$B/api/applications/$APP/buildruns" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);p=[b for b in d if b.get('status') in ('pending','running')];print(len(p))" 2>/dev/null)
  echo "  待完成构建数: $PENDING"
  [ "$PENDING" = "0" ] && break
  sleep 3
done
# 9.4 查镜像列表（构建产物，digest 不可变真源；多服务各自独立 tag）
curl -s -H "$H" "$B/api/applications/$APP/images" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);print('\n'.join('  image: '+x['id']+'  '+x.get('tag','')+'  digest='+x.get('digest','')[:20] for x in d) if d else '  (暂无镜像)')" 2>/dev/null
echo "=== §9 DevOps 链路创建完成 ==="

echo "=== §10 持续流量生成（traffic-gen 针对 paas-shop bff + agent:shop-agent）==="
REGISTRY="${REGISTRY:?设置 REGISTRY 为你的集群 registry 地址，如 <nodeIP>:30050}"
# 10.1 创建 traffic-gen appconfig（注入 paas-shop bff URL + agent 虚拟模型，env 归属 paas-shop test 环境）
for kv in "SHOP_BFF_URL:http://paas-shop-bff:8080" "CORE_URL:http://paas-core.paas.svc.cluster.local" "API_KEY:sk-acme-dev" "AGENT_MODEL:agent:$AGENT_ID" "MICRO_INTERVAL:300" "AGENT_INTERVAL:3600"; do
  K="${kv%%:*}"; V="${kv#*:}"
  TYPE="env"; [ "$K" = "API_KEY" ] && TYPE="secret"
  curl -s -o /dev/null -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/configs" -d \
    "{\"key\":\"$K\",\"value\":\"$V\",\"type\":\"$TYPE\",\"envId\":\"$ENV_TEST\"}" && echo "  cfg: $K"
done

# paas-shop 业务 appconfig（与 deploy-paas-shop.sh §4 一致，幂等）
for kv in \
  "PRODUCT_PAGE_SIZE:20:env" \
  "RECOMMEND_COUNT:3:env" \
  "RECOMMEND_CACHE_TTL:300:env" \
  "PAAS_APPCONFIG_URL:http://paas-core.paas.svc.cluster.local:env" \
  "PAAS_APP_ID:paas-shop:env" \
  "PAAS_ENV_ID:$ENV_TEST:env" \
  "PAAS_API_KEY:sk-acme-dev:secret"; do
  K="${kv%%:*}"; REST="${kv#*:}"; V="${REST%:*}"; TYPE="${REST##*:}"
  curl -s -o /dev/null -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/configs" -d \
    "{\"key\":\"$K\",\"value\":\"$V\",\"type\":\"$TYPE\",\"envId\":\"$ENV_TEST\"}" && echo "  cfg: $K"
done
# 10.2 创建 traffic-gen Deployment（常驻，持续调 paas-shop bff /api/products+/api/recommend + agent:shop-agent）
curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/workloads" -d \
  "{\"envId\":\"$ENV_TEST\",\"type\":\"service\",\"name\":\"paas-shop-traffic-gen\",\"image\":\"$REGISTRY/paas-shop/traffic-gen:v2\",\"replicas\":1}" \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  deploy:',d.get('id'),d.get('name'))" 2>/dev/null
# 10.3 创建 traffic-gen CronJob（每 5 分钟单次调用，补充流量脉冲）
curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/workloads" -d \
  "{\"envId\":\"$ENV_TEST\",\"type\":\"cronjob\",\"name\":\"paas-shop-traffic-pulse\",\"image\":\"$REGISTRY/paas-shop/traffic-gen:v2\",\"schedule\":\"*/5 * * * *\",\"command\":\"/svc once\"}" \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  cronjob:',d.get('id'),d.get('name'))" 2>/dev/null
echo "=== §10 流量生成资源创建完成 ==="

echo "=== §3 计费（模型绑定应用级 Key + 配额 + 账单，用量归因到应用）==="
# 3.1 绑定模型（type=models 平台约定复数）-> binding_injector 创建应用级 API Key + 注入 PAAS_LLM_API_KEY/PAAS_LLM_BASE_URL
#    注：早期 type=model（单数）不匹配 binding_injector case "models"，致应用级 Key 未注入；已修正。
curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/bindings" -d \
  '{"type":"models","name":"glm-5.2"}' \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});bs=d.get('bindings',[]);print('  binding:',bs[-1].get('type'),bs[-1].get('name') if bs else 'none')" 2>/dev/null
echo "  注：模型绑定注入 PAAS_LLM_API_KEY/PAAS_LLM_BASE_URL；chatbot 重启后用应用级 Key（gateway meter 用量归因到 paas-shop）"
# 3.1b 绑定知识库（type=knowledgebase）-> binding_injector 注入 PAAS_KB_ID/PAAS_KB_API_BASE（chatbot RAG 检索用）
if [ -n "$KB_ID" ]; then
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/bindings" -d \
    "{\"type\":\"knowledgebase\",\"name\":\"shop-faq\"}" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});bs=d.get('bindings',[]);print('  binding:',bs[-1].get('type'),bs[-1].get('name') if bs else 'none')" 2>/dev/null
  echo "  注：KB 绑定注入 PAAS_KB_ID（chatbot 调 /api/knowledgebases/{id}/retrieve 真向量检索）"
fi
# 3.2 设置配额（租户级，6 资源维度：applications/workloads/models/gpu/tokens/storage_gb）
curl -s -X PUT -H "$H" -H "Content-Type: application/json" "$B/api/billing/quota" -d \
  '{"limits":{"applications":10,"workloads":20,"models":10,"gpu":2,"tokens":1000000,"storage_gb":50}}' \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  quota:',d.get('limits'))" 2>/dev/null
# 3.3 生成账单（当前月，按用量 × 单价逐项算 amount）
PERIOD=$(date +%Y-%m)
curl -s -X POST -H "$H" "$B/api/billing/records/generate?period=$PERIOD" \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  bill:',d.get('id'),'total=',d.get('total'),'status=',d.get('status'))" 2>/dev/null
# 3.4 查用量（含 byApp 应用级归因；chatbot 用应用级 Key 后 byApp.paas-shop 有 token 用量）
curl -s -H "$H" "$B/api/billing/usage" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  usage byApp:',json.dumps(d.get('usage',{}).get('byApp',{}),ensure_ascii=False))" 2>/dev/null
echo "=== §3 计费资源创建完成 ==="

echo "=== §5 安全密钥（平台 Secret 集中管理 + 审计，凭证不散落 appconfig 明文）==="
# 创建 paas-shop 凭证 Secret（DB/Redis/qdrant）。平台掩码展示 + 写操作记审计。
# 注：演示密码值；实际部署时填数据服务真实密码（dataservice Connection 生成的）。
for s in \
  '{"name":"shop-db-password","type":"secret","value":"paas-shop-db-2026","desc":"paas-shop PostgreSQL 密码"}' \
  '{"name":"shop-redis-password","type":"secret","value":"paas-shop-redis-2026","desc":"paas-shop Redis 密码"}' \
  '{"name":"shop-qdrant-api-key","type":"secret","value":"paas-shop-qdrant-2026","desc":"paas-shop qdrant API key"}'; do
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/security/secrets" -d "$s" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  secret:',d.get('id'),d.get('name'))" 2>/dev/null
done
# 查审计日志（Create Secret 自动记审计，actor/动作/资源）
curl -s -H "$H" "$B/api/security/audit-logs?resourceType=secret" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);print('  审计 '+str(len(d))+' 条（secret 增删）')" 2>/dev/null
echo "=== §5 安全密钥创建完成 ==="

echo "=== §6 数据服务备份（平台 backup 模块，mock size + Create 即 completed）==="
# 为 paas-shop 三个数据服务创建全量备份记录（演示平台备份管理能力）。
for ds in shop-db shop-cache shop-kb; do
  DS_ID=$(curl -s -H "$H" "$B/api/dataservices" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);print(next((x['id'] for x in d if x.get('name')=='$ds'),''))" 2>/dev/null)
  [ -z "$DS_ID" ] && continue
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/backups" -d \
    "{\"resourceId\":\"$DS_ID\",\"type\":\"full\"}" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  backup '+('$ds')+':',d.get('id'),'size=',d.get('sizeMB'),'MB status=',d.get('status'))" 2>/dev/null
done
echo "=== §6 备份创建完成 ==="
