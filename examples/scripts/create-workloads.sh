#!/usr/bin/env bash
# 创建示例工作负载（mcp-server + traffic-gen Deployment + CronJob）+ 绑定资源。
# 依赖：paas-examples:v1 镜像已推送 + 脚本1 已创建 appconfig（traffic-gen env 注入）。
set -uo pipefail
H="Authorization: Bearer ${PAAS_TOKEN:?请设置 PAAS_TOKEN（API Key，dev 默认 sk-acme-admin）}"
B="${PAAS_BASE:?请设置 PAAS_BASE（core 地址，dev 默认 http://paas.k8s.dd）}"
NODE_IP=$(kubectl get nodes -o wide | awk '!/master|ROLES/{print $6; exit}' | grep -E '^[0-9.]+$')
IMAGE="$NODE_IP:30050/paas/paas-examples:v1"

ENV_TEST=$(curl -s -H "$H" "$B/api/environments" | python3 -c "import sys,json;d=json.load(sys.stdin);es=d.get('data',d if isinstance(d,list) else []);print(next((e['id'] for e in es if e.get('type')=='test'),es[0]['id'] if es else ''))" 2>/dev/null)
echo "ENV_TEST=$ENV_TEST IMAGE=$IMAGE"

echo "=== 1. mcp-server 工作负载（app-cs，AI 工具 MCP server）==="
curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/app-cs/workloads" -d \
  "{\"name\":\"wl-mcp-server\",\"type\":\"service\",\"envId\":\"$ENV_TEST\",\"image\":\"$IMAGE\",\"command\":\"mcp-server\",\"replicas\":1,\"port\":80,\"containerPort\":8080}" \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  workload:',d.get('id'),d.get('name'))" 2>/dev/null

echo "=== 2. traffic-gen 常驻 Deployment（app-rec，微服务链+AI Agent 记忆）==="
curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/app-rec/workloads" -d \
  "{\"name\":\"wl-traffic-gen\",\"type\":\"service\",\"envId\":\"$ENV_TEST\",\"image\":\"$IMAGE\",\"command\":\"traffic-gen\",\"replicas\":1}" \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  workload:',d.get('id'),d.get('name'))" 2>/dev/null

echo "=== 3. traffic-gen CronJob（app-rec，每5分钟脉冲调用微服务链）==="
curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/app-rec/workloads" -d \
  "{\"name\":\"wl-traffic-pulse\",\"type\":\"cronjob\",\"envId\":\"$ENV_TEST\",\"image\":\"$IMAGE\",\"command\":\"traffic-gen once\",\"schedule\":\"*/5 * * * *\"}" \
  | python3 -c "import sys,json;d=json.load(sys.stdin).get('data',{});print('  workload:',d.get('id'),d.get('name'))" 2>/dev/null

echo "=== 4. 绑定数据服务到 app-rec（演示资源绑定，若未绑）==="
# 查现有数据服务，绑 db/cache/mq 到 app-rec
for ds_kind in db cache mq; do
  DS_ID=$(curl -s -H "$H" "$B/api/dataservices?kind=$ds_kind" | python3 -c "import sys,json;d=json.load(sys.stdin);ds=d.get('data',d if isinstance(d,list) else []);print(ds[0]['id'] if ds else '')" 2>/dev/null)
  if [ -n "$DS_ID" ]; then
    curl -s -o /dev/null -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/app-rec/bindings" -d \
      "{\"type\":\"$ds_kind\",\"name\":\"$DS_ID\"}" && echo "  bind $ds_kind: $DS_ID"
  fi
done

echo "=== 5. 等 reconciler 落地 + 验证 ==="
sleep 15
echo "--- mcp-server Pod ---"
kubectl get pods -n paas -l paas.aitoys/workload=wl-mcp-server 2>/dev/null | tail -2
echo "--- traffic-gen Pod ---"
kubectl get pods -n paas -l paas.aitoys/workload=wl-traffic-gen 2>/dev/null | tail -2
echo "--- traffic-pulse CronJob ---"
kubectl get cronjob wl-traffic-pulse -n paas 2>/dev/null | tail -2

echo "=== 6. 测试 MCP 工具（mcp-server 就绪后）==="
sleep 5
TOOL_ID=$(curl -s -H "$H" "$B/api/tools" | python3 -c "import sys,json;d=json.load(sys.stdin);ts=d.get('data',d if isinstance(d,list) else []);print(next((t['id'] for t in ts if t.get('name')=='order-tools'),''))" 2>/dev/null)
if [ -n "$TOOL_ID" ]; then
  curl -s -X POST -H "$H" "$B/api/tools/$TOOL_ID/test" | python3 -c "import sys,json;d=json.load(sys.stdin);print('  MCP test:',d.get('data',d))" 2>/dev/null | head -c 200; echo
fi
