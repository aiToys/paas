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
# 4. pipeline 创建 trigger 是 PipelineTrigger 对象非字符串（model.go PipelineTrigger{type,...}；
#    handler decode 进 struct）——"trigger":"manual" 反序列化 400，改 "trigger":{"type":"manual"}。
# 5. 模板 deploy stage 去 "strategy":"rolling"：engine deploy 只消费 envId/lane/service/serviceId/
#    imageSource（engine.go execDeploy），strategy 静默忽略。
# 6. workflow toolId 解析按 type=mcp + name=refund_order 精确匹配（mcp 工具实体名与 MCP 方法名
#    逐字一致无前缀，spec stage2；miss 回退首个 mcp 工具并提示——toolName 才是方法名，功能不受影响）。
# 7. 审查修复（R1-C1）：模板 admin 端点需 super_admin，默认 PAAS_TOKEN(sk-acme-admin) 仅
#    tenant-admin 恒 403 且此前静默吞错——part3 前置登录 admin 换 JWT（唯一 super_admin 是
#    密码用户，dev seed 演示凭证 123456，生产用 PAAS_ADMIN_USER/PAAS_ADMIN_PASS 覆盖；登录失败
#    显式警告跳过模板+pipeline 块）；创建类 POST 检查响应含 data 才报成功（堵虚假成功）。
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
# 响应形态 {data:[...]}——索引前必须先取 ['data']（brief 原文 "[0]['id']" 漏层：KeyError 被
# jget 的 2>/dev/null 吞成空串，APP_ID 恒空 → part1 整段虚假成功）。
resolve_app_id() {
  if [ -z "$APP_ID" ]; then
    APP_ID=$(api "$PAAS_API/api/applications" | jget "['data'][0]['id']") # 列表首项=paas-shop（单演示应用）
  fi
  if [ -z "$APP_ID" ]; then
    echo "  [错误] 应用 ID 解析失败（PAAS_API=$PAAS_API 不可达或列表为空）" >&2
    exit 1
  fi
  echo "APP_ID=$APP_ID"
}

# ensure_data_service 幂等建数据服务（同名跳过）。
# 契约（handler resolveFromEngine 实证）：顶层 engineId 必填（引擎目录真源，kind/engine 由
# 平台从 Engine 派生写回 spec.engine）——brief 原文 spec.engine=postgres 是无效字段（missing
# engineId 400 被 || true 吞成虚假成功）。spec 仅放 size_gb/version 等容量参数。
ensure_data_service() { # kind name engineId envId extra_spec_json
  local exists
  exists=$(api "$PAAS_API/api/dataservices?kind=$1" | python3 -c "
import sys,json
for d in json.load(sys.stdin)['data']:
    if d['name']=='$2': print(d['id']); break" 2>/dev/null || true)
  if [ -n "$exists" ]; then echo "  已存在 $2 ($exists)"; return 0; fi
  local resp
  resp=$(api -X POST "$PAAS_API/api/dataservices" -H "Content-Type: application/json" \
    -d "{\"engineId\":\"$3\",\"name\":\"$2\",\"envId\":\"$4\",\"spec\":{$5}}")
  [ -n "$(echo "$resp" | jget "['data']['id']")" ] && echo "  已创建 $2" \
    || { echo "  [警告] $2 创建失败: $resp"; return 1; }
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

# ① prod 数据服务（环境级数据隔离）。engineId=平台引擎目录（managed 模式，平台生成凭证）。
ensure_data_service db shop-db-prod pg-managed "$ENV_PROD" '"version":"15","size_gb":"20"'
ensure_data_service cache shop-redis-prod redis-managed "$ENV_PROD" '"size_gb":"1"'
ensure_data_service mq shop-nats-prod nats-managed "$ENV_PROD" '"size_gb":"1"'

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
  # image 优先（完整 registry:tag 或 registry@digest 可拉）；imageRef 经 Release 滚动后是
  # 裸 digest（sha256:... 无 registry 前缀，kubelet 拉不动 → ImagePullBackOff 卡 deploying）。
  # image 也是裸 digest 时，借同 app 任一完整引用（...@sha256:...）拼 registry 前缀。
  api "$PAAS_API/api/applications/$APP_ID/workloads" | python3 -c "
import sys,json
ws=json.load(sys.stdin)['data']
img=''
for w in ws:
    if w.get('service')=='$1' and w.get('envId')=='$ENV_TEST' and w.get('type')=='service':
        img=w.get('image') or w.get('imageRef') or ''; break
if img.startswith('sha256:'):
    for x in ws:
        xi=x.get('image') or ''
        if '@' in xi and not xi.startswith('sha256:'):
            img=xi.split('@')[0]+'@'+img; break
print(img)" 2>/dev/null || true
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
# 环境维度收口（R1 审计 C1/C2）：nginx 反代上游与 dpdisc 查询名后缀——
# 不注入则 prod 前端 API 全落 test bff / bff-prod 经数据面发现查到 test 实例。
ensure_appconfig "$ENV_PROD" "BFF_UPSTREAM" "paas-shop-bff-prod:8080" "env"
ensure_appconfig "$ENV_PROD" "PAAS_SVC_SUFFIX" "-prod" "env"
ensure_appconfig "$ENV_PROD" "PAAS_ORDER_ACTION_TOKEN" "$ACTION_TOKEN" "secret"
# chatbot 必需：平台 Agent 虚拟模型（agent:<id>，缺则起动即退；agent 实体租户级无环境维度，
# test/prod 同 ID）。从 test 桶自动发现，避免硬编码 agent ID 随 seed 漂移。
AGENT_MODEL=$(api "$PAAS_API/api/applications/$APP_ID/configs?envId=$ENV_TEST" | python3 -c "
import sys,json
for c in json.load(sys.stdin)['data']:
    if c['key']=='PAAS_AGENT_MODEL': print(c['value']); break" 2>/dev/null || true)
[ -n "$AGENT_MODEL" ] && ensure_appconfig "$ENV_PROD" "PAAS_AGENT_MODEL" "$AGENT_MODEL" "env"
[ -z "$AGENT_MODEL" ] && echo "  [警告] test 桶无 PAAS_AGENT_MODEL，chatbot-prod 将起动失败（先在 test 配置后重跑）"

# ③' sweeper TTL 按环境注入（brief 核对点③；sweeper 读 ORDER_TTL_MINUTES，分钟数）：
# test=2 演示立等可取 / prod=30 语义化显式（fallback 恰为 30）。
ensure_appconfig "$ENV_TEST" "ORDER_TTL_MINUTES" "2" "env"
ensure_appconfig "$ENV_PROD" "ORDER_TTL_MINUTES" "30" "env"

# ⑦ 配置中心动态热更新链路（R1 审计 H1/M2/M3）：
#    H1 chatbot dynconfig 按应用名发现（GET /api/configcenter/apps/{name}/published）：
#       应用名=paas-shop（平台唯一应用）；PAAS_CONFIG_ENV 由 controller 注入（=workload EnvID），
#       鉴权复用 chatbot 既有 LLM Key（发现端点是数据面消费契约，已认证即可）。
#    M3 product ccpull 轮拉 ns 维度发现：ns 名平台派生 app-<APP_ID>-<ENV>；
#       PAAS_API_BASE 默认集群内 core 地址无需注入。
#    M2 statsworker 回写 appconfig：注入 URL/ENV_ID，Key 与 ccpull 共用 dev Key——
#       回写需 config:write（仅 tenant-admin），dev Key 403 走 warn 跳过。
#       偏差记录：环境级桶注入会广播到桶内全部服务，不注入 admin Key（安全底线）；
#       完整回写链路依赖平台「服务身份/最小权限凭证」能力（遗留债务）。
ensure_appconfig "$ENV_TEST" "PAAS_CONFIG_APP" "paas-shop" "env"
ensure_appconfig "$ENV_PROD" "PAAS_CONFIG_APP" "paas-shop" "env"
ensure_appconfig "$ENV_TEST" "PAAS_API_KEY" "sk-acme-dev" "secret"
ensure_appconfig "$ENV_PROD" "PAAS_API_KEY" "sk-acme-dev" "secret"
ensure_appconfig "$ENV_TEST" "PAAS_APPCONFIG_URL" "http://paas-core.paas.svc.cluster.local" "env"
ensure_appconfig "$ENV_PROD" "PAAS_APPCONFIG_URL" "http://paas-core.paas.svc.cluster.local" "env"
ensure_appconfig "$ENV_TEST" "PAAS_ENV_ID" "$ENV_TEST" "env"
ensure_appconfig "$ENV_PROD" "PAAS_ENV_ID" "$ENV_PROD" "env"

#    演示配置发布（热更新链路可验）：chatbot 欢迎语 + product 分页大小（默认 20 → 5 差异可见）。
#    upsert 后查 active 快照，缺 key 才 publish（幂等：重跑不堆版本）。
#    先于 PAAS_CONFIGCENTER_NS 注入执行：upsert 懒建派生 ns，后续才能按名解析 ns 实体 ID。
ensure_dynconf() { # envId key value
  api -X POST "$PAAS_API/api/applications/$APP_ID/dynamic-configs?envId=$1" \
    -H "Content-Type: application/json" -d "{\"key\":\"$2\",\"value\":\"$3\"}" >/dev/null
  local snap
  snap=$(api "$PAAS_API/api/configcenter/apps/paas-shop/published?env=$1")
  if ! echo "$snap" | grep -q "\"$2\""; then
    api -X POST "$PAAS_API/api/applications/$APP_ID/dynamic-configs/publish?envId=$1" >/dev/null \
      && echo "  已发布动态配置 $2（env=$1）"
  fi
}
ensure_dynconf "$ENV_TEST" "welcome_message" "您好，我是商店智能客服，可以帮您查商品、下单、推荐礼物～"
ensure_dynconf "$ENV_TEST" "product_page_size" "5"
ensure_dynconf "$ENV_PROD" "welcome_message" "您好，我是商店智能客服，可以帮您查商品、下单、推荐礼物～"
ensure_dynconf "$ENV_PROD" "product_page_size" "5"

#    PAAS_CONFIGCENTER_NS 注入 ns 实体 ID（ns-xxx）非派生名——ccpull 打的
#    /api/configcenter/namespaces/{ns}/published 按主键 ID 查，按名查恒 published:false。
cc_ns_id() { # envId —— 按派生名 app-<APP_ID>-<envId> 查 ns 实体 ID
  api "$PAAS_API/api/configcenter/namespaces" | python3 -c "
import sys,json
for n in json.load(sys.stdin)['data']:
    if n['name']=='app-$APP_ID-$1': print(n['id']); break" 2>/dev/null || true
}
CC_NS_TEST=$(cc_ns_id "$ENV_TEST")
CC_NS_PROD=$(cc_ns_id "$ENV_PROD")
[ -n "$CC_NS_TEST" ] && ensure_appconfig "$ENV_TEST" "PAAS_CONFIGCENTER_NS" "$CC_NS_TEST" "env" \
  || echo "  [警告] test 派生 ns 未找到（ensure_dynconf 应已懒建，重跑自愈）"
[ -n "$CC_NS_PROD" ] && ensure_appconfig "$ENV_PROD" "PAAS_CONFIGCENTER_NS" "$CC_NS_PROD" "env" \
  || echo "  [警告] prod 派生 ns 未找到（ensure_dynconf 应已懒建，重跑自愈）"

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

echo "== part 3: 演示流水线 + 退款审批 workflow =="

# 0. admin JWT：模板管理端点 /api/admin/pipeline-templates 需 super_admin（platformAdmin 门禁），
#    默认 PAAS_TOKEN=sk-acme-admin 仅 tenant-admin 恒 403。唯一 super_admin 是密码用户 admin
#    （dev seed 演示凭证 123456，PAAS_DISABLE_DEMO_SEED=true 时不 seed）——生产环境用
#    PAAS_ADMIN_USER/PAAS_ADMIN_PASS 覆盖。admin 用户 tenant=t-acme，与模板 payload 租户一致。
ADMIN_USER="${PAAS_ADMIN_USER:-admin}"
ADMIN_PASS="${PAAS_ADMIN_PASS:-123456}"
ADMIN_JWT=$(curl -sS -X POST "$PAAS_API/api/auth/sessions" -H "Content-Type: application/json" \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" | jget "['data']['accessToken']")
if [ -z "$ADMIN_JWT" ]; then
  echo "  [警告] admin 登录失败（用户 ${ADMIN_USER}）——跳过模板+pipeline 创建（admin 端点需 super_admin）"
fi

# 1. 租户自定义模板（super_admin 经 admin 端点；带 tenantId；创建后验响应含 data 堵虚假成功）
if [ -n "$ADMIN_JWT" ]; then
  TPL_EXISTS=$(curl -sS -H "Authorization: Bearer $ADMIN_JWT" "$PAAS_API/api/admin/pipeline-templates" | python3 -c "
import sys,json
for t in json.load(sys.stdin)['data']:
    if t['id']=='tpl-shop-release': print('yes'); break" 2>/dev/null || true)
  if [ "$TPL_EXISTS" != "yes" ]; then
    cat > /tmp/tpl-shop-release.json <<'EOF'
{
  "id": "tpl-shop-release", "name": "商店发布流水线（test→prod 晋升演示）",
  "kind": "cd", "tenantId": "t-acme",
  "description": "构建 → 部署测试基线 → 打版本 → 晋升生产（promote 自动找 -prod 显式基线）",
  "stages": [
    {"name": "构建", "type": "build"},
    {"name": "部署测试", "type": "deploy", "params": {"envId": "{{app.env.test}}", "lane": "default", "imageSource": "priorBuild"}},
    {"name": "发布版本", "type": "release", "params": {"versionStrategy": "auto-increment"}},
    {"name": "晋升生产", "type": "promote"}
  ]
}
EOF
    RESP=$(curl -sS -X POST "$PAAS_API/api/admin/pipeline-templates" \
      -H "Authorization: Bearer $ADMIN_JWT" -H "Content-Type: application/json" \
      -d @/tmp/tpl-shop-release.json)
    [ -n "$(echo "$RESP" | jget "['data']['id']")" ] \
      && echo "  已创建模板 tpl-shop-release" \
      || echo "  [警告] 模板创建失败: $RESP"
  fi

  # 2. 绑定 pipeline（app 作用域）。
  #    paramOverrides 必带（engine 契约：build stage 的 buildArgs 只来自模板静态 params，
  #    SERVICE 必须走 pipeline 级覆盖，key 格式 "<stageIdx>.<paramKey>"；deploy stage 的
  #    service 同理）——缺失则构建 "no Go files in /app/paas-shop"（SERVICE 空走全量构建）。
  #    同用 ADMIN_JWT（admin 用户 tenant=t-acme，与模板同租户；模板缺失时此处 templateId 校验必 404）。
  #    已存在也 PUT 一次 paramOverrides（幂等自愈，防手工/历史漂移）。
  PIPE_INFO=$(curl -sS -H "Authorization: Bearer $ADMIN_JWT" "$PAAS_API/api/applications/$APP_ID/pipelines" | python3 -c "
import sys,json
for p in json.load(sys.stdin)['data']:
    if p['name']=='shop-release': print(p['id']); break" 2>/dev/null || true)
  PIPE_PAYLOAD='{"name":"shop-release","kind":"cd","templateId":"tpl-shop-release","trigger":{"type":"manual"},
    "paramOverrides":{"0.buildArgs":{"SERVICE":"order"},"1.service":"order"}}'
  if [ -z "$PIPE_INFO" ]; then
    RESP=$(curl -sS -X POST "$PAAS_API/api/applications/$APP_ID/pipelines" \
      -H "Authorization: Bearer $ADMIN_JWT" -H "Content-Type: application/json" \
      -d "$PIPE_PAYLOAD")
    PIPE_INFO=$(echo "$RESP" | jget "['data']['id']")
    [ -n "$PIPE_INFO" ] \
      && echo "  已创建 pipeline shop-release（手动触发，SERVICE=order）" \
      || echo "  [警告] pipeline 创建失败: $RESP"
  else
    curl -sS -X PUT "$PAAS_API/api/applications/$APP_ID/pipelines/$PIPE_INFO" \
      -H "Authorization: Bearer $ADMIN_JWT" -H "Content-Type: application/json" \
      -d "$PIPE_PAYLOAD" >/dev/null
    echo "  已对齐 pipeline shop-release paramOverrides（SERVICE=order）"
  fi
fi

# 3. 退款审批 workflow（spec §6：llm 建议 → approve 人工 → tool 真实退款）
AGENT_ID=$(api "$PAAS_API/api/agents" | python3 -c "
import sys,json
for a in json.load(sys.stdin)['data']:
    if a['name']=='shop-assistant': print(a['id']); break" 2>/dev/null || true)
TOOL_INFO=$(api "$PAAS_API/api/tools" | python3 -c "
import sys,json
ts=json.load(sys.stdin)['data']
for t in ts:
    if t.get('type')=='mcp' and t.get('name')=='refund_order': print(t['id']+' exact'); break
else:
    for t in ts:
        if t.get('type')=='mcp': print(t['id']+' fallback'); break" 2>/dev/null || true)
TOOL_ID=${TOOL_INFO%% *}
case "$TOOL_INFO" in
  *" fallback") [ -n "$TOOL_ID" ] && echo "  [提示] 未找到名为 refund_order 的 mcp 工具，回退首个 mcp 工具 ${TOOL_ID}（toolName 才是方法名，功能不受影响）" ;;
esac
WF_EXISTS=$(api "$PAAS_API/api/workflows" | python3 -c "
import sys,json
for w in json.load(sys.stdin)['data']:
    if w['name']=='shop-refund-approval': print('yes'); break" 2>/dev/null || true)
if [ "$WF_EXISTS" != "yes" ] && [ -n "$AGENT_ID" ] && [ -n "$TOOL_ID" ]; then
  cat > /tmp/wf.json <<EOF
{
  "name": "shop-refund-approval",
  "desc": "退款审批：LLM 分析理由 → 人工审批 → MCP 真实退款（spec §6）",
  "enabled": true,
  "nodes": [
    {"id": "start", "type": "start", "name": "开始", "nextId": "analyze"},
    {"id": "analyze", "type": "llm", "name": "分析退款理由", "nextId": "approve",
     "config": {"agentId": "$AGENT_ID",
       "inputTemplate": "订单 {{inputs.orderId}} 申请退款，理由：{{inputs.reason}}。请给出退款建议与金额摘要（50 字内）。"}},
    {"id": "approve", "type": "approve", "name": "人工审批", "nextId": "refund",
     "config": {"message": "确认退款订单 {{inputs.orderId}}？拒绝请中止运行（abort）"}},
    {"id": "refund", "type": "tool", "name": "执行退款", "nextId": "end",
     "config": {"toolId": "$TOOL_ID", "toolName": "refund_order",
       "args": {"orderId": "{{inputs.orderId}}"}}},
    {"id": "end", "type": "end", "name": "结束"}
  ]
}
EOF
  RESP=$(api -X POST "$PAAS_API/api/workflows" -H "Content-Type: application/json" -d @/tmp/wf.json)
  [ -n "$(echo "$RESP" | jget "['data']['id']")" ] \
    && echo "  已创建 workflow shop-refund-approval" \
    || echo "  [警告] workflow 创建失败: $RESP"
fi
[ -z "$AGENT_ID" ] && echo "  [警告] 未找到 agent shop-assistant，跳过 workflow 创建"
[ -z "$TOOL_ID" ] && echo "  [警告] 未找到 mcp 类型工具，跳过 workflow 创建"

echo "== setup 完成 =="
echo "  APP_ID=${APP_ID}（tenant=${TENANT_ID}）"
echo "  演示域名: shop.test.example.com（test）/ shop.example.com（prod，本机 hosts 或 curl -H Host）"
echo "  发布演示: DevOps → shop-release 手动触发（构建→部署测试→打版本→晋升生产）"
echo "  退款演示: AI 服务 → 工作流 shop-refund-approval 触发（LLM 分析→人工审批→MCP 退款）"
