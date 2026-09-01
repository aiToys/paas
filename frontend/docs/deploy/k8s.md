# K8s 集群部署

## 公网安装

```bash
helm install paas deploy/charts/paas
```

注意：

- **CRD 不随 helm upgrade 应用**——升级后需显式 `kubectl apply -f config/crds/`
- 镜像 tag 不变不触发 rollout——需 `kubectl rollout restart deploy/paas-core`

## 集群内自建 registry（气隙替代）

节点直连 registry（NodePort 30050），Pod 镜像引用 `<nodeIP>:30050/...`（kubelet 解析不了 svc DNS）：

```bash
./scripts/deploy-k8s.sh   # 检测 worker nodeIP + envsubst 注入 values
```

## 镜像构建

多阶段 Dockerfile（node 前端 → Go 交叉编译 → distroless）：

- 国内源默认，`--build-arg` 覆盖
- **必须 `DOCKER_BUILDKIT=1`** + builder `FROM --platform=$BUILDPLATFORM`（arm64 Mac QEMU 下 Go 必 SIGSEGV）
- 国内拉镜像走 daocloud 中转（`docker.m.daocloud.io`）

## in-cluster 要点

- `ctrl.GetConfig()` 自动检测（`PAAS_KUBECONFIG` 或 SA token）
- manager cache 按 `PAAS_K8S_NAMESPACE` 限定 namespace
