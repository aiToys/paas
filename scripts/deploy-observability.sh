#!/usr/bin/env bash
# 可观测栈部署（唯一入口）—— Prometheus + Loki/Promtail + Grafana + Jaeger + Ingress。
#
# 【为什么必须走这个脚本，勿绕过】
# 在本脚本之前，deploy/observability/*.yaml **没有任何部署路径**：deploy-k8s.sh 只部署
# deploy/charts/paas，这几个文件全靠手工 helm upgrade。后果（2026-09-24 排查所得）：
#   1) 两次 kubectl set image 带外 patch（09-05 / 09-24）没写回 values，被 helm 静默打回
#      → KSM ImagePullBackOff。根因是「应用前没有 diff 渲染结果 vs 线上实际」这一步。
#   2) values 里一个失效死键潜伏无告警：chart 29.x 的子 chart 键是 kube-state-metrics.*，
#      而文件里写的是 kubeStateMetrics.*（chart 的 values.schema.json 顶层 additionalProperties
#      未设 → 未知键静默放行，helm 不报任何错）。
#   3) 冗余抓取 job 与 chart 内置 job 重复 → 同名指标两份时序，而平台 PromQL 不带 job 过滤
#      → sum() 翻倍（cAdvisor 一例、应用 RED 一例）。
# 三者的共同点是「改了没人验证」。本脚本把「渲染 → 预检 diff → 应用(--wait) → 部署后断言」
# 收成唯一路径：想改这套东西就必须经过它，检查是 by construction 的，不靠纪律记得跑。
#
# 用法：
#   ./scripts/deploy-observability.sh            # 部署（预检发现漂移会要求确认）
#   ./scripts/deploy-observability.sh --check    # 只渲染 + 预检 diff + 目标断言，不写集群
#   ./scripts/deploy-observability.sh --yes      # 跳过漂移确认（CI/无人值守）
#
# 【换集群：只改下面的环境变量，values 文件不动】
#   NS_OBS=monitoring NS_PAAS=paas-system STORAGE_CLASS=gp3 \
#   DOMAIN_SUFFIX=example.com INGRESS_CLASS=nginx MIRROR=none \
#   GRAFANA_IMAGE_REGISTRY=docker.io GRAFANA_IMAGE_REPOSITORY=grafana/grafana \
#   LOKI_IMAGE_REPO=docker.io/grafana/loki \
#   PROMTAIL_IMAGE_REGISTRY=docker.io PROMTAIL_IMAGE_REPO=grafana/promtail \
#   GRAFANA_ADMIN_PASSWORD='<强口令>' ./scripts/deploy-observability.sh
#
#   镜像变量为何有的拆两段：chart 拼法不同。grafana chart 拼 image.registry + "/" +
#   image.repository（默认 registry=docker.io），promtail 子 chart 同款；而 loki 子 chart
#   直接用 image.repository 整串。拆错的后果是前缀叠加（docker.io/hub.wang.dd:5000/...），
#   helm 不报错、Pod 直接 ImagePullBackOff。
#
#   MIRROR=daocloud（默认）→ 镜像走国内中转站（本网段节点可达；registry.k8s.io 会把 manifest
#     请求重定向到 *.pkg.dev，本网络不可达）。MIRROR=none → 上游 docker.io/quay.io/registry.k8s.io。
#   OBS_CHART_DIR=/path/to/charts → 从离线包目录取 <chart>-<ver>.tgz（airsync 私有化交付场景）。
#   OBS_MANIFEST_DIR=/path/to/observability → 清单目录（airsync 离线包内为 observability/）。
#   --chart-versions → 机器可读输出三个 chart 的「仓库/名 版本 仓库URL」（airsync bundle 用）。
#
# 前置：
#   - kubectl / helm / envsubst(gettext) / jq 在 PATH；kubectl 已配好目标集群
#   - 集群有 STORAGE_CLASS 指定的 StorageClass（dev 用 local-path：
#     kubectl apply -f deploy/k8s/local-path.yaml）
#   - 三个 chart 可取：helm cache / 仓库 / OBS_CHART_DIR（脚本会预先解析，缺哪个报哪个）
#
# 注意：本脚本只应用 observability 栈。控制面（paas chart）仍走 ./scripts/deploy-k8s.sh。
set -euo pipefail

# ────────────────────────── 参数（默认值 = 当前 dev 集群） ──────────────────────────
NS_OBS="${NS_OBS:-observability}"          # 可观测栈 namespace
NS_PAAS="${NS_PAAS:-paas}"                 # 控制面 namespace（paas-core 抓取 job）
STORAGE_CLASS="${STORAGE_CLASS:-local-path}"
DOMAIN_SUFFIX="${DOMAIN_SUFFIX:-k8s.dd}"   # Ingress 通配域名后缀
INGRESS_CLASS="${INGRESS_CLASS:-hermes}"
GRAFANA_ADMIN_PASSWORD="${GRAFANA_ADMIN_PASSWORD:-paas-admin}"   # dev 演示凭证，生产必须覆盖
GRAFANA_IMAGE_REGISTRY="${GRAFANA_IMAGE_REGISTRY:-hub.wang.dd:5000}"
GRAFANA_IMAGE_REPOSITORY="${GRAFANA_IMAGE_REPOSITORY:-observability/grafana}"
LOKI_IMAGE_REPO="${LOKI_IMAGE_REPO:-hub.wang.dd:5000/observability/loki}"
PROMTAIL_IMAGE_REGISTRY="${PROMTAIL_IMAGE_REGISTRY:-hub.wang.dd:5000}"
PROMTAIL_IMAGE_REPO="${PROMTAIL_IMAGE_REPO:-observability/promtail}"
MIRROR="${MIRROR:-daocloud}"               # daocloud | none
OBS_CHART_DIR="${OBS_CHART_DIR:-}"         # 离线 chart 目录（可选）
# 清单目录（values + 原始 manifest）。airsync 离线包把 deploy/observability/* 放到包内
# observability/ 并以 OBS_MANIFEST_DIR 指向它——离线目标机没有仓库，只有 tarball。
OBS_MANIFEST_DIR="${OBS_MANIFEST_DIR:-deploy/observability}"

PROM_VER=29.24.0
LOKI_VER=2.10.3
GRAFANA_VER=10.5.15
# chart 三元组真源（名/版本/仓库 URL）：--chart-versions 供 airsync bundle 查询后 helm pull，
# 避免版本号与仓库地址在 Go 侧再写一份而漂移（改这里即同时改打包与部署）。
OBS_CHARTS=(
  "prometheus-community/prometheus ${PROM_VER} https://prometheus-community.github.io/helm-charts"
  "grafana/loki-stack ${LOKI_VER} https://grafana.github.io/helm-charts"
  "grafana/grafana ${GRAFANA_VER} https://grafana.github.io/helm-charts"
)

CHECK_ONLY=0
ASSUME_YES=0
for arg in "$@"; do
  case "$arg" in
    --check)   CHECK_ONLY=1 ;;
    --yes|-y)  ASSUME_YES=1 ;;
    # 机器可读输出：每行「仓库/名 版本 仓库URL」。airsync bundle 据此 helm pull 三个 chart。
    --chart-versions) printf '%s\n' "${OBS_CHARTS[@]}"; exit 0 ;;
    -h|--help) sed -n '2,50p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "✗ 未知参数：${arg}（-h 看用法）"; exit 1 ;;
  esac
done

# ────────────────────────── 前置检查 ──────────────────────────
for c in kubectl helm envsubst jq; do
  if ! command -v "$c" >/dev/null 2>&1; then
    echo "✗ 缺少命令：$c"
    if [ "$c" = envsubst ]; then echo "  macOS: brew install gettext && brew link --force gettext"; fi
    exit 1
  fi
done
kubectl cluster-info >/dev/null 2>&1 || { echo "✗ kubectl 连不上集群（检查 kubeconfig）"; exit 1; }

case "$MIRROR" in
  daocloud) MIRROR_DOCKER=docker.m.daocloud.io; MIRROR_QUAY=quay.m.daocloud.io; MIRROR_K8S=k8s.m.daocloud.io ;;
  none)     MIRROR_DOCKER=docker.io;            MIRROR_QUAY=quay.io;             MIRROR_K8S=registry.k8s.io ;;
  *) echo "✗ MIRROR 只支持 daocloud | none（当前：${MIRROR}）"; exit 1 ;;
esac

# envsubst 用**显式变量表**：只替换列出的 ${VAR}，其它 $ 原样保留。
# 必须如此——grafana-dashboards.yaml 含 Grafana 模板变量 $namespace/$__all，
# prometheus-values.yaml 含 relabel 正则回引 $1:$2，裸 envsubst 会把它们打成空串。
export NS_OBS NS_PAAS STORAGE_CLASS DOMAIN_SUFFIX INGRESS_CLASS GRAFANA_ADMIN_PASSWORD \
       GRAFANA_IMAGE_REGISTRY GRAFANA_IMAGE_REPOSITORY \
       LOKI_IMAGE_REPO PROMTAIL_IMAGE_REGISTRY PROMTAIL_IMAGE_REPO \
       MIRROR_DOCKER MIRROR_QUAY MIRROR_K8S
VARS='${NS_OBS} ${NS_PAAS} ${STORAGE_CLASS} ${DOMAIN_SUFFIX} ${INGRESS_CLASS} ${GRAFANA_ADMIN_PASSWORD} ${GRAFANA_IMAGE_REGISTRY} ${GRAFANA_IMAGE_REPOSITORY} ${LOKI_IMAGE_REPO} ${PROMTAIL_IMAGE_REGISTRY} ${PROMTAIL_IMAGE_REPO} ${MIRROR_DOCKER} ${MIRROR_QUAY} ${MIRROR_K8S}'

HELM_CACHE="$(helm env HELM_REPOSITORY_CACHE 2>/dev/null || echo "$HOME/.cache/helm/repository")"

echo "╔══════════════════════════════════════════════════════════╗"
echo "║  可观测栈部署（observability stack）"
echo "╚══════════════════════════════════════════════════════════╝"
echo "  namespace      : ${NS_OBS}   （控制面 ns: ${NS_PAAS}）"
echo "  StorageClass   : ${STORAGE_CLASS}"
echo "  Ingress        : *.${DOMAIN_SUFFIX}  class=${INGRESS_CLASS}"
echo "  镜像           : MIRROR=${MIRROR}（docker=${MIRROR_DOCKER} quay=${MIRROR_QUAY} k8s=${MIRROR_K8S}）"
echo "  grafana/loki   : ${GRAFANA_IMAGE_REGISTRY}/${GRAFANA_IMAGE_REPOSITORY} / ${LOKI_IMAGE_REPO}"
echo "  模式           : $([ "$CHECK_ONLY" = 1 ] && echo '仅预检（不写集群）' || echo '部署')"

# ────────────────────────── 1/5 chart 解析 + 渲染 ──────────────────────────
# chart 解析顺序：OBS_CHART_DIR（离线包）→ helm 仓库缓存 → 仓库引用 → OCI 引用。
# 先全部解析成功再动手：任一缺失就 fail fast，避免"装了一半"。
resolve_chart() { # $1=chart 名 $2=版本 $3=仓库引用 $4=OCI 引用；结果写入全局 CHART_ARGS
  if [ -n "$OBS_CHART_DIR" ] && [ -f "$OBS_CHART_DIR/$1-$2.tgz" ]; then
    CHART_ARGS="$OBS_CHART_DIR/$1-$2.tgz"; return 0
  fi
  if [ -f "$HELM_CACHE/$1-$2.tgz" ]; then
    CHART_ARGS="$HELM_CACHE/$1-$2.tgz"; return 0
  fi
  if helm show chart "$3" --version "$2" >/dev/null 2>&1; then
    CHART_ARGS="$3 --version $2"; return 0
  fi
  if [ -n "$4" ] && helm show chart "$4" --version "$2" >/dev/null 2>&1; then
    CHART_ARGS="$4 --version $2"; return 0
  fi
  return 1
}

echo ""
echo "▶ 1/5 解析 chart + 渲染..."
CHART_ARGS=""
resolve_chart prometheus "$PROM_VER" prometheus-community/prometheus oci://ghcr.io/prometheus-community/charts/prometheus \
  || { echo "✗ 取不到 prometheus chart ${PROM_VER}。离线环境用 OBS_CHART_DIR 指向含 prometheus-${PROM_VER}.tgz 的目录，"
       echo "  或联网机 helm pull prometheus-community/prometheus --version $PROM_VER 后拷入 $HELM_CACHE"; exit 1; }
CHART_PROM="$CHART_ARGS"; echo "  prometheus : $CHART_PROM"

resolve_chart loki-stack "$LOKI_VER" grafana/loki-stack "" \
  || { echo "✗ 取不到 loki-stack chart ${LOKI_VER}（同上：OBS_CHART_DIR 或 helm pull grafana/loki-stack）"; exit 1; }
CHART_LOKI="$CHART_ARGS"; echo "  loki-stack : $CHART_LOKI"

resolve_chart grafana "$GRAFANA_VER" grafana/grafana oci://ghcr.io/grafana/helm-charts/grafana \
  || { echo "✗ 取不到 grafana chart ${GRAFANA_VER}（同上：OBS_CHART_DIR 或 helm pull grafana/grafana）"; exit 1; }
CHART_GRAFANA="$CHART_ARGS"; echo "  grafana    : $CHART_GRAFANA"

WORK="$(mktemp -d -t obs-deploy.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT

# 占位符清单：渲染后逐文件校验无残留（防 envsubst 变量表漏项 → 把 ${X} 原样写进集群）。
PLACEHOLDERS='\$\{(NS_OBS|NS_PAAS|STORAGE_CLASS|DOMAIN_SUFFIX|INGRESS_CLASS|GRAFANA_ADMIN_PASSWORD|GRAFANA_IMAGE_REGISTRY|GRAFANA_IMAGE_REPOSITORY|LOKI_IMAGE_REPO|PROMTAIL_IMAGE_REGISTRY|PROMTAIL_IMAGE_REPO|MIRROR_DOCKER|MIRROR_QUAY|MIRROR_K8S)\}'
RAW_MANIFESTS="jaeger.yaml ingress.yaml grafana-dashboard-providers.yaml grafana-dashboards.yaml"
VALUES_FILES="prometheus-values.yaml grafana-values.yaml loki-stack-values.yaml"

for f in $VALUES_FILES $RAW_MANIFESTS; do
  envsubst "$VARS" < "$OBS_MANIFEST_DIR/$f" > "$WORK/$f"
  if grep -qE "$PLACEHOLDERS" "$WORK/$f"; then
    echo "✗ $OBS_MANIFEST_DIR/$f 仍有未替换占位符：$(grep -oE "$PLACEHOLDERS" "$WORK/$f" | sort -u | tr '\n' ' ')"
    echo "  → 该占位符未登记进脚本 VARS 变量表"
    exit 1
  fi
done

# shellcheck disable=SC2086  # CHART_PROM/CHART_LOKI/CHART_GRAFANA 需按空格拆成「引用 + --version」
helm template prom    $CHART_PROM    -n "$NS_OBS" -f "$WORK/prometheus-values.yaml"  > "$WORK/rendered-prom.yaml"
helm template loki    $CHART_LOKI    -n "$NS_OBS" -f "$WORK/loki-stack-values.yaml"  > "$WORK/rendered-loki.yaml"
helm template grafana $CHART_GRAFANA -n "$NS_OBS" -f "$WORK/grafana-values.yaml"     > "$WORK/rendered-grafana.yaml"
echo "  ✓ 渲染完成（prom/loki/grafana + 4 个原始 manifest）"

# 渲染侧对象结构化（真实解析，不用正则抓 YAML）：kubectl create --dry-run=client 逐文档转 JSON。
# 逐文件解析而非 cat 拼接——原始 manifest 首个文档不带 `---` 头，拼接会让相邻文档粘连
# （实测报错：ConfigMap 里出现未知 spec 字段）。每次调用输出一串 JSON 对象，追加即合法流。
: > "$WORK/rendered.json"
for f in "$WORK/rendered-prom.yaml" "$WORK/rendered-loki.yaml" "$WORK/rendered-grafana.yaml" \
         "$WORK/jaeger.yaml" "$WORK/ingress.yaml" \
         "$WORK/grafana-dashboard-providers.yaml" "$WORK/grafana-dashboards.yaml"; do
  if ! kubectl create --dry-run=client -f "$f" -o json >> "$WORK/rendered.json" 2>"$WORK/parse.err"; then
    echo "✗ 渲染结果解析失败：$(basename "$f")"
    head -3 "$WORK/parse.err" | sed 's/^/    /'
    exit 1
  fi
done

# ────────────────────────── 2/5 预检 diff：渲染 vs 线上 ──────────────────────────
# 这是本脚本存在的核心理由：helm upgrade 会静默把带外 kubectl patch 打回，
# 只有「应用前先比一次」能在破坏发生前发现它（2026-09-24 的 KSM ImagePullBackOff 就是缺这一步）。
echo ""
echo "▶ 2/5 预检 diff（渲染结果 vs 线上实际）..."

DRIFT=0
cm_diff=""
img_diff=""
ann_diff=""
cm_new=""

# helm hook 资源（注解 helm.sh/hook，如 grafana-test/loki-stack-test）由 helm test 触发时创建，
# helm install/upgrade 根本不建它们。若纳入比对，每次都会把「线上没有」误报成漂移/将新建。
JQ_NO_HOOK='select((.metadata.annotations // {})["helm.sh/hook"] == null)'

# ConfigMap 内容指纹：先归一化再哈希。归一化 = 去每行行尾空白 + 去末尾空行。
# 不归一化会误报：helm 渲染的 prometheus.yml 比线上多一个末尾空行，sha 不同但语义完全相同。
cm_fingerprint() { # stdin=单个 k8s 对象 JSON；stdout=12 位 sha
  jq -c '
    def dropEmptyEnd: if length > 0 and .[-1] == "" then (.[:-1] | dropEmptyEnd) else . end;
    .data | with_entries(.value |= (split("\n") | map(sub("[ \t]+$"; "")) | dropEmptyEnd | join("\n")))
  ' | shasum | cut -c1-12
}

# 全新集群无「线上」可比：跳过比对（否则所有对象都会被当成漂移）。
if ! kubectl get ns "$NS_OBS" >/dev/null 2>&1; then
  echo "  · namespace $NS_OBS 不存在 → 全新安装，跳过漂移比对"
else
  # A. 工作负载镜像 —— 抓 kubectl set image / 手工改镜像这类带外漂移。
  live_imgs="$(kubectl -n "$NS_OBS" get deploy,sts,ds -o json 2>/dev/null \
    | jq -r '.items[] | "\(.kind)/\(.metadata.name)  \([.spec.template.spec.containers[].image]|join(" "))"' | sort || true)"
  rendered_imgs="$(jq -s -r '.[] | select(.kind=="Deployment" or .kind=="StatefulSet" or .kind=="DaemonSet")
    | '"$JQ_NO_HOOK"' | "\(.kind)/\(.metadata.name)  \([.spec.template.spec.containers[].image]|join(" "))"' "$WORK/rendered.json" | sort)"
  img_diff="$(diff <(printf '%s\n' "$live_imgs") <(printf '%s\n' "$rendered_imgs") || true)"

  # B. Pod template 注解 —— 抓「values 改了但只影响 pod template 注解」的漂移。
  # 首版只比镜像 + ConfigMap，导致 loki 的 podAnnotations 修正（http-metrics → 3100）被误判「无漂移」（2026-09-24 实测）。
  # 排除两类噪声键，否则会永久误报：
  #   · checksum*                —— 由 ConfigMap 内容派生，ConfigMap 变更已由 C 覆盖
  #   · kubectl.kubernetes.io/*  —— 运维动作（rollout restart 打 restartedAt），helm upgrade 不会回收，
  #                                 线上实测 prom-prometheus-server / loki-promtail 各残留一个
  JQ_POD_ANN='[.spec.template.metadata.annotations // {} | to_entries[] | select((.key | test("checksum|^kubectl\\.kubernetes\\.io/")) | not) | .key + "=" + .value] | sort | join(",")'
  live_ann="$(kubectl -n "$NS_OBS" get deploy,sts,ds -o json 2>/dev/null \
    | jq -r '.items[] | .kind + "/" + .metadata.name + "  [" + ('"$JQ_POD_ANN"') + "]"' | sort || true)"
  rendered_ann="$(jq -s -r '.[] | select(.kind=="Deployment" or .kind=="StatefulSet" or .kind=="DaemonSet")
    | '"$JQ_NO_HOOK"' | .kind + "/" + .metadata.name + "  [" + ('"$JQ_POD_ANN"') + "]"' "$WORK/rendered.json" | sort)"
  ann_diff="$(diff <(printf '%s\n' "$live_ann") <(printf '%s\n' "$rendered_ann") || true)"

  # C. ConfigMap 内容 —— 抓手工编辑 prometheus.yml 等带外配置改动。
  for cm in $(jq -s -r ".[] | $JQ_NO_HOOK | select(.kind==\"ConfigMap\") | .metadata.name" "$WORK/rendered.json" | sort -u); do
    r="$(jq -s -c --arg n "$cm" ".[] | select(.kind==\"ConfigMap\" and .metadata.name==\$n) | ." "$WORK/rendered.json" | cm_fingerprint)"
    # 显式判存在，不用「管道拿到空值」推断：set -o pipefail 下 kubectl 的 NotFound 非零退出
    # 会传染整条管道，让 set -e 把脚本误杀在比对中途（实测踩到）。
    if ! kubectl -n "$NS_OBS" get cm "$cm" -o json > "$WORK/cm-live.json" 2>/dev/null; then
      cm_new="${cm_new}  $cm
"
      continue
    fi
    l="$(cm_fingerprint < "$WORK/cm-live.json")"
    if [ "$r" != "$l" ]; then
      cm_diff="${cm_diff}  $cm  (线上 $l → 渲染 $r)
"
    fi
  done
fi

if [ -n "$img_diff" ]; then
  DRIFT=1
  echo "  ⚠ 工作负载镜像有差异（左=线上，右=将应用）:"
  printf '%s\n' "$img_diff" | sed 's/^/    /'
fi
if [ -n "$ann_diff" ]; then
  DRIFT=1
  echo "  ⚠ Pod template 注解有差异（左=线上，右=将应用）:"
  printf '%s\n' "$ann_diff" | sed 's/^/    /'
fi
if [ -n "$cm_diff" ]; then
  DRIFT=1
  echo "  ⚠ ConfigMap 内容有差异（线上 → 渲染）—— helm 会覆盖线上值:"
  printf '%s' "$cm_diff" | sed 's/^/    /'
fi
if [ -n "$cm_new" ]; then
  echo "  · 将新建的 ConfigMap（非漂移）:"
  printf '%s' "$cm_new" | sed 's/^/    /'
fi
if [ "$DRIFT" = 0 ]; then echo "  ✓ 无漂移：线上与 values 声明一致"; fi

if [ "$DRIFT" = 1 ] && [ "$CHECK_ONLY" = 0 ] && [ "$ASSUME_YES" = 0 ]; then
  echo ""
  echo "  上面每一项都会被本次应用**覆盖**。若其中有你手工做过的改动，请先写回 values 文件。"
  printf "  继续应用？[y/N] "
  read -r ans
  case "$ans" in y|Y|yes) ;; *) echo "已取消（未改动集群）"; exit 0 ;; esac
fi

# ────────────────────────── 3/5 应用 ──────────────────────────
if [ "$CHECK_ONLY" = 1 ]; then
  echo ""
  echo "▶ 3/5 --check 模式：跳过应用"
else
  echo ""
  echo "▶ 3/5 应用（kubectl apply 原始清单 + helm upgrade --install --wait）..."
  kubectl get ns "$NS_OBS" >/dev/null 2>&1 || kubectl create ns "$NS_OBS"
  # 原始清单必须先于 helm upgrade apply：grafana values 挂载 grafana-dashboards.yaml 的
  # 面板 CM（extraConfigmapMounts），CM 不存在则新 Pod FailedMount、--wait 超时、脚本
  # set -e 中断，CM 永远建不出来（2026-09-25 踩坑：新增 infra 面板 CM 时触发，helm 记录
  # 滞留在 failed、线上实际已滚动）。CM 先落集群，grafana 升级即一次到位。
  kubectl apply -f "$WORK/jaeger.yaml" -f "$WORK/ingress.yaml" \
    -f "$WORK/grafana-dashboard-providers.yaml" -f "$WORK/grafana-dashboards.yaml"
  # --wait 是真实门禁：镜像拉不动/PVC 绑不上会在这里失败退出（今天 KSM 的
  # ImagePullBackOff 就是被它抓住的），而不是"helm 报成功、Pod 其实起不来"。
  # shellcheck disable=SC2086
  helm upgrade --install prom    $CHART_PROM    -n "$NS_OBS" -f "$WORK/prometheus-values.yaml" --wait --timeout 5m
  # shellcheck disable=SC2086
  helm upgrade --install loki    $CHART_LOKI    -n "$NS_OBS" -f "$WORK/loki-stack-values.yaml" --wait --timeout 5m
  # shellcheck disable=SC2086
  helm upgrade --install grafana $CHART_GRAFANA -n "$NS_OBS" -f "$WORK/grafana-values.yaml"    --wait --timeout 5m
fi

# ────────────────────────── 4/5 部署后断言 ──────────────────────────
# 只对「健康是硬要求」的 job 判失败（这几个一旦 down 平台指标就是错的）；
# 其余 job 的 down 目标全部打印，交由人判断——不用 IP 白名单，避免把新回归一起豁免掉。
echo ""
echo "▶ 4/5 抓取目标断言..."
ASSERT_JOBS="kubernetes-api-servers kubernetes-nodes kubernetes-nodes-cadvisor paas-core prometheus prometheus-pushgateway"

targets="$(kubectl -n "$NS_OBS" exec deploy/prom-prometheus-server -c prometheus-server -- \
  wget -qO- 'http://localhost:9090/api/v1/targets?state=active' 2>/dev/null || echo '')"
if [ -z "$targets" ]; then
  echo "  ⚠ 取不到 Prometheus targets API（Prometheus 未就绪？）—— 断言跳过"
  FAIL=0
else
  echo "  按 job 汇总:"
  printf '%s' "$targets" | jq -r '[.data.activeTargets[] | {j:.labels.job,h:.health}]
    | group_by(.j)[] | "    \(.[0].j)  总\(length) up=\([.[]|select(.h=="up")]|length) down=\([.[]|select(.h!="up")]|length)"'
  FAIL=0
  for j in $ASSERT_JOBS; do
    down="$(printf '%s' "$targets" | jq --arg j "$j" '[.data.activeTargets[]|select(.labels.job==$j and .health!="up")]|length')"
    if [ "$down" != "0" ]; then
      echo "  ✗ 断言失败：job=$j 有 $down 个 down target"
      FAIL=1
    fi
  done
  # 其余 down 目标：打印不判失败（含已知欠账，如跨网段节点 exporter、误抓的 gRPC/Serf 端口）。
  printf '%s' "$targets" | jq -r --arg jobs "$ASSERT_JOBS" '
    .data.activeTargets[] | select(.health!="up")
    | . as $t | select([$jobs|split(" ")|.[]] | index($t.labels.job) | not)
    | "    · \($t.labels.job)  \($t.scrapeUrl|sub("^https?://";""))  \($t.lastError|.[0:70])"' 2>/dev/null \
    | head -20 || true
  if [ "$FAIL" = 0 ]; then echo "  ✓ 关键 job 全部 up"; fi
fi

# ────────────────────────── 5/5 汇总 ──────────────────────────
echo ""
echo "▶ 5/5 状态"
kubectl -n "$NS_OBS" get pods
echo ""
kubectl -n "$NS_OBS" get ingress 2>/dev/null || true
echo ""
if [ "$CHECK_ONLY" = 1 ]; then
  echo "✅ 预检完成（未改动集群）。"
else
  echo "✅ 部署完成。访问："
  echo "   - Grafana   : http://grafana.$DOMAIN_SUFFIX/   (admin / \$GRAFANA_ADMIN_PASSWORD)"
  echo "   - Prometheus: http://prom.$DOMAIN_SUFFIX/targets"
  echo "   - Jaeger    : http://jaeger.$DOMAIN_SUFFIX/"
fi
echo ""
echo "  控制面侧需同步（core 经 ClusterIP 访问三支柱）——deploy/charts/paas/values-paas-k8s.yaml:"
echo "    observability.prometheusURL: http://prom-prometheus-server.$NS_OBS.svc.cluster.local:80"
echo "    observability.lokiURL:       http://loki.$NS_OBS.svc.cluster.local:3100"
echo "    observability.jaegerURL:     http://jaeger.$NS_OBS.svc.cluster.local:16686"
echo "    observability.otelEndpoint:  jaeger.$NS_OBS.svc.cluster.local:4318"

if [ "${FAIL:-0}" = 1 ]; then exit 1; fi
exit 0
