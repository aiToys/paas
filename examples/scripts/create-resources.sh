#!/usr/bin/env bash
# 创建平台示例资源（覆盖 AI/治理/可观测/安全/计费全模块）。
# 幂等：已存在的资源跳过（按 name 查重）。依赖：core 已部署（v0.1.4+，含 appconfig ListPlain 修复）。
set -uo pipefail
H="Authorization: Bearer ${PAAS_TOKEN:?请设置 PAAS_TOKEN（API Key，dev 默认 sk-acme-admin）}"
B="${PAAS_BASE:?请设置 PAAS_BASE（core 地址，dev 默认 http://paas.k8s.dd）}"

pp() { python3 -c "import sys,json; d=json.load(sys.stdin); print(json.dumps(d.get('data',d),ensure_ascii=False))" 2>/dev/null; }

echo "=== 0. 删除故障负载 wl-4ee5e0a5c907 ==="
curl -s -o /dev/null -w "  delete: %{http_code}\n" -X DELETE -H "$H" "$B/api/workloads/wl-4ee5e0a5c907"

echo "=== 1. 查环境（取 acme test/prod envId）==="
ENV_TEST=$(curl -s -H "$H" "$B/api/environments" | python3 -c "import sys,json;d=json.load(sys.stdin);es=d.get('data',d if isinstance(d,list) else []);print(next((e['id'] for e in es if e.get('type')=='test'),es[0]['id'] if es else ''))" 2>/dev/null)
ENV_PROD=$(curl -s -H "$H" "$B/api/environments" | python3 -c "import sys,json;d=json.load(sys.stdin);es=d.get('data',d if isinstance(d,list) else []);print(next((e['id'] for e in es if e.get('type')=='prod'),''))" 2>/dev/null)
echo "  test=$ENV_TEST prod=$ENV_PROD"

echo "=== 2. 创建 Prompt cs-greeting（若不存在）==="
curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/prompts" -d \
  '{"name":"cs-greeting","template":"你是 {{.brand}} 智能客服。规则：\n1. 仅回答 {{.brand}} 产品相关问题\n2. 涉及订单/退款时调用工具查询真实状态，不编造\n3. 不确定时诚实告知\n4. 保持友好简洁\n\n用户问题：{{.question}}","variables":["brand","question"]}' \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  prompt:',d.get('id'),'v'+str(d.get('version')),'active:',d.get('active'))" 2>/dev/null

echo "=== 3. 创建 MCP 工具 order-tools（指向 mcp-server）==="
TOOL_ID=$(curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/tools" -d \
  '{"name":"order-tools","description":"订单查询与退款工具（query_order/refund_order）","type":"mcp","config":{"serverURL":"http://wl-mcp-server.paas.svc.cluster.local","apiKey":""},"enabled":true}' \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print(d.get('id',''))" 2>/dev/null)
echo "  tool: $TOOL_ID"

echo "=== 4. 创建 Agent cs-bot（绑 KB+工具+prompt+记忆）==="
KB_ID="kb-1786101237999230309-1"
AGENT_ID=$(curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/agents" -d \
  "{\"name\":\"cs-bot\",\"description\":\"智能客服 Agent（RAG+工具+记忆）：查订单/退款/产品咨询\",\"model\":\"glm-5.2\",\"promptRef\":\"cs-greeting\",\"tools\":[\"$TOOL_ID\"],\"knowledgeBases\":[\"$KB_ID\"],\"maxSteps\":5,\"enabled\":true}" \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print(d.get('id',''))" 2>/dev/null)
echo "  agent: $AGENT_ID (virtual model: agent:$AGENT_ID)"

echo "=== 5. 创建 appconfig（traffic-gen env，注入工作负载）==="
APP_ID="app-rec"
for kv in "CORE_URL:http://paas-core.paas.svc.cluster.local:8080" "API_KEY:sk-acme-dev" "AGENT_MODEL:agent:$AGENT_ID" "REC_SVC_URL:http://wl-rec-svc.paas.svc.cluster.local" "MICRO_INTERVAL:600" "AGENT_INTERVAL:3600"; do
  K="${kv%%:*}"; V="${kv#*:}"
  TYPE="env"; [ "$K" = "API_KEY" ] && TYPE="secret"
  curl -s -o /dev/null -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP_ID/configs" -d \
    "{\"key\":\"$K\",\"value\":\"$V\",\"type\":\"$TYPE\",\"envId\":\"$ENV_TEST\"}" && echo "  cfg: $K=$V ($TYPE)"
done

echo "=== 6. 服务治理：注册 rec-svc / cs-api 服务 ==="
for svc in '{"name":"rec-svc","appId":"app-rec","envId":"'"$ENV_TEST"'","protocol":"http","port":80,"desc":"推荐服务"}' \
           '{"name":"cs-api","appId":"app-cs","envId":"'"$ENV_TEST"'","protocol":"http","port":80,"desc":"客服API"}'; do
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/services" -d "$svc" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  service:',d.get('id'),d.get('name'))" 2>/dev/null
done

echo "=== 7. 服务治理：API 网关路由 ==="
curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/routes" -d \
  '{"name":"rec-route","path":"/api/rec/*","serviceId":"rec-svc","methods":["GET","POST"],"stripPath":true,"enabled":true}' \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  route:',d.get('id'),d.get('path'))" 2>/dev/null

echo "=== 8. 服务治理：熔断器 ==="
curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/breakers" -d \
  '{"name":"rec-breaker","serviceId":"rec-svc","strategy":"error_rate","threshold":50,"minRequests":10,"windowSecs":60,"enabled":true}' \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  breaker:',d.get('id'),d.get('name'))" 2>/dev/null

echo "=== 9. 告警规则 ==="
for rule in '{"name":"rec-cpu-high","metricName":"cpu","targetType":"app","targetId":"app-rec","operator":">","threshold":80,"severity":"warning","enabled":true}' \
            '{"name":"rec-rps-drop","metricName":"rps","targetType":"app","targetId":"app-rec","operator":"<","threshold":10,"severity":"warning","enabled":true}' \
            '{"name":"rec-latency-high","metricName":"latency","targetType":"app","targetId":"app-rec","operator":">","threshold":500,"severity":"critical","enabled":true}'; do
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/observability/alert-rules" -d "$rule" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  rule:',d.get('id'),d.get('name'))" 2>/dev/null
done

echo "=== 10. 安全密钥 ==="
curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/security/secrets" -d \
  '{"name":"db-rec-password","type":"secret","value":"paas-rec-db-2026","desc":"推荐服务数据库密码示例"}' \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  secret:',d.get('id'),d.get('name'))" 2>/dev/null

echo "=== 11. 评估用例（Agent cs-bot）==="
for tc in '{"agentId":"'"$AGENT_ID"'","name":"订单查询","input":"查一下 ORD-1001 订单状态","expected":"shipped","matchType":"contains"}' \
          '{"agentId":"'"$AGENT_ID"'","name":"退款流程","input":"ORD-1002 申请退款，原因商品损坏","expected":"退款","matchType":"contains"}'; do
  curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/agent-evals" -d "$tc" \
    | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  eval:',d.get('id'),d.get('name'))" 2>/dev/null
done

echo "=== 完成 ==="
echo "AGENT_ID=$AGENT_ID"
echo "TOOL_ID=$TOOL_ID"
echo "ENV_TEST=$ENV_TEST"
