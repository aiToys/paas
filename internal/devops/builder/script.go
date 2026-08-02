package builder

// Job 容器内构建脚本（DooD：挂节点 docker.sock，复用节点 daemon）。
//
// 设计：所有"业务逻辑"（cloneURL 注入 token、tag/ref 计算）在 Go 侧算好透传为 env，
// 脚本只做纯变量替换，避免 shell 拼接带来的注入面。脚本读取的 env：
//   - CLONE_URL（已含 token，私有仓库 HTTPS clone 用）
//   - BRANCH / APP_ID / REGISTRY / REF（registry/app:branch-commit8）
//   - COMMIT（已知则用，空则 rev-parse HEAD 回填）
//   - DOCKERFILE（非空则 -f 指定，相对 BUILD_CTX）/ BUILD_CONTEXT（空则 .）
//   - REGISTRY_USER / REGISTRY_PASS（非空则 docker login，密码经 stdin 不进 argv）
//
// 注意：
//   - docker build 参数用 BUILD_ARGS 变量拼接后无引号展开（依赖 word splitting），
//     而非 ${var:+-f "$var"}——busybox ash 对 ${:+} 嵌套引号展开有 bug（会丢 -f）。
//   - DOCKER_BUILDKIT=0 禁用 buildkit 用 classic builder：DooD 下 buildkit 经 sock 的
//     context transfer 有 bug（dockerfile 收到 2B 损坏 → "failed to read dockerfile"），
//     classic builder 直接传 context 不受影响。
//
// 最后一行 echo "PAAS_DIGEST=sha256:..." 是与 Go 侧约定的回传标记（parseDigest 正则解析）。
const builderScript = `set -eu
export DOCKER_BUILDKIT=0
git clone --depth 1 --branch "$BRANCH" "$CLONE_URL" /workspace
cd /workspace
if [ -z "$COMMIT" ]; then COMMIT=$(git rev-parse HEAD); fi
if [ -n "$REGISTRY_USER" ]; then printf '%s\n' "$REGISTRY_PASS" | docker login -u "$REGISTRY_USER" --password-stdin "$REGISTRY"; fi
BUILD_CTX="${BUILD_CONTEXT:-.}"
BUILD_ARGS="-t $REF"
if [ -n "$DOCKERFILE" ]; then BUILD_ARGS="$BUILD_ARGS -f $DOCKERFILE"; fi
docker build $BUILD_ARGS "$BUILD_CTX"
docker push "$REF"
digest=$(docker inspect --format '{{range .RepoDigests}}{{.}}{{end}}' "$REF")
if [ -z "$digest" ]; then digest=$(docker inspect --format '{{.Id}}' "$REF"); fi
echo "PAAS_DIGEST=${digest#*@}"
echo "PAAS_BUILD_DONE=1"
`
