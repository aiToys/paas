# 配置中心

运行时**动态**配置（区别于应用配置 appconfig 的静态重启注入）：

## Namespace 双 scope

- **app 派生**：应用 × 环境自动懒建
- **shared**：跨应用共享配置

## 版本化发布

- 发布 = 不可变快照（partial unique index 防双 active）
- 空发布拒绝（无变更返回 409）
- 回滚 = 重新激活历史版本，事务化翻转状态
- 按应用名发现：未知 / 无 active 统一 `{"published":false}` 不泄漏

## 泳道覆盖（LaneOverride）

merge 链：`app × env → lane`，lane key 级覆盖；version + overrideHash 双指纹。

## 与 appconfig 的区别

| | appconfig | configcenter |
|--|-----------|--------------|
| 时机 | 部署时注入环境变量 | 运行时热更新 |
| 形态 | 应用 × 环境静态 | 版本化快照 + 泳道覆盖 |
