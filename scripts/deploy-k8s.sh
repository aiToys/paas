#!/usr/bin/env bash
# 部署 PaaS 到本地 K8s 集群（参考 aiem 模式：hub.wang.dd:5000 + hermes ingress + paas.k8s.dd）。
#
# 流程：docker build（amd64 交叉编译 + 前端 embed）→ push 私有 registry → helm upgrade --install
#
# 用法：
#   ./scripts/deploy-k8s.sh              # 默认 TAG=0.1.0
#   TAG=0.1.1 ./scripts/deploy-k8s.sh    # 自定义 tag
#   SKIP_PUSH=1 ./scripts/deploy-k8s.sh  # 仅构建不推送（调试）
#   ARCH=arm64 ./scripts/deploy-k8s.sh   # arm64 集群（默认 amd64）
#
# 前置：
#   - docker（colima 或 Docker Desktop）+ buildkit
#   - hub.wang.dd:5000 可达（从 docker daemon；若 dockerd push 超时，脚本自动 fallback crane 直推）
#   - crane（go-containerregistry）在 PATH 时用于 push fallback：`go install github.com/google/go-containerregistry/cmd/crane@latest`
#   - kubectl/helm 已配置集群访问
#
# 镜像架构：Dockerfile builder 用本地架构 Go 交叉编译到 amd64（ARG GOARCH，默认 amd64），
# runtime 用 linux/amd64 distroless。arm64 集群用 ARCH=arm64（需改 Dockerfile runtime --platform）。
set -euo pipefail

REG="${REG:-hub.wang.dd:5000/paas}"
TAG="${TAG:-0.1.0}"
NS="${NS:-paas}"
RELEASE="${RELEASE:-paas}"
IMAGE="$REG/paas-core:$TAG"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "╔══════════════════════════════════════════════╗"
echo "║  PaaS K8s 部署（$IMAGE）"
echo "╚══════════════════════════════════════════════╝"

echo ""
echo "▶ 1/3 构建镜像（多阶段：前端 build + Go 交叉编译 amd64 + distroless）..."
docker build -t "$IMAGE" -f Dockerfile .

push_image() {
  # 优先 docker push；dockerd 到 registry 网络不通（如 colima VM 路由问题）时 fallback crane 直推。
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
  echo "▶ 2/3 推送到私有 registry $REG ..."
  push_image
fi

echo ""
echo "▶ 3/3 helm upgrade --install（CRD + RBAC + core + ingress）..."
helm upgrade --install "$RELEASE" deploy/charts/paas \
  -n "$NS" --create-namespace \
  -f deploy/charts/paas/values-paas-k8s.yaml \
  --set image.tag="$TAG"

echo ""
echo "⏳ 等待 Pod 就绪..."
kubectl -n "$NS" rollout status deploy/"$RELEASE-core" --timeout=180s || true

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
