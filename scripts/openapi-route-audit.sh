#!/usr/bin/env bash
# openapi-route-audit.sh —— OpenAPI 登记端点 vs mux 实际路由 runtime 对账（第五审计原则工具化）。
# 原理：route registry 同源驱动 mux 与 spec，漂移只能来自绕过 registry 的手工路由。
# 探测全部 method+path，404 且 body 含 "page not found"（Go mux 默认裸 404）= 真漏注册；
# 404 但 body 是 {"error":...} = 业务 not found（路由存在，参数路径字面量探测的预期形态）。
# 用法：PAAS_API=http://paas.k8s.dd PAAS_TOKEN=sk-acme-admin ./scripts/openapi-route-audit.sh
# 退出码：漏注册数非零则非零（可挂 CI）。
set -euo pipefail
PAAS_API="${PAAS_API:-http://paas.k8s.dd}"
PAAS_TOKEN="${PAAS_TOKEN:-sk-acme-admin}"

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT

curl -sS "$PAAS_API/openapi.json" | python3 -c "
import sys,json
for p,ops in sorted(json.load(sys.stdin)['paths'].items()):
    for m in ops: print(m.upper(), p)" > "$TMP"

echo "OpenAPI 端点总数: $(wc -l < "$TMP" | tr -d ' ')"
python3 - "$TMP" "$PAAS_API" "$PAAS_TOKEN" <<'EOF'
import subprocess,sys
paths_file, api, token = sys.argv[1], sys.argv[2], sys.argv[3]
miss = []
for line in open(paths_file):
    m, p = line.split()
    r = subprocess.run(['curl','-sS','-w','\n%{http_code}','-X',m,
        '-H',f'Authorization: Bearer {token}','-H','Content-Type: application/json',
        '-d','{}', api+p], capture_output=True, text=True, timeout=20)
    body, _, code = r.stdout.rpartition('\n')
    if code == '404' and 'page not found' in body:
        miss.append(f'{m} {p}')
print(f'真漏注册(mux 404): {len(miss)}')
for x in miss: print('  MISS:', x)
sys.exit(1 if miss else 0)
EOF
