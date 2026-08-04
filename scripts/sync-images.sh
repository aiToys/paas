#!/usr/bin/env bash
# 镜像依赖同步：从源（daocloud 中转 / hub.wang.dd）拉取，retag 后 push 到集群内 registry。
#
# 解决：节点拉不到 docker.io（超时）+ Mac→hub.wang.dd 物理网络不通。
# 源默认走 daocloud 中转（docker.m.daocloud.io，国内可达）；hub.wang.dd 作备用（节点可达）。
#
# 用法：
#   NODE_IP=x.x.x.x ./scripts/sync-images.sh              # 同步全部核心镜像
#   NODE_IP=x.x.x.x ./scripts/sync-images.sh postgres     # 仅同步指定（按目标名匹配）
#   SOURCE=mirror ./scripts/sync-images.sh                # 源强制 mirror（默认）
#   SOURCE=hub ./scripts/sync-images.sh                   # 源强制 hub.wang.dd（节点执行时）
#
# 前置：
#   - 集群内 registry 已部署（deploy/k8s/registry.yaml，NodePort 30050）
#   - 执行机 docker daemon 配 insecure-registries: ["<nodeIP>:30050"]
#   - Mac 执行时 SOURCE=mirror（daocloud 可达）；节点执行时 SOURCE=hub（hub.wang.dd 可达）
#
# 镜像清单真源：docs/deploy/dependencies.md。新增镜像同步更新清单 + 此处 IMAGES 数组。
set -euo pipefail

NODE_IP="${NODE_IP:-$(kubectl get nodes -o wide | awk '!/master|ROLES/{print $6; exit}' | grep -E '^[0-9.]+$' || true)}"
if [[ -z "$NODE_IP" ]]; then
  echo "✗ 无法检测 NODE_IP。手动指定：NODE_IP=x.x.x.x ./scripts/sync-images.sh"
  exit 1
fi
REG="$NODE_IP:30050"
SOURCE="${SOURCE:-mirror}"   # mirror=daocloud 中转；hub=hub.wang.dd
PLATFORM="${PLATFORM:-linux/amd64}"   # 集群节点架构（Mac arm64 拉必须 --platform，否则 exec format error）
MIRROR="docker.m.daocloud.io"
HUB="hub.wang.dd:5000"

# 镜像清单：<源镜像不带 registry 前缀> <目标 repo/路径> <目标 tag>
# 核心链路（paas-core 自身除外，由 deploy-k8s.sh build+push）。
IMAGES=(
  # 元数据 DB + builder + 内置 Git
  "library/postgres:16-alpine|library|16-alpine"
  "library/docker:git|library|git"
  "gitea/gitea:1.22.6|devtools|1.22.6"
  # 数据服务引擎
  "library/mysql:8|library|8"
  "library/postgres:15-alpine|library|15-alpine"
  "library/redis:7-alpine|library|7-alpine"
  "valkey/valkey:7-alpine|library|7-alpine"
  "library/nats:2-alpine|library|2-alpine"
  "minio/minio:latest|library|latest"
  # registry 自身 bootstrap + local-path helper
  "library/registry:2|library|2"
  "library/busybox:latest|library|latest"
)

FILTER="${1:-}"   # 按目标名过滤（如 "postgres" 匹配所有 postgres 镜像）

echo "╔══════════════════════════════════════════════╗"
echo "║  镜像同步 → $REG （源: $SOURCE）"
echo "╚══════════════════════════════════════════════╝"

src_of() {  # 源镜像全名（按 SOURCE 选 registry 前缀）
  local img="$1"
  if [[ "$SOURCE" == "hub" ]]; then
    echo "$HUB/$img"
  else
    echo "$MIRROR/$img"
  fi
}

sync_one() {
  local img="$1" target_repo="$2" tag="$3"
  # 目标名（去源 repo 前缀，与 controller engineImage 同款规整）
  local name="${img##*/}"
  name="${name%%:*}"
  local target="$REG/$target_repo/$name:$tag"

  if [[ -n "$FILTER" && "$name" != *"$FILTER"* && "$target_repo/$name" != *"$FILTER"* ]]; then
    return 0
  fi

  local src
  src=$(src_of "$img")
  echo "▶ $src ($PLATFORM)  →  $target"
  # --platform 强制架构：Mac arm64 默认拉 arm64，推到 amd64 节点会 exec format error。
  if ! docker pull --platform "$PLATFORM" "$src"; then
    echo "  ✗ pull 失败（源 $src 不可达？换 SOURCE=hub 或检查网络）"
    return 1
  fi
  docker tag "$src" "$target"
  if ! docker push "$target"; then
    echo "  ⚠ docker push 失败，尝试 crane 直推..."
    if command -v crane >/dev/null 2>&1; then
      local tar; tar="$(mktemp -t sync.XXXXXX.tar)"
      docker save "$target" -o "$tar"
      crane push --insecure "$tar" "$target" && rm -f "$tar"
    else
      echo "  ✗ crane 未安装，push 失败。go install github.com/google/go-containerregistry/cmd/crane@latest"
      return 1
    fi
  fi
  echo "  ✓ done"
}

fail=0
for entry in "${IMAGES[@]}"; do
  IFS='|' read -r img repo tag <<<"$entry"
  sync_one "$img" "$repo" "$tag" || fail=$((fail+1))
done

echo ""
echo "✅ 同步完成（失败 $fail 个）"
echo "catalog: curl http://$REG/v2/_catalog"
