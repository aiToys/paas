# 知识库（RAG）部署指南

知识库（KB）是 P1 落地的 AI 套件首块：文档→切片→embedding→向量存储→检索。**它不是开箱即用**，依赖三个外部后端，缺一不可，否则文档上传/检索 503 或卡「解析中」。

## 依赖三件套

| 依赖 | 作用 | 部署形态 |
|------|------|----------|
| **qdrant** | 向量存储（KB 多租户靠 collection 名 `kb_{kbID}` 隔离，共享实例） | `deploy/kb/qdrant.yaml`（Deployment+PVC+Service） |
| **minio** | 对象存储（存文档原文，异步解析读取；多租户靠 bucket `kb-{tenant}` 隔离） | `deploy/kb/minio.yaml`（Deployment+PVC+Service） |
| **airouter** | embedding 模型（`text-embedding-v4`，复用 MaaS catalog；推理也用它） | core env `PAAS_AIROUTER_API_KEY`（key 不入库，部署时注入） |

> embedding 走 airouter 统一通道：KB 与 Playground 推理共享同一个 airouter api_key。没配 key → 文档索引 `failed`（"embedding 模型不可用/凭证缺失"）。

## 一键部署（dev 集群）

`scripts/deploy-k8s.sh` 已集成 KB infra（step 3/5）：

```bash
PAAS_AIROUTER_API_KEY=<你的 airouter key> ./scripts/deploy-k8s.sh
```

脚本会：
1. 构建 core 镜像（前端 + Go）
2. push 内网 registry
3. **apply `deploy/kb/{qdrant,minio}.yaml`**（envsubst 注入 NODE_IP，镜像 `${NODE_IP}:30050/library/{qdrant,minio}`）
4. helm upgrade（values-paas-k8s.yaml 的 `kb:` 段指向 KB Service）
5. rollout restart core

### 前置：镜像预推到内网 registry

KB infra 镜像必须先在集群内 registry（`<nodeIP>:30050`）存在，否则 Pod 拉取失败：

```bash
# 经 daocloud 中转拉 + 推到内网 registry（amd64！）
NODE_IP=$(kubectl get nodes -o wide | awk '!/master|ROLES/{print $6; exit}')
docker pull --platform linux/amd64 docker.m.daocloud.io/qdrant/qdrant:latest
docker tag  qdrant/qdrant:latest ${NODE_IP}:30050/library/qdrant
docker push ${NODE_IP}:30050/library/qdrant

docker pull --platform linux/amd64 docker.m.daocloud.io/minio/minio:latest
docker tag  minio/minio:latest ${NODE_IP}:30050/library/minio
docker push ${NODE_IP}:30050/library/minio
```

## core env 配置（chart `kb:` 段）

`deploy/charts/paas/values-paas-k8s.yaml`：

```yaml
kb:
  qdrantURL: "http://paas-kb-qdrant.paas.svc.cluster.local:6333"
  qdrantAPIKey: "paas-kb-qdrant-dev"      # = qdrant.yaml 的 QDRANT__SERVICE__API_KEY
  minioEndpoint: "paas-kb-minio.paas.svc.cluster.local:9000"  # 不含 scheme（minio SDK 自动判）
  minioAccessKey: "paas-kb"               # = minio.yaml 的 MINIO_ROOT_USER
  minioSecretKey: "paas-kb-minio-dev"     # = minio.yaml 的 MINIO_ROOT_PASSWORD
```

任一为空 → core 不装配 retriever/blob → KB 文档上传/检索 503（KB CRUD 仍可用）。公网 chart `values.yaml` 默认全空（不假定 infra）。

## 凭证一致性（关键）

KB infra yaml 与 chart `kb:` 段的凭证必须**完全一致**，否则 core 连不上：

| chart `kb:` 字段 | 对应 infra yaml env |
|------------------|---------------------|
| `qdrantAPIKey`   | qdrant `QDRANT__SERVICE__API_KEY` |
| `minioAccessKey` | minio `MINIO_ROOT_USER` |
| `minioSecretKey` | minio `MINIO_ROOT_PASSWORD` |

改凭证时**两边同步改** + 重启 core + 重启 KB Pod。

## 验证

```bash
# 1. KB infra Pod Running
kubectl -n paas get pods | grep kb-   # paas-kb-qdrant / paas-kb-minio 均 1/1 Running

# 2. core env 已注入
kubectl -n paas get deploy paas-core -o jsonpath='{.spec.template.spec.containers[0].env[*]}' | tr '{' '\n' | grep -iE "KB_|AIROUTER"

# 3. KB CRUD + 上传 + 检索 e2e
curl -H "Authorization: Bearer sk-acme-admin" http://paas.k8s.dd/api/knowledgebases                          # 列表 200
# 建 KB（前端「资源中心 → 知识库」→ 新建，选 vector/storage 实例或任意占位）→ 上传 txt/md → 状态 parsing→indexed
# 检索测试：输入查询 → 返回 chunks + 相似度
```

## 故障排查

| 现象 | 根因 | 修复 |
|------|------|------|
| 上传 503「知识库后端未配置」 | core env `kb:` 段有空值（retriever/blob 没装配） | 配齐 5 个 kb env + 重启 core |
| 上传「成功」但文档列表空、搜不到 | **前端 bug（已修）**：旧版 fetchAuth 不检查 HTTP status，503 被当成功 | 更新前端（onUpload 检查 resp.ok） |
| 文档卡「解析中」不前进 | processDocument 异步 goroutine 失败但 markFailed 也用了 canceled ctx；或 airouter 不可达 | 看 core 日志，确认 airouter key + qdrant/minio 可达 |
| 文档 `failed`「embedding 模型不可用/凭证缺失」 | airouter api_key 未配或 text-embedding-v4 通道无凭证 | 配 `PAAS_AIROUTER_API_KEY` 重启 core |
| 文档 `failed`「向量库初始化失败」 | qdrant 不可达或 api_key 不匹配 | 验证 qdrant Pod Running + `qdrantAPIKey` 一致 |
| 文档 `failed`「原文存储失败」 | minio 不可达或 access/secret 不匹配 | 验证 minio Pod Running + 凭证一致 |

## 数据持久化

- qdrant：PVC `paas-kb-qdrant`（5Gi，local-path），存 `/qdrant/storage`（collection + vectors）
- minio：PVC `paas-kb-minio`（5gi，local-path），存 `/data`（文档原文 bucket）

删 PVC = 删数据。重部署（不删 PVC）数据保留。

## 与 dataservice 的关系（勿混）

KB 用的是**专用共享实例**（`paas-kb-qdrant` / `paas-kb-minio`），**不复用** dataservice 体系建的租户实例（`ds-*`）。两者解耦：
- dataservice `ds-*`：租户私有数据服务（用户建，按租户隔离，应用绑定注入连接）
- KB `paas-kb-*`：平台共享 AI 后端（KB 多租户靠 collection/bucket 名隔离）

> 当前 KB 的 `vectorStoreRef` / `objectStoreRef` 字段为**展示用占位**（MVP 全局 env 模式），未真正解析到 dataservice 实例。后续 P1.x 可改为按 ref 动态解析（每个 KB 用自己引用的实例，去掉全局 env 依赖）。

## 留后续

- KB ref 动态解析（`vectorStoreRef`/`objectStoreRef` → dataservice connection，去全局 env）
- 多模态文档解析（PDF/Office，现仅 txt/md/html）
- BM25 混合检索 + reranker（现纯向量检索）
- 凭证改 K8s Secret 注入（生产，现固定 dev 值）
