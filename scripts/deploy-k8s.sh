#!/usr/bin/env bash
# 部署 PaaS 到本地 K8s 集群（集群内自建 registry + hermes ingress + paas.k8s.dd）。
#
# 流程：检测 worker nodeIP → docker build（amd64 交叉编译 + 前端 embed）
#       → push 集群内 registry（NodePort 30050）→ envsubst values → helm upgrade --install
#
# 用法：
#   ./scripts/deploy-k8s.sh              # 默认 TAG=0.1.0
#   TAG=0.1.1 ./scripts/deploy-k8s.sh    # 自定义 tag
#   SKIP_PUSH=1 ./scripts/deploy-k8s.sh  # 仅构建不推送（调试）
#   ARCH=arm64 ./scripts/deploy-k8s.sh   # arm64 集群（默认 amd64）
#
# 前置：
#   - 集群内 registry 已部署：kubectl apply -f deploy/k8s/registry.yaml（见 docs/deploy/dependencies.md）
#   - docker（colima 或 Docker Desktop）+ buildkit
#   - Mac/colima docker daemon 已配 insecure-registries: ["<nodeIP>:30050"]（HTTP registry 无 TLS）
#   - crane（go-containerregistry）在 PATH 时用于 push fallback：`go install github.com/google/go-containerregistry/cmd/crane@latest`
#   - envsubst（gettext）+ kubectl/helm 已配置集群访问
#
# 镜像架构：Dockerfile builder 用本地架构 Go 交叉编译到 amd64（ARG GOARCH，默认 amd64），
# runtime 用 linux/amd64 distroless。arm64 集群用 ARCH=arm64（需改 Dockerfile runtime --platform）。
set -euo pipefail

TAG="${TAG:-0.1.0}"
NS="${NS:-paas}"
RELEASE="${RELEASE:-paas}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# 检测 worker 节点 InternalIP（registry NodePort 30050 在所有节点可达，取首个 worker）。
# kubelet/CRI 在节点拉镜像无法解析 svc.cluster.local，故 Pod 镜像也用 IP:NodePort。
NODE_IP="${NODE_IP:-$(kubectl get nodes -o wide | awk '!/master|ROLES/{print $6; exit}' | grep -E '^[0-9.]+$' || true)}"
if [[ -z "$NODE_IP" ]]; then
  # 兜底：取第一个非 control-plane 节点
  NODE_IP=$(kubectl get nodes -o jsonpath='{range .items[*]}{.status.addresses[?(@.type=="InternalIP")].address}{"\n"}{end}' | head -1)
fi
if [[ -z "$NODE_IP" ]]; then
  echo "✗ 无法检测 worker 节点 IP。手动指定：NODE_IP=x.x.x.x ./scripts/deploy-k8s.sh"
  exit 1
fi
REG="${REG:-${NODE_IP}:30050/paas}"
IMAGE="$REG/paas-core:$TAG"

echo "╔══════════════════════════════════════════════╗"
echo "║  PaaS K8s 部署（${IMAGE}）"
echo "╚══════════════════════════════════════════════╝"
echo "  worker nodeIP: $NODE_IP  （registry NodePort 30050）"

# 前置检查：Mac docker daemon 是否配了 <NODE_IP>:30050 insecure registry。
if ! docker info 2>/dev/null | grep -q "$NODE_IP:30050"; then
  echo "⚠ Mac docker daemon 未检测到 insecure registry \"$NODE_IP:30050\"。"
  echo "  请在 ~/.colima/default/docker.sock 或 Docker Desktop 配 insecure-registries 后重启 daemon，"
  echo "  否则 docker push 将因 HTTP registry 被拒。详见 docs/deploy/dependencies.md。"
fi

if ! command -v envsubst >/dev/null 2>&1; then
  echo "✗ envsubst 未安装（gettext）。macOS: brew install gettext && brew link --force gettext"
  exit 1
fi

echo ""
echo "▶ 1/4 构建镜像（多阶段：前端 build + Go 交叉编译 amd64 + alpine runtime）..."
# DOCKER_BUILDKIT=1 走 buildkit：builder 用 --platform=$BUILDPLATFORM 跑本地架构（arm64 Mac），
# Go 交叉编译到 amd64，避 QEMU 全栈模拟（Go http2/TLS 在 QEMU amd64 下 SIGSEGV 致 go mod
# download 必 crash）。buildkit 内置 frontend 支持 $BUILDPLATFORM（不加 # syntax= 不拉远程）。
# --platform=linux/amd64：明确目标架构（dev 集群 amd64），Dockerfile 的 ARG TARGETARCH 据此取 amd64。
DOCKER_BUILDKIT=1 docker build --platform=linux/amd64 -t "$IMAGE" -f Dockerfile .

push_image() {
  # 优先 docker push（daemon 已配 insecure registry）；失败 fallback crane 直推（不经 dockerd）。
  if docker push "$IMAGE" 2>&1; then
    return 0
  fi
  echo "⚠ docker push 失败，尝试 crane 直推（不经 dockerd）..."
  if command -v crane >/dev/null 2>&1; then
    local tar
    tar="$(mktemp -t paas-core.XXXXXX.tar)"
    docker save "$IMAGE" -o "$tar"
    crane push --insecure "$tar" "$IMAGE"
    rm -f "$tar"
    return 0
  fi
  echo "✗ crane 未安装且 docker push 失败。请安装：go install github.com/google/go-containerregistry/cmd/crane@latest"
  return 1
}

if [[ "${SKIP_PUSH:-0}" == "1" ]]; then
  echo "  SKIP_PUSH=1，跳过推送"
else
  echo ""
  echo "▶ 2/4 推送到集群内 registry $REG ..."
  push_image
fi

# 构建+推送完成后清理 dangling 镜像（上一轮 build 留下的旧 <none> 镜像）。
# 只清无 tag 且未被任何容器引用的，避免影响正在跑的容器；本次构建的新镜像有 tag 不会被清。
echo ""
echo "▶ 2.5/4 清理本地旧镜像（dangling <none>）..."
pruned=$(docker image prune -f 2>&1 | grep "Total reclaimed space" || true)
if [[ -n "$pruned" ]]; then
  echo "  $pruned"
else
  echo "  无可清理镜像"
fi

echo ""
echo "▶ 3/5 部署知识库后端（qdrant + minio 共享实例，KB RAG 用）..."
# KB 共享 infra：镜像来自内网 registry（library/qdrant + library/minio）。
# ${NODE_IP} 由 envsubst 注入镜像地址；values-paas-k8s.yaml 的 kb 段指向这些 Service。
export NODE_IP
envsubst < deploy/kb/qdrant.yaml | kubectl -n "$NS" apply -f -
envsubst < deploy/kb/minio.yaml | kubectl -n "$NS" apply -f -

echo ""
echo "▶ 4/5 envsubst values（注入 NODE_IP=${NODE_IP}）→ helm upgrade --install..."
VALUES_TMP="$(mktemp -t paas-values.XXXXXX.yaml)"
export NODE_IP
envsubst < deploy/charts/paas/values-paas-k8s.yaml > "$VALUES_TMP"
helm upgrade --install "$RELEASE" deploy/charts/paas \
  -n "$NS" --create-namespace \
  -f "$VALUES_TMP" \
  --set image.tag="$TAG" \
  --set maas.airouterApiKey="${PAAS_AIROUTER_API_KEY:-}"
rm -f "$VALUES_TMP"

# CRD 在 chart templates/ 内，helm upgrade 对已存在的 CRD 不应用变更（CRD 升级缺口：
# 2026-08-28 e2e 暴露——Task 3 加 resources 字段后集群 CRD 仍旧版，reconciler 静默丢字段）。
# 显式 kubectl apply 保证 CRD schema 跟随 chart（apply 幂等，不删 CR 实例）。
kubectl apply -f config/crds/ >/dev/null

# image.tag 不变时 helm upgrade 不改 deployment spec，不触发 rollout ——
# 会造成「部署成功但 Pod 跑旧镜像 digest」的假象。强制 rollout restart，
# 配合 pullPolicy: Always 确保每次部署拉取最新 push 的 digest。
echo ""
echo "▶ 5/5 强制 rollout restart（拉取最新镜像 digest）..."
kubectl -n "$NS" rollout restart deploy/"${RELEASE}-core"

echo ""
echo "⏳ 等待 Pod 就绪..."
kubectl -n "$NS" rollout status deploy/"${RELEASE}-core" --timeout=180s || true

echo ""
echo "📊 部署状态:"
kubectl -n "$NS" get pods
echo ""
kubectl -n "$NS" get ingress
echo ""
echo "✅ 部署完成。访问："
echo "   - 官网:       http://paas.k8s.dd/"
echo "   - 用户控制台: http://paas.k8s.dd/console/"
echo "   - 后台管理:   http://paas.k8s.dd/admin/"
echo "   - API 探针:   curl http://paas.k8s.dd/livez"
echo "   - 模型列表:   curl -H 'Authorization: Bearer sk-acme-admin' http://paas.k8s.dd/v1/models"
echo "   - registry:   curl http://${NODE_IP}:30050/v2/_catalog"
