# console-user UI 打磨记录（10 轮）

## 设计语言

深空墨蓝 + 靛蓝主色 + emerald 运行态信号（参考 Vercel 精确 / Linear 间距 / OpenAI Platform 数据清晰）。
signature：模型/部署卡片带呼吸状态点 + Mono 字体（JetBrains Mono）展示技术数据 + 应用→资源拓扑。

设计系统文件：`frontend/console-user/src/styles/theme.css`（CSS token、Element Plus 深色覆盖、滚动条、呼吸点动画）。
字体：Inter（正文）+ JetBrains Mono（数据/代码/端点）。

## 产品架构演进（打磨过程中确立）

控制台从"MaaS 专用页"演进为**应用为主线的统一控制台**（与用户对齐）：

- **应用为主线**（`/applications`，默认入口）：应用是一等公民，资源是应用的依赖，随应用生命周期联动。
- **资源中心**为横向视图：模型推理 / 消息队列 / 数据访问层 / 服务治理（后三者"即将上线"占位）。
- 应用详情（`/applications/:id`）的「资源绑定」Tab 是 signature：按类型分组展示绑定资源 + `+ 绑定资源`浮层（选类型申请）。
- 后端已预留：Core 插件契约让每个产品域成为独立插件，未来前端导航可由插件动态生成。

## 10 轮打磨明细

| 轮次 | 内容 |
|------|------|
| R1 | 设计系统（CSS token / 字体 / Element Plus 深色主题覆盖）+ 壳布局（侧栏+顶栏+主区） |
| R2 | 模型市场卡片网格（分类筛选、模型图标、活跃实例呼吸指示、hover 上浮） |
| R3 | 部署页 → 状态卡片视图（呼吸状态灯、副本/GPU/吞吐、可复制端点、操作） |
| R4 | Playground 聊天式重设计（消息气泡、流式光标、打字指示、底部输入器、Enter 发送） |
| R5 | API 密钥页（横幅、掩码密钥复制、状态徽章、吊销） |
| R6 | 用量页（4 统计卡 + SVG sparkline、Token 趋势折线、模型分布条形） |
| R7 | **范式重构**：应用为主线导航 + 应用列表页（环境/健康/资源摘要/新建卡片） |
| R8 | 应用详情页（资源绑定分组 + 绑定浮层 + 概览资源拓扑）|
| R9 | 路由切换淡入过渡、Element Plus 组件深色精修、reduced-motion 支持 |
| R10 | 响应式（窄屏自动图标化侧栏）、一致性扫描、构建验证通过 |

## 验证

- `pnpm --filter @paas/console-user build` 通过（vue-tsc 类型检查 + 生产构建）。
- 端到端：Playground 流式推理经真实 Gateway（echo provider）回显验证。
- 每轮均经 Playwright 截图评估（截图为临时验证产物，未入库）。
