# 泵道（Lane）

> 注：全链路灰度 = 环境内流量染色，不是环境复制。

## 三种生命周期

| 模式 | 用途 | 回收 |
|------|------|------|
| standard | 联调泳道 | TTL 自动回收（级联删泳道内工作负载） |
| permanent | 大项目 / 常驻火车 | 手动关闭 |
| canary-\<runID\> | 金丝雀并行验证 | promote（滚动全量）或 terminate（零风险退出） |

## 流量语义

- 入口染色归 SDK（`LaneMiddleware` / `ApplyLaneHeader`），平台网关不做
- 命中泳道的服务走泳道副本，未命中降级基线——**只部署变更服务**
- 跨泳道服务发现：`<service>-<lane>` 先查，降级 `<service>`

## 全链路标牌

trace 属性 `paas.lane`、Loki lane label、资源属性 tenant/cluster/lane 三处对齐，排障一条链路看全。

## 删除保护

DELETE 泳道：前置校验进行中流水线 run（409 拒绝）；生产泳道删除需 `prod:write`。
