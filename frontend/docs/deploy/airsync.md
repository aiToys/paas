# 离线交付（airsync）

气隙环境四步走：

```bash
# 1. 有网环境：打离线包（镜像 + chart + sha256 清单）
airsync bundle --values values-paas.yaml -o paas-bundle/

# 2. 物理介质拷贝到目标环境

# 3. 安装（verify sha256 → load/retag/push 内部 registry → helm install）
airsync install --bundle paas-bundle/ --kubeconfig ...

# 4. 体检
airsync doctor
```

## 子命令

| 命令 | 说明 |
|------|------|
| `bundle` | 打包镜像 + chart + 校验和 |
| `install` | 校验 → 导入 → 安装一条龙 |
| `verify` | sha256 完整性校验 |
| `doctor` | 部署后健康诊断 |

## 编译

```bash
make airsync   # 产出 bin/airsync
```
