# 代码构建与流水线

## 代码库

- **internal**：内置 Gitea（无头 + paas-bot 账号），PR / 分支 API 完整
- **external**：外部仓库（校验 clone 可达性防 RCE）

## 构建（BuildRun）

builder 三模式：

| 模式 | 说明 |
|------|------|
| k8s | DooD Job 集群内构建（默认） |
| process | 本地进程 |
| mock | 演示 |

构建产物镜像 digest 不可变；多服务工作负载按 `buildArgs.SERVICE` 分服务构建。

## 流水线（模板 + 绑定）

```
PipelineTemplate（内置 tpl-ci / tpl-cd，Version 升级自动覆盖）
  └── Pipeline（绑定模板 + 参数覆盖）
        └── PipelineRun / StageRun（占位符触发时解析固化）
```

8 种阶段：**build** / **deploy** / **test**（smoke+manual）/ **approve** / **release** / **promote** / **baseline** / **canary**。

## 触发

- 手动 / webhook（token 常量时间比较 + branch glob 匹配）
- 单实例串行：并发触发 409
- 运行详情独立页（GitHub Actions 式全屏视图 + 实时 stage 日志）
