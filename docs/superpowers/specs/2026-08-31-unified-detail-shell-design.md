# 统一详情页骨架（DetailShell）设计

日期：2026-08-31 · 状态：已批准（方案 A）· 范围：console-user 纯前端

## 问题

console-user 有三种「打开详情」的表现形态，用户要学多遍：

| 形态 | 页面 | 现状 |
|---|---|---|
| 面包屑身份条 + tab 分组 | ApplicationDetail | 最成熟（P1.6 紧凑化 + P1.5 tab 三组分段） |
| `.detail-page + .crumb` 骨架 | DevOps 五单据（Build/Release/Change/Batch/PipelineRun） | 统一但各写一份，无 tab、无组件复用 |
| 各自为政 | DataServiceDetail（双返回按钮）/ ServiceDetail（裸文本返回）/ EnvironmentDetail（icon 返回）/ LaneDetail（自造 page-header）/ ConfigCenter 详情（同组件双路由无独立骨架） | 风格漂移最重 |

「点一个东西 → 打开详情」应全站只有一个语义：**面包屑返回 + 紧凑身份条 + tab 分组找能力**。

## 方案：DetailShell 通用组件 + 渐进迁移

### 1. DetailShell 组件（`src/components/DetailShell.vue`）

以 ApplicationDetail 的 crumb 结构为蓝本抽取，props/slots：

```
<DetailShell
  :crumbs="[{label:'应用', to:'/applications'}, {label:'app-cs'}]"  // 面包屑（末段高亮）
  :tags="[{type:'success', label:'健康'}, {type:'danger', label:'prod'}]"  // 身份 chips
  :loading="loading"                                                 // 骨架占位
  :fallback="{to:'/resources/db', label:'返回列表'}"                  // 实体不存在时的引导
>
  <template #actions>…</template>   // 右端按钮/⋯ 下拉
  <template #tabs>…</template>      // el-tabs / tab 分组（可选）
  <slot />                          // 页面主体
</DetailShell>
```

- 面包屑根 crumb 可点击返回；返回逻辑统一 `router.back()` 兜底 crumb.to（带 fallback 判定，书签直进也正确）。
- 统一骨架 CSS（crumb/tags/tabs 三段），各页删自造样式。
- **不强制 tab**：单维度详情（BuildDetail 等）无 tabs 也用同一壳——统一的是「返回+身份」语言，tab 是可选能力。

### 2. 迁移清单（按漂移程度排序）

| 批次 | 页面 | 改动 |
|---|---|---|
| P1 | DataServiceDetail / ServiceDetail / EnvironmentDetail / LaneDetail | 漂移最重，全量迁 DetailShell；DataServiceDetail 删双返回按钮 |
| P2 | DevOps 五单据 + PipelineRunPage | 已有 crumb 骨架，改为复用组件（删重复 CSS） |
| P3 | ConfigCenter 详情视图（`:nsId` 路由） | 补面包屑（列表 → ns 名）+ 身份条 |
| P4 | ApplicationDetail | 迁到 DetailShell（自身即蓝本，抽取后回接） |

### 3. 列表页「打开」语义统一（顺手对齐）

- 列表行点击 = 跳详情页（已有路由的：workloads 行 → 详情抽屉保留为快速预览，不承载完整详情——现状可接受，不强改）。
- 本次只统一详情页骨架，不动列表页结构（YAGNI，列表形态已各就绪）。

### 4. 不做（明确边界）

- 不收敛菜单结构（P1.6 已按频率分层，不动）。
- 不做命令面板（留后续）。
- 不动 console-admin（基座自带规范）。

## 验收

1. 全站详情页返回/身份/布局同构：面包屑根可返回、身份 chips 一致、tab（如有）分组风格一致。
2. `pnpm build` + lint 绿；书签直进详情页（无历史）返回不死链。
3. 各详情页数据逻辑零改动（纯壳迁移，行为等价）。
