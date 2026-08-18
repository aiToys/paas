#!/usr/bin/env bash
# 验证平台全资源可用状态（示例完整性检查）。
set -uo pipefail
H="Authorization: Bearer ${PAAS_TOKEN:?请设置 PAAS_TOKEN（API Key，dev 默认 sk-acme-admin）}"; B="${PAAS_BASE:?请设置 PAAS_BASE（core 地址，dev 默认 http://paas.k8s.dd）}"
ok=0; fail=0
check() { # $1=name $2=condition
  if eval "$2"; then echo "  ✅ $1"; ok=$((ok+1)); else echo "  ❌ $1"; fail=$((fail+1)); fi
}

echo "=== 工作负载（K8s Pod ready）==="
check "rec-svc 3/3" "kubectl get deploy wl-rec-svc -n paas -o jsonpath='{.status.readyReplicas}' 2>/dev/null | grep -q 3"
check "cs-api 2/2" "kubectl get deploy wl-cs-api -n paas -o jsonpath='{.status.readyReplicas}' 2>/dev/null | grep -q 2"
check "mcp-server running" "kubectl get deploy wl-mcp-server -n paas -o jsonpath='{.status.readyReplicas}' 2>/dev/null | grep -q 1"
check "traffic-gen running" "kubectl get deploy wl-traffic-gen -n paas -o jsonpath='{.status.readyReplicas}' 2>/dev/null | grep -q 1"
check "traffic-pulse CronJob" "kubectl get cronjob wl-traffic-pulse -n paas 2>/dev/null | grep -q wl-traffic-pulse"
check "故障负载已删" "! kubectl get deploy wl-4ee5e0a5c907 -n paas 2>/dev/null | grep -q wl-4ee5e0a5c907"

echo "=== 数据服务（StatefulSet ready）==="
check "数据服务 ≥6 ready" "[ \$(kubectl get sts -n paas -o jsonpath='{.items[*].status.readyReplicas}' 2>/dev/null | wc -w) -ge 6 ]"

echo "=== AI 资源 ==="
check "KB product-rag 存在" "curl -s -H '$H' $B/api/knowledgebases | grep -q product-rag"
check "KB 文档 indexed" "curl -s -H '$H' $B/api/knowledgebases/kb-1786101237999230309-1/documents | python3 -c 'import sys,json;d=json.load(sys.stdin);exit(0 if any(x.get(\"status\")==\"indexed\" for x in d.get(\"data\",[])) else 1)'"
check "Prompt cs-greeting 存在" "curl -s -H '$H' $B/api/prompts | grep -q cs-greeting"
check "Tool order-tools 存在" "curl -s -H '$H' $B/api/tools | grep -q order-tools"
check "Agent cs-bot 存在" "curl -s -H '$H' $B/api/agents | grep -q cs-bot"
check "MCP 工具可调(test)" "curl -s -X POST -H '$H' $B/api/tools/\$(curl -s -H '$H' $B/api/tools|python3 -c 'import sys,json;d=json.load(sys.stdin);print(next((t[\"id\"] for t in d.get(\"data\",[]) if t.get(\"name\")==\"order-tools\"),\"\"))')/test | grep -q query_order"
check "Agent 推理可用" "curl -s -X POST -H '$H' -H 'Content-Type: application/json' $B/v1/chat/completions -d '{\"model\":\"agent:'\$(curl -s -H '$H' $B/api/agents|python3 -c 'import sys,json;d=json.load(sys.stdin);print(next((a[\"id\"] for a in d.get(\"data\",[]) if a.get(\"name\")==\"cs-bot\"),\"\"))'\",\"messages\":[{\"role\":\"user\",\"content\":\"你好\"}],\"stream\":false}' | grep -q choices"

echo "=== 服务治理 ==="
check "service rec-svc 注册" "curl -s -H '$H' $B/api/services | grep -q rec-svc"
check "route rec-route 存在" "curl -s -H '$H' $B/api/routes | grep -q rec-route"
check "breaker rec-breaker 存在" "curl -s -H '$H' $B/api/breakers | grep -q rec-breaker"

echo "=== 可观测 + 安全 ==="
check "告警规则 ≥3" "[ \$(curl -s -H '$H' $B/api/observability/alert-rules | python3 -c 'import sys,json;d=json.load(sys.stdin);print(len(d.get(\"data\",[])))') -ge 3 ]"
check "密钥 db-rec-password 存在" "curl -s -H '$H' $B/api/security/secrets | grep -q db-rec-password"
check "审计日志有记录" "curl -s -H '$H' $B/api/security/audit-logs | grep -q ."

echo "=== appconfig 注入明文（ListPlain 修复）==="
check "traffic-gen Pod env CORE_URL 明文" "kubectl exec deploy/wl-traffic-gen -n paas -- printenv CORE_URL 2>/dev/null | grep -q paas-core"
check "traffic-gen Pod env API_KEY 明文(非掩码)" "kubectl exec deploy/wl-traffic-gen -n paas -- printenv API_KEY 2>/dev/null | grep -q sk-acme-dev"

echo ""
echo "结果: ✅ $ok 通过, ❌ $fail 失败"
