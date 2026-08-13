# 镜像依赖管理

PaaS 在 K8s 集群内**自建 Docker Registry**（`deploy/karts/registry.yaml`），所有镜像依赖统一来自集群内 registry（NodePort 30050），去除对外部 `hub.wang.dd:5000` 的依赖。一处管理、可文档化排查。

本文档是**镜像依赖单一真源**：哪些镜像该预推、从哪同步、节点 insecure registry 怎么配、出问题怎么查。

## 架构

```
集群内 registry: deploy/k8s/registry.yaml
  Service: paas-registry (NodePort 30050)
  地址: <nodeIP>:30050  （worker 节点 InternalIP）
  存储: local-path PVC 20Gi
```

**地址统一 `<nodeIP>:30050`**：
- kubelet/CRI 在节点拉镜像，**无法解析 `svc.cluster.local`**，Pod 镜像也用 IP:NodePort
- builder DooD 经节点 docker daemon push → IP:NodePort（节点网络）
- core Pod registry client（镜像库 UI）→ Pod 网络 → NodePort（kube-proxy 转发）
- Mac 导入 → IP:NodePort

`NODE_IP` 由 `scripts/deploy-k8s.sh` 自动检测（首个 worker 节点 InternalIP），`envsubst` 注入 `values-paas-k8s.yaml` 的 `${NODE_IP}` 占位。

## 镜像清单

| 镜像 | 用途 | 源（导入用） | 目标路径 |
|------|------|--------------|----------|
| `paas-core:0.1.0` | 平台自身（控制面 + 前端 embed） | 本地 `docker build` | `<nodeIP>:30050/paas/paas-core:0.1.0` |
| `registry:2` | 集群内 registry 自身（bootstrap） | `hub.wang.dd:5000/library/registry:2` 或 `docker.m.daocloud.io/library/registry:2` | （registry.yaml 直接引用） |
| `postgres:16-alpine` | 元数据 DB（PAAS_DB_URL） | `docker.m.daocloud.io/library/postgres:16-alpine` | `<nodeIP>:30050/library/postgres:16-alpine` |
| `docker:git` | DevOps builder Job 容器（DooD） | `docker.m.daocloud.io/library/docker:git` | `<nodeIP>:30050/library/docker:git` |
| `gitea:1.22.6` | 内置 Git 后端（无头） | `docker.m.daocloud.io/gitea/gitea:1.22.6` | `<nodeIP>:30050/devtools/gitea:1.22.6` |
| `mysql:8` | 数据服务引擎（db=mysql） | `docker.m.daocloud.io/library/mysql:8` | `<nodeIP>:30050/library/mysql:8` |
| `postgres:15-alpine` | 数据服务引擎（db=postgres） | `docker.m.daocloud.io/library/postgres:15-alpine` | `<nodeIP>:30050/library/postgres:15-alpine` |
| `redis:7-alpine` | 数据服务引擎（cache=redis） | `docker.m.daocloud.io/library/redis:7-alpine` | `<nodeIP>:30050/library/redis:7-alpine` |
| `valkey:7-alpine` | 数据服务引擎（cache=valkey） | `docker.m.daocloud.io/valkey/valkey:7-alpine` | `<nodeIP>:30050/library/valkey:7-alpine` |
| `nats:2-alpine` | 数据服务引擎（mq=nats） | `docker.m.daocloud.io/library/nats:2-alpine` | `<nodeIP>:30050/library/nats:2-alpine` |
| `minio:latest` | 数据服务引擎（storage=minio） | `docker.m.daocloud.io/minio/minio:latest` | `<nodeIP>:30050/library/minio:latest` |
| `local-path-provisioner:v0.0.24` | StorageClass（local-path PVC） | `hub.wang.dd:5000/rancher/local-path-provisioner:v0.0.24` | （local-path.yaml 直接引用） |
| `busybox:latest` | local-path helper（PV mount/umount） | `docker.m.daocloud.io/library/busybox:latest` | `<nodeIP>:30050/library/busybox:latest` |
| observability 全套 | Prom/Loki/Jaeger/Grafana/Promtail/node-exporter | 见 `deploy/observability/` | `<nodeIP>:30050/observability/*` |

> **data-service 引擎镜像**：reconciler 按 `PAAS_IMAGE_REGISTRY` env 拼 `<registry>/library/<name>`，引擎镜像需推到 `<nodeIP>:30050/library/`。占位引擎（kafka/rabbitmq/rocketmq/vector/search）不拉起，无需预推。

## 首次部署步骤

### 1. 部署 local-path StorageClass（registry/gitea/postgres 依赖 PVC）

```bash
# local-path.yaml 镜像需先导入（bootstrap 顺序：local-path 先于 registry）
# 若节点已能拉 hub.wang.dd:5000 可跳过导入直接 apply
kubectl apply -f deploy/k8s/local-path.yaml
```

### 2. 部署集群内 registry

```bash
# registry:2 镜像需节点可拉（hub.wang.dd:5000/library/registry:2 或 daocloud 中转）
kubectl apply -f deploy/k8s/registry.yaml
kubectl -n paas wait deploy/paas-registry --for=condition=ready --timeout=120s

# 拿 NODE_IP
NODE_IP=$(kubectl get nodes -o wide | awk '!/master|ROLES/{print $6; exit}')
echo "registry: http://$NODE_IP:30050"
curl http://$NODE_IP:30050/v2/   # 返 {} 即就绪
```

### 3. 配置 docker daemon insecure registry（节点 + Mac）

registry 是 HTTP（无 TLS），docker daemon 需配 insecure。

**Mac（colima）**：`~/.docker/config.json` 或 colima 配置追加 `"insecure-registries": ["<nodeIP>:30050"]`，重启 docker daemon。

**节点（kb2/kb3 worker）**：`/etc/docker/daemon.json` 追加 `"insecure-registries": ["<nodeIP>:30050"]`，`sudo systemctl restart docker`。kb1 master 可选（业务 Pod 不调度）。

> 不配则 `docker push/pull` 报 `http: server gave HTTP response to HTTPS client`。

### 4. 导入镜像到集群内 registry

```bash
# 列表驱动（postgres/docker:git/gitea/引擎/local-path helper 等）
NODE_IP=$NODE_IP ./scripts/sync-images.sh

# 验证
curl http://$NODE_IP:30050/v2/_catalog
curl "http://$NODE_IP:30050/v2/library/postgres/tags/list"
```

> **Mac → hub.wang.dd 不通时**：`sync-images.sh` 走 daocloud 中转（`docker.m.daocloud.io`），或经集群内 DooD Job（节点可达 hub.wang.dd + NodePort）。

### 5. 部署 PaaS

```bash
./scripts/deploy-k8s.sh   # 自动检测 NODE_IP + envsubst + build + push + helm upgrade
```

## 排查指南

**症状：Pod `ImagePullBackOff` / `ErrImagePull`**

```bash
kubectl -n paas describe pod <pod> | grep -A5 "Failed to pull"   # 看拉取失败的镜像地址
curl http://$NODE_IP:30050/v2/_catalog                           # 查 registry 有没有该仓库
curl "http://$NODE_IP:30050/v2/<name>/tags/list"                 # 查 tag 在不在
```

缺则按「镜像清单」表导入：`NODE_IP=$NODE_IP ./scripts/sync-images.sh <镜像名>`。

**症状：`docker push` 报 HTTPS client**

Mac/节点 docker daemon 未配 `<nodeIP>:30050` insecure registry → 见步骤 3。

**症状：builder 构建产物 push 失败（构建记录 failed）**

builder Job 经节点 docker daemon push `<nodeIP>:30050`。查：① worker 节点 insecure registry 配没配；② registry Pod Running；③ `PAAS_REGISTRY` env 值（`kubectl -n paas exec deploy/paas-core -- env | grep PAAS_REGISTRY`）。

**症状：镜像库 UI（DevOps tab）503「镜像库不可达」**

core Pod registry client 访问 `<nodeIP>:30050` 失败。查：① registry Pod Running；② core Pod 是否仍带 hostNetwork（应为 false，`kubectl get pod -o wide` IP 是 Pod 网段）；③ `PAAS_REGISTRY` env 值正确。

## 旁路组件（仍用 hub.wang.dd，按需迁移）

以下独立组件本次未迁集群内 registry（非 paas-core 核心链路），保留 hub.wang.dd 引用，可按上表「源」列后续迁移：

- `deploy/devops/gitea.yaml`（gitea 镜像）—— 迁移：apply 前 `sed s|hub.wang.dd:5000|<nodeIP>:30050|` 或改 yaml 占位。
- `deploy/k8s/local-path.yaml`（local-path-provisioner + busybox）—— 基础设施，先于 registry 存在，保留。
- `deploy/observability/*`（observability 全套）—— 独立 helm chart，迁移改 values repository 前缀。

## registry 管理

- **查看**：`curl http://$NODE_IP:30050/v2/_catalog`（全量仓库）+ 平台镜像库 UI（DevOps tab → 镜像库，catalog/tags/digest 实时视图）。
- **存储**：local-path PVC 20Gi（`kubectl -n paas get pvc paas-registry-data`），dev/demo 轻量。
- **删除**：当前 registry 未启 delete API（`REGISTRY_STORAGE_DELETE_ENABLED=false`），镜像库只读。删除 + GC 留后续。
- **持久化**：registry Pod 重启数据不丢（PVC）；删 PVC `kubectl -n paas delete pvc paas-registry-data` 才清空（高危）。
