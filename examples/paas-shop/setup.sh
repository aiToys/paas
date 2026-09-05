#!/usr/bin/env bash
# setup.sh —— paas-shop 演示资源初始化（spec §9 七步 + 演示编排）。
# 幂等：先查后建，重复执行安全。域名统一 *.example.com（对外演示材料不泄漏内网地址）；
# 本机访问演示域名：curl -H 'Host: shop.example.com' http://<集群入口>/ 或 hosts 解析。
#
# 对 brief 的实测偏差（openapi/handler 实证，2026-09-05）：
# 1. cron_image 不按 tag 子串匹配（Image.Tag=<branch>-<commit8>[-<argsHash8>]，buildArgs 只进
#    sha256 哈希不含 SERVICE 名）——改走 buildruns 列表按 buildArgs.SERVICE 取最近成功构建的
#    imageId，再查 /api/images/{id} 拼 registry:tag；支持 SWEEPER_IMG env 手工覆盖。
# 2. sweeper TTL 按环境显式注入 appconfig ORDER_TTL_MINUTES：test=2（演示立等可取）/prod=30
#    （sweeper 二进制 fallback 恰为 30，显式化语义）。
# 3. 审查修复（R1）：应用绑定 bind type=具体 kind 非 "dataservice"（store 白名单+injector 只认
#    具体 kind）；治理 Service 创建 payload 补必填 envId/protocol/port（Service.Validate）。
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

echo "== part 2: prod 全套（spec §9）=="

# ① prod 数据服务（环境级数据隔离）
ensure_data_service db shop-db-prod postgres "$ENV_PROD" ',"version":"15","size_gb":"20"'
ensure_data_service cache shop-redis-prod redis "$ENV_PROD" ',"size_gb":"1"'
ensure_data_service mq shop-nats-prod nats "$ENV_PROD" ',"size_gb":"1"'

# 应用双绑定。bind type=具体 kind（db/cache/mq/...）非 "dataservice" 大类：
# memory store 白名单不含 dataservice（400 吞错）；BindingInjector 只认具体 kind
# 才注入 DATABASE_URL/REDIS_URL/NATS_URL，否则 prod 服务起动即退。
for kv in "db:shop-db-prod" "cache:shop-redis-prod" "mq:shop-nats-prod"; do
  kind="${kv%%:*}"; ds="${kv#*:}"
  DS_ID=$(api "$PAAS_API/api/dataservices" | python3 -c "
import sys,json
for d in json.load(sys.stdin)['data']:
    if d['name']=='$ds': print(d['id']); break" 2>/dev/null || true)
  if [ -n "$DS_ID" ]; then
    api -X POST "$PAAS_API/api/applications/$APP_ID/bindings" -H "Content-Type: application/json" \
      -d "{\"type\":\"$kind\",\"name\":\"$ds\"}" >/dev/null 2>&1 || true
  fi
done
echo "  双绑定完成（BindingInjector 自动注入 prod 桶 appconfig）"

# ② prod baseline workload：6 服务 + 2 CronJob（显式命名 -prod 后缀规避 Service 撞名，事实 1）
#    image 取自 test 同 Service workload 的 imageRef（同源复用）。
prod_svc_image() { # svc_name
  api "$PAAS_API/api/applications/$APP_ID/workloads" | python3 -c "
import sys,json
for w in json.load(sys.stdin)['data']:
    if w.get('service')=='$1' and w.get('envId')=='$ENV_TEST' and w.get('type')=='service':
        print(w.get('imageRef') or w['image']); break" 2>/dev/null || true
}
for svc in product recommend order chatbot bff frontend; do
  IMG=$(prod_svc_image "$svc")
  [ -z "$IMG" ] && { echo "  [警告] $svc 无 test 镜像可复用，跳过"; continue; }
  cat > /tmp/wl-prod.json <<EOF
{
  "appId": "$APP_ID", "envId": "$ENV_PROD", "laneId": "default",
  "service": "$svc", "type": "service", "name": "paas-shop-$svc-prod",
  "image": "$IMG", "replicas": 1,
  "port": $( [ "$svc" = "bff" ] && echo 8080 || ([ "$svc" = "frontend" ] && echo 80 || ([ "$svc" = "product" ] && echo 8081 || ([ "$svc" = "recommend" ] && echo 8082 || ([ "$svc" = "chatbot" ] && echo 8083 || echo 8084))))),
  "resources": {"cpuRequest": "100m", "cpuLimit": "500m", "memRequest": "128Mi", "memLimit": "256Mi"}
}
EOF
  ensure_workload /tmp/wl-prod.json
done

# CronJob：sweeper-prod（TTL=30min）+ statsworker-prod（恢复部署）
# cron_image 解析某 SERVICE 最近一次成功构建的镜像全量引用（registry:tag）。
# 实测偏差（见文件头注释 1）：tag 只含 buildArgs 哈希不含 SERVICE 名，改走 buildruns 列表
# （两 Store 实现均 StartedAt 降序，首个匹配=该 SERVICE 最近成功构建）。
cron_image() { # buildArgs SERVICE
  local bid
  bid=$(api "$PAAS_API/api/applications/$APP_ID/buildruns" | python3 -c "
import sys,json
for b in json.load(sys.stdin)['data']:
    if b.get('status')=='success' and (b.get('buildArgs') or {}).get('SERVICE')=='$1' and b.get('imageId'):
        print(b['imageId']); break" 2>/dev/null || true)
  [ -z "$bid" ] && return 0
  api "$PAAS_API/api/images/$bid" | python3 -c "
import sys,json
d=json.load(sys.stdin)['data']
print(d['registry']+':'+d['tag'])" 2>/dev/null || true
}
SWEEPER_IMG="${SWEEPER_IMG:-$(cron_image sweeper)}"
[ -n "$SWEEPER_IMG" ] && cat > /tmp/wl-sweeper-prod.json <<EOF
{"appId":"$APP_ID","envId":"$ENV_PROD","laneId":"default","service":"sweeper","type":"cronjob",
 "name":"paas-shop-sweeper-prod","image":"$SWEEPER_IMG","schedule":"*/5 * * * *",
 "resources":{"cpuRequest":"50m","cpuLimit":"200m","memRequest":"64Mi","memLimit":"128Mi"}}
EOF
[ -n "$SWEEPER_IMG" ] && ensure_workload /tmp/wl-sweeper-prod.json
[ -z "$SWEEPER_IMG" ] && echo "  [警告] sweeper 无构建镜像（可 SWEEPER_IMG=registry/repo:tag 覆盖），跳过 sweeper-prod"

# statsworker-prod（恢复部署；消费统计任务低频 */10，test 侧既有 schedule 已不可考）
STATSWORKER_IMG="${STATSWORKER_IMG:-$(cron_image statsworker)}"
[ -n "$STATSWORKER_IMG" ] && cat > /tmp/wl-statsworker-prod.json <<EOF
{"appId":"$APP_ID","envId":"$ENV_PROD","laneId":"default","service":"statsworker","type":"cronjob",
 "name":"paas-shop-statsworker-prod","image":"$STATSWORKER_IMG","schedule":"*/10 * * * *",
 "resources":{"cpuRequest":"50m","cpuLimit":"200m","memRequest":"64Mi","memLimit":"128Mi"}}
EOF
[ -n "$STATSWORKER_IMG" ] && ensure_workload /tmp/wl-statsworker-prod.json
[ -z "$STATSWORKER_IMG" ] && echo "  [警告] statsworker 无构建镜像（可 STATSWORKER_IMG=registry/repo:tag 覆盖），跳过 statsworker-prod"

# ③ prod 域名 Route：shop.example.com / → frontend-prod、/api → bff-prod（先建治理 Service 再 Route）
ensure_gov_service() { # name envId port
  local exists
  exists=$(api "$PAAS_API/api/services" | python3 -c "
import sys,json
for s in json.load(sys.stdin)['data']:
    if s['name']=='$1': print(s['id']); break" 2>/dev/null || true)
  if [ -n "$exists" ]; then echo "$exists"; return 0; fi
  # Service.Validate 必填 name/envId/protocol(http|grpc)/port>0（缺任一 400）
  api -X POST "$PAAS_API/api/services" -H "Content-Type: application/json" \
    -d "{\"name\":\"$1\",\"envId\":\"$2\",\"protocol\":\"http\",\"port\":$3}" | jget "['data']['id']"
}
SVC_FE=$(ensure_gov_service paas-shop-frontend-prod "$ENV_PROD" 80)
SVC_BFF=$(ensure_gov_service paas-shop-bff-prod "$ENV_PROD" 8080)
ensure_route() { # name host path serviceId
  local exists
  exists=$(api "$PAAS_API/api/routes" | python3 -c "
import sys,json
for r in json.load(sys.stdin)['data']:
    if r['name']=='$1': print('yes'); break" 2>/dev/null || true)
  [ "$exists" = "yes" ] && { echo "  已存在 Route $1"; return 0; }
  api -X POST "$PAAS_API/api/routes" -H "Content-Type: application/json" \
    -d "{\"name\":\"$1\",\"host\":\"$2\",\"path\":\"$3\",\"serviceId\":\"$4\",\"methods\":[\"ANY\"],\"enabled\":true}" >/dev/null
  echo "  已创建 Route $1（$2$3）"
}
ensure_route shop-prod-root shop.example.com / "$SVC_FE"
ensure_route shop-prod-api shop.example.com /api "$SVC_BFF"
# test 域名同构（对齐三套域名方案 spec §10）
SVC_FE_T=$(ensure_gov_service paas-shop-frontend "$ENV_TEST" 80)
SVC_BFF_T=$(ensure_gov_service paas-shop-bff "$ENV_TEST" 8080)
ensure_route shop-test-root shop.test.example.com / "$SVC_FE_T"
ensure_route shop-test-api shop.test.example.com /api "$SVC_BFF_T"

# ④ bff prod 环境配置（按环境隔离，spec §9④）
ensure_appconfig "$ENV_PROD" "PRODUCT_SERVICE_URL" "http://paas-shop-product-prod:8081" "env"
ensure_appconfig "$ENV_PROD" "RECOMMEND_SERVICE_URL" "http://paas-shop-recommend-prod:8082" "env"
ensure_appconfig "$ENV_PROD" "CHATBOT_SERVICE_URL" "http://paas-shop-chatbot-prod:8083" "env"
ensure_appconfig "$ENV_PROD" "ORDER_SERVICE_URL" "http://paas-shop-order-prod:8084" "env"
ensure_appconfig "$ENV_PROD" "PAAS_ORDER_ACTION_TOKEN" "$ACTION_TOKEN" "secret"

# ③' sweeper TTL 按环境注入（brief 核对点③；sweeper 读 ORDER_TTL_MINUTES，分钟数）：
# test=2 演示立等可取 / prod=30 语义化显式（fallback 恰为 30）。
ensure_appconfig "$ENV_TEST" "ORDER_TTL_MINUTES" "2" "env"
ensure_appconfig "$ENV_PROD" "ORDER_TTL_MINUTES" "30" "env"

# ⑤ 发布顺序：无操作（事实 3：prod-bj 天然晋升目标，e2e 断言覆盖）

# ⑥ 备份记录（backup 前端入口演示）
DB_PROD_ID=$(api "$PAAS_API/api/dataservices" | python3 -c "
import sys,json
for d in json.load(sys.stdin)['data']:
    if d['name']=='shop-db-prod': print(d['id']); break" 2>/dev/null || true)
if [ -n "$DB_PROD_ID" ]; then
  # backup POST 仅接收 resourceId（model 无 name 字段，多传静默丢弃）
  api -X POST "$PAAS_API/api/backups" -H "Content-Type: application/json" \
    -d "{\"resourceId\":\"$DB_PROD_ID\"}" >/dev/null 2>&1 || true
  echo "  备份记录已建（shop-db-prod）"
fi

echo "== setup part 2 完成 =="
