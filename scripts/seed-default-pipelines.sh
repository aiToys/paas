#!/usr/bin/env bash
# 为已有 app 补建默认流水线绑定（tpl-ci/tpl-cd）。
# 新建 app 时 OnAppCreate 自动建（零操作）；此脚本补已有 app（OnAppCreate 仅触发于新建）。
# 幂等：已有同名 binding 跳过。依赖：core 已部署 + 平台预置模板已 seed。
set -uo pipefail
H="Authorization: Bearer sk-acme-admin"
B="${PAAS_URL:-http://paas.k8s.dd}"

echo "=== 为已有 app 补建默认流水线绑定（tpl-ci/tpl-cd）==="
APPS=$(curl -s -H "$H" "$B/api/applications" | python3 -c "import sys,json; d=json.load(sys.stdin); [print(a['id']) for a in d.get('data',[])]" 2>/dev/null)
for APP in $APPS; do
  for TPL in tpl-ci tpl-cd; do
    KIND="${TPL#tpl-}"
    NAME="$APP-$KIND"
    EXISTS=$(curl -s -H "$H" "$B/api/applications/$APP/pipelines" | python3 -c "import sys,json; d=json.load(sys.stdin); print(any(p['name']=='$NAME' for p in d.get('data',[])))" 2>/dev/null)
    if [ "$EXISTS" = "False" ]; then
      curl -s -X POST -H "$H" -H "Content-Type: application/json" "$B/api/applications/$APP/pipelines" \
        -d "{\"name\":\"$NAME\",\"kind\":\"$KIND\",\"templateId\":\"$TPL\"}" >/dev/null 2>&1 \
        && echo "  $APP: created $NAME" \
        || echo "  $APP: $NAME 创建失败"
    fi
  done
done
echo "=== 补建完成 ==="
