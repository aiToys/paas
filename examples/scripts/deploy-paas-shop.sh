#!/usr/bin/env bash
# 为 paas-shop 示例应用创建核心工作负载 + 绑定数据服务 + 业务 appconfig。
# 补齐仓库缺失部分：setup-paas-shop.sh 注释依赖本脚本建 paas-shop 4 service + statsworker cronjob。
#
# 前提：paas-shop 应用已建、shop-db/cache/mq 数据服务已建（running）、镜像已推 $REGISTRY/paas-shop/<svc>:tag。
# 幂等：workload 按 name 查重，已存在跳过。
set -uo pipefail
H="Authorization: Bearer sk-acme-admin"
B="http://paas.k8s.dd"
APP="paas-shop"
REGISTRY="${REGISTRY:?设置 REGISTRY 为集群 registry，如 <nodeIP>:30050}"
TAG="${TAG:-latest}"

ENV_TEST=$(curl -s -H "$H" "$B/api/environments" | python3 -c "import sys,json;d=json.load(sys.stdin);es=d.get('data',d if isinstance(d,list) else []);print(next((e['id'] for e in es if e.get('type')=='test'),es[0]['id'] if es else ''))" 2>/dev/null)
echo "ENV_TEST=$ENV_TEST  APP=$APP  REGISTRY=$REGISTRY  TAG=$TAG"

echo "=== 1. 创建 5 个 service workload（bff/product/recommend/chatbot/mcp）==="
# name 对齐 setup-paas-shop.sh §1 治理注册的服务名（paas-shop-product 等）。
# port 必须与 containerPort 一致：bff/mcp 间硬编码直连 containerPort（product:8081 等），
# applyService 是 CreateOrUpdate，port=80 重建 workload 会把 Service 端口改 80 -> 直连 refused。
# 仅 paas-shop-mcp 保持 port:80（Tool serverURL 无端口=80，与 containerPort:8080 经 Service 转发一致）。
for svc in \
  "{\"envId\":\"$ENV_TEST\",\"type\":\"service\",\"name\":\"paas-shop-product\",\"service\":\"product\",\"image\":\"$REGISTRY/paas-shop/product:$TAG\",\"replicas\":1,\"port\":8081,\"containerPort\":8081}" \
  "{\"envId\":\"$ENV_TEST\",\"type\":\"service\",\"name\":\"paas-shop-recommend\",\"service\":\"recommend\",\"image\":\"$REGISTRY/paas-shop/recommend:$TAG\",\"replicas\":1,\"port\":8082,\"containerPort\":8082}" \
  "{\"envId\":\"$ENV_TEST\",\"type\":\"service\",\"name\":\"paas-shop-chatbot\",\"service\":\"chatbot\",\"image\":\"$REGISTRY/paas-shop/chatbot:$TAG\",\"replicas\":1,\"port\":8083,\"containerPort\":8083}" \
  "{\"envId\":\"$ENV_TEST\",\"type\":\"service\",\"name\":\"paas-shop-bff\",\"service\":\"bff\",\"image\":\"$REGISTRY/paas-shop/bff:$TAG\",\"replicas\":1,\"port\":8080,\"containerPort\":8080}" \
  "{\"envId\":\"$ENV_TEST\",\"type\":\"service\",\"name\":\"paas-shop-mcp\",\"service\":\"mcp\",\"image\":\"$REGISTRY/paas-shop/mcp:$TAG\",\"replicas\":1,\"port\":80,\"containerPort\":8080}"; do
  NAME=$(echo "$svc" | python3 -c "import sys,json;print(json.load(sys.stdin)['name'])")
  # 查重：已存在跳过
  EXIST=$(curl -s -H "$H" "$B/api/workloads?type=service" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);print('yes' if any(x.get('name')=='$NAME' for x in d) else 'no')" 2>/dev/null)
  if [ "$EXIST" = "yes" ]; then
    echo "  skip (exists): $NAME"
    continue
  fi
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/workloads" -d "$svc" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  workload:',d.get('id'),d.get('name'))" 2>/dev/null
done

echo "=== 2. 创建 statsworker CronJob（*/10 分钟聚合统计回写 appconfig）==="
EXIST=$(curl -s -H "$H" "$B/api/workloads?type=cronjob" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);print('yes' if any(x.get('name')=='paas-shop-statsworker' for x in d) else 'no')" 2>/dev/null)
if [ "$EXIST" != "yes" ]; then
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/workloads" -d \
    "{\"envId\":\"$ENV_TEST\",\"type\":\"cronjob\",\"name\":\"paas-shop-statsworker\",\"image\":\"$REGISTRY/paas-shop/statsworker:$TAG\",\"schedule\":\"*/10 * * * *\"}" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  cronjob:',d.get('id'),d.get('name'))" 2>/dev/null
else
  echo "  skip (exists): paas-shop-statsworker"
fi

echo "=== 3. 绑定数据服务（shop-db/cache/mq -> product/recommend/statsworker）==="
# binding_injector 按 type 注入：db->DATABASE_URL, cache->REDIS_URL, mq->NATS_URL
# 绑定是应用级，注入到应用所有 workload（product/recommend/statsworker 各取所需 env）
for ds in \
  "{\"type\":\"db\",\"name\":\"shop-db\"}" \
  "{\"type\":\"cache\",\"name\":\"shop-cache\"}" \
  "{\"type\":\"mq\",\"name\":\"shop-mq\"}"; do
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/bindings" -d "$ds" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});bs=d.get('bindings',[]);print('  binding:',bs[-1].get('type'),bs[-1].get('name') if bs else 'exists')" 2>/dev/null
done

echo "=== 4. 业务 appconfig key（product/recommend 配置 + statsworker 凭证 + chatbot agent 模型）==="
# 查 shop-agent id（PAAS_AGENT_MODEL=agent:<id>，chatbot 经平台 Agent runtime 推理）
AGENT_ID=$(curl -s -H "$H" "$B/api/agents" | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',[]);items=d if isinstance(d,list) else d.get('items',[]);print(next((a['id'] for a in items if a.get('name')=='shop-agent'),''))" 2>/dev/null)
for kv in \
  "PRODUCT_PAGE_SIZE:20:env" \
  "RECOMMEND_COUNT:3:env" \
  "RECOMMEND_CACHE_TTL:300:env" \
  "PAAS_APPCONFIG_URL:http://paas-core.paas.svc.cluster.local:env" \
  "PAAS_APP_ID:paas-shop:env" \
  "PAAS_ENV_ID:$ENV_TEST:env" \
  "PAAS_AGENT_MODEL:agent:$AGENT_ID:env" \
  "PAAS_API_KEY:sk-acme-dev:secret"; do
  K="${kv%%:*}"; REST="${kv#*:}"; V="${REST%:*}"; TYPE="${REST##*:}"
  curl -s -o /dev/null -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/configs" -d \
    "{\"key\":\"$K\",\"value\":\"$V\",\"type\":\"$TYPE\",\"envId\":\"$ENV_TEST\"}" && echo "  cfg: $K"
done

echo "=== 5. 等 reconciler 落地 + 验证 ==="
sleep 15
echo "--- paas-shop pods ---"
kubectl get pods -n paas-t-acme -l paas.aitoys/app=paas-shop 2>/dev/null | tail -10
echo "--- statsworker cronjob ---"
kubectl get cronjob paas-shop-statsworker -n paas-t-acme 2>/dev/null | tail -2
echo "=== deploy-paas-shop.sh 完成 ==="
