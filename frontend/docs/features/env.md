# 环境与发布

## 环境

- 类型：`test`（测试）/ `prod`（生产），生产环境视觉强隔离 + 顶栏 scope 15 分钟自动回退
- 发布阶序（PromoteOrder）：如 测试 → 生产，晋升按阶序逐级，防跳级

## Release 与版本

- **deploy** 阶段：产部署记录（可回滚），不打版本
- **release** 阶段：版本里程碑（git tag + 镜像版本号），不部署
- **promote** 阶段：把当前环境基线部署到下一阶序环境
- 回滚指针：PreviousImageID，一键回上一版本

## 生产防护

- 生产写操作需 `prod:write` 权限（admin），fail-closed（环境解析失败按生产处理）
- 危险操作（删生产负载等）输入应用名二次确认
- 流水线 stage 组合静态预演，防 developer 绕过 approve 门禁
