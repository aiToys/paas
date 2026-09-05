#!/usr/bin/env bash
# setup.sh —— paas-shop 演示资源初始化（spec §9 七步 + 演示编排）。
# 幂等：先查后建，重复执行安全。域名统一 *.example.com（对外演示材料不泄漏内网地址）；
# 本机访问演示域名：curl -H 'Host: shop.example.com' http://<集群入口>/ 或 hosts 解析。
set -euo pipefail

PAAS_API="${PAAS_API:-http://paas.k8s.dd}"
PAAS_TOKEN="${PAAS_TOKEN:-sk-acme-admin}"
APP_ID="${APP_ID:-}"
TENANT_ID="t-acme"
ENV_TEST="env-acme-test"
ENV_PROD="env-acme-prod-bj"
ACTION_TOKEN="${ACTION_TOKEN:-paas-shop-order-token-2026}"

api() { curl -sS -H "Authorization: Bearer $PAAS_TOKEN" "$@"; }
jget() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null || true; }

# resolve_app_id 按名解析应用 ID（APP_ID 未显式指定时）。
resolve_app_id() {
  if [ -z "$APP_ID" ]; then
    APP_ID=$(api "$PAAS_API/api/applications" | jget "[0]['id']") # 列表首项=paas-shop（单演示应用）
  fi
  echo "APP_ID=$APP_ID"
}

# ensure_data_service 幂等建数据服务（同名跳过）。
ensure_data_service() { # kind name engine envId extra_spec_json
  local exists
  exists=$(api "$PAAS_API/api/dataservices?kind=$1" | jget "[? \"$1\"=='x'][0]['id']" 2>/dev/null)
  # 简化：按名过滤
  exists=$(api "$PAAS_API/api/dataservices?kind=$1" | python3 -c "
import sys,json
for d in json.load(sys.stdin)['data']:
    if d['name']=='$2': print(d['id']); break" 2>/dev/null || true)
  if [ -n "$exists" ]; then echo "  已存在 $2 ($exists)"; return 0; fi
  api -X POST "$PAAS_API/api/dataservices" -H "Content-Type: application/json" \
    -d "{\"kind\":\"$1\",\"name\":\"$2\",\"envId\":\"$4\",\"spec\":{\"engine\":\"$3\"$5}}" | jget "['data']['id']" >/dev/null && echo "  已创建 $2"
}

# ensure_workload 幂等建 workload（按 app+env+lane+service 查重）。
ensure_workload() { # json_file
  local payload; payload=$(cat "$1")
  local name; name=$(echo "$payload" | jget "['name']")
  local exists
  exists=$(api "$PAAS_API/api/applications/$APP_ID/workloads" | python3 -c "
import sys,json
for w in json.load(sys.stdin)['data']:
    if w['name']=='$name': print('yes'); break" 2>/dev/null || true)
  if [ "$exists" = "yes" ]; then echo "  已存在 workload $name"; return 0; fi
  api -X POST "$PAAS_API/api/applications/$APP_ID/workloads" \
    -H "Content-Type: application/json" -d "$payload" >/dev/null && echo "  已创建 workload $name"
}

# ensure_appconfig 幂等 upsert 应用配置（app×env×key 查重）。
ensure_appconfig() { # envId key value type
  local exists
  exists=$(api "$PAAS_API/api/applications/$APP_ID/configs?envId=$1" | python3 -c "
import sys,json
for c in json.load(sys.stdin)['data']:
    if c['key']=='$2': print('yes'); break" 2>/dev/null || true)
  local body="{\"appId\":\"$APP_ID\",\"envId\":\"$1\",\"key\":\"$2\",\"value\":\"$3\",\"type\":\"$4\"}"
  if [ "$exists" = "yes" ]; then
    api -X POST "$PAAS_API/api/applications/$APP_ID/configs" -H "Content-Type: application/json" -d "$body" >/dev/null
    echo "  已更新配置 $2"
  else
    api -X POST "$PAAS_API/api/applications/$APP_ID/configs" -H "Content-Type: application/json" -d "$body" >/dev/null
    echo "  已创建配置 $2"
  fi
}

echo "== part 1: test 侧共享资源（Secret/Action-Token/MQ 登记）=="
resolve_app_id

# 1. 订单操作令牌：appconfig type=secret（静态加密 enc:v1 落库 + 按环境桶注入全部 workload）
ensure_appconfig "$ENV_TEST" "PAAS_ORDER_ACTION_TOKEN" "$ACTION_TOKEN" "secret"

# 2. 平台 Secret 实体（演示掩码管理面；scope=tenant）
sec_exists=$(api "$PAAS_API/api/security/secrets" | python3 -c "
import sys,json
for s in json.load(sys.stdin)['data']:
    if s['name']=='paas-shop-order-action': print('yes'); break" 2>/dev/null || true)
if [ "$sec_exists" != "yes" ]; then
  api -X POST "$PAAS_API/api/security/secrets" -H "Content-Type: application/json" \
    -d "{\"name\":\"paas-shop-order-action\",\"type\":\"secret\",\"scope\":\"tenant\",\"value\":\"$ACTION_TOKEN\",\"desc\":\"paas-shop 订单操作令牌（演示静态加密）\"}" >/dev/null
  echo "  已创建平台 Secret paas-shop-order-action"
fi

# 3. MQ topic + 双消费组登记（管理面对齐真实业务流，spec §4）
#    topic 需挂 shop MQ 实例：先解析 test 环境 mq 实例 ID。
MQ_ID=$(api "$PAAS_API/api/dataservices?kind=mq" | python3 -c "
import sys,json
for d in json.load(sys.stdin)['data']:
    if d['envId']=='$ENV_TEST': print(d['id']); break" 2>/dev/null || true)
if [ -n "$MQ_ID" ]; then
  TOPIC_ID=$(api "$PAAS_API/api/mq-topics?mqId=$MQ_ID" | python3 -c "
import sys,json
for t in json.load(sys.stdin)['data']:
    if t['name']=='shop-events': print(t['id']); break" 2>/dev/null || true)
  if [ -z "$TOPIC_ID" ]; then
    TOPIC_ID=$(api -X POST "$PAAS_API/api/mq-topics" -H "Content-Type: application/json" \
      -d "{\"mqId\":\"$MQ_ID\",\"name\":\"shop-events\"}" | jget "['data']['id']")
    echo "  已登记 topic shop-events"
  fi
  for grp in bff-consumer stats-consumer; do
    cg=$(api "$PAAS_API/api/consumer-groups?topicId=$TOPIC_ID" | python3 -c "
import sys,json
for g in json.load(sys.stdin)['data']:
    if g['name']=='$grp': print('yes'); break" 2>/dev/null || true)
    if [ "$cg" != "yes" ]; then
      api -X POST "$PAAS_API/api/consumer-groups" -H "Content-Type: application/json" \
        -d "{\"topicId\":\"$TOPIC_ID\",\"name\":\"$grp\"}" >/dev/null
      echo "  已登记消费组 $grp"
    fi
  done
else
  echo "  [跳过] test 环境无 mq 实例（shop-nats）——请先确认数据服务"
fi
