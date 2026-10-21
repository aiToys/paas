# 离线交付（airsync）

airsync 将版本镜像、Helm chart 与 sha256 清单打成 `.tar.gz` 文件。需要联网端准备依赖，目标端准备可用 K8s 集群、内网镜像库和 docker/helm/kubectl。

## 编译与检查工具

在仓库根目录执行：

```bash
make airsync
./bin/airsync doctor
```

`doctor` 检查本机工具依赖；它不是集群或业务健康验收。

## 打包、校验与安装

下列版本、路径与 registry 均需替换为本次交付的实际值：

```bash
# 联网环境：按已准备的版本镜像打包
./bin/airsync bundle --version <版本> --out <离线包.tar.gz>

# 拷贝文件到目标环境后先校验
./bin/airsync verify --bundle <离线包.tar.gz>

# 校验 → 导入/重标记/推送到内网 registry → Helm 安装
./bin/airsync install --bundle <离线包.tar.gz> --target-registry <内网镜像库> --namespace paas
```

bundle 支持 `--registry`（默认 `ghcr.io/aitoys`）、`--chart`（默认 `deploy/charts/paas`）和 `--out`。必须给 `--version`；install 必须给 `--bundle` 与 `--target-registry`。

工具没有 `bundle --values`、`install -f` 或 `install --kubeconfig` 参数。集群选择沿用 kubectl/Helm 的配置，例如 `KUBECONFIG` 环境变量。

当前 install 只覆盖镜像和命名空间等内置参数，默认安装面向演示；生产安全配置不会自动补齐。需在交付前通过受控的 chart 定制及凭证注入流程准备目标配置，再核对[生产配置表](./index#生产安全配置)。安装完成不等于安全基线或业务依赖已验证。

## 可观测栈与其他依赖

`bundle --with-observability` 附带观测栈的 chart、清单与部署脚本，install 会继续部署；**不包含全部观测栈镜像**。目标集群仍需可拉取 Prometheus、Grafana、Loki、Promtail、Jaeger 等镜像，按仓库 `docs/deploy/dependencies.md` 预先推送到内网 registry。

知识库、Git、builder 和数据服务实际使用的镜像，也要按部署范围准备。外部模型供应商在气隙环境不一定可达，应配置目标网络可访问的兼容通道。

## 验证边界

sha256 校验用于核对归档完整性，不证明来源真实性或部署成功。安装后按[K8s 验证步骤](./k8s#部署后验证)检查 Core、数据库、数据面与实际使用的外部能力。
