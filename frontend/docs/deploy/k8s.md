# K8s 集群部署

## 安装前准备

- Kubernetes 集群与 Helm，当前 kubeconfig 指向预期集群。
- 创建 CRD、ClusterRole 等资源的权限；数据面通过 ServiceAccount 与 RBAC 访问集群。
- 可拉取的 Core/PostgreSQL 镜像，以及默认或显式配置的 StorageClass。
- 访问入口：Ingress Controller 与 DNS/TLS，或本地端口转发。

在仓库根目录安装到独立 namespace：

```bash
helm install paas deploy/charts/paas --namespace paas --create-namespace
kubectl -n paas get pods
kubectl -n paas get services
```

默认 values 面向演示。生产先按[生产配置表](./index#生产安全配置)准备覆盖文件，再使用 `-f <你的 values 文件>` 安装。实际镜像版本和来源应与交付件核对。

若未启用 Ingress，可从 `get services` 结果选择 Core 服务进行端口转发：

```bash
kubectl -n paas port-forward service/<Core 服务名> 8080:8080
```

访问 `http://localhost:8080/`、`/console/`、`/next/`、`/admin/` 和 `/docs/`。`/next/` 为新版试验版。

## CRD 生命周期与升级

当前 Workload/DataService CRD 位于 chart **`templates/crds.yaml`**，由 `crds.install` 控制，参与 Helm 安装与升级；与 Helm `crds/` 目录的仅安装语义不同。

- 升级时对照本版本 schema 与存量资源，检查 CRD 及控制器镜像是否一致。
- chart 当前也将 CRD 纳入卸载生命周期；删除 CRD 会影响关联 CR，生产移除 release 前先检查资源归属。
- 推荐使用新的镜像 tag/digest 交付。相同 tag 和相同 Pod template 不会仅因仓库镜像变化就自动滚动；需要时对实际 Core Deployment 执行滚动重启，并核对 Pod 镜像与就绪状态。

## 数据面与真实构建

`ctrl.GetConfig()` 使用 `PAAS_KUBECONFIG` 或集群内 SA 配置。无有效配置会降级；`PAAS_K8S_ENABLED=false` 可显式禁用数据面。

真实构建需 `devops.builder=k8s`，镜像库及 Git 访问配置，并满足当前 DooD 构建对节点 Docker socket 的要求。默认 builder 为空，使用 mock；仅看到构建记录成功不能证明镜像已真实构建与推送。

## 开发集群脚本与镜像库

`scripts/deploy-k8s.sh` 用于已有开发集群的构建部署：检测节点 IP、生成覆盖值、推送 registry 后升级。使用前准备 `deploy/k8s/registry.yaml`、同步依赖镜像，并审查 `values-paas-k8s.yaml` 中的开发配置。

节点拉镜像使用 `<nodeIP>:30050/...`；kubelet 不通过 Pod 的 svc DNS 解析镜像库地址。依赖清单见仓库 `docs/deploy/dependencies.md`。此脚本不替代通用生产安装配置。

## 部署后验证

1. `/livez` 存活、控制台与文档入口正常。
2. 核对本次镜像 tag/digest、Pod 就绪与数据库持久化。
3. 创建测试工作负载，检查 CRD、Deployment/Job/CronJob 和运行态回写。
4. 按需分别验证真实构建推送、模型调用、采集后端及外部入口；这些检查独立于 Helm 成功。
