# console-user 信息架构按操作频率重组 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** console-user 按操作频率重组：侧栏三段分层 + 资源组折叠记忆 + 应用强化；应用详情头部去动作化并压成面包屑紧凑条。

**Architecture:** 纯前端 3 文件改动，零后端。新增 `useNavState` composable 持久化资源组展开态；`App.vue` nav 重组三段（主操作/资源与能力/环境）+ 资源组可折叠 + 应用视觉强化；`ApplicationDetail.vue` 删 header 大卡片与冗余监控/部署按钮，改面包屑紧凑身份条，删除应用降权到 `⋯` 下拉。

**Tech Stack:** Vue 3 + Element Plus + TypeScript + Pinia + Vite（console-user）；验证用 vue-tsc --noEmit + pnpm build + k8s e2e（项目无 vitest）。

## Global Constraints

- **范围仅 console-user**，不动 console-admin（P1.4 已重构）、不动后端（零后端改动）。
- **不自动 git commit / 不动分支**（项目硬约定：`未经用户明确要求，不要执行 git commit / 分支操作`）。SDD 走工作区 diff 审查，不提交。
- **验证手段**：项目无 vitest，前端验证 = `pnpm exec vue-tsc --noEmit`（类型）+ `pnpm build` 三套（构建）+ k8s e2e curl（功能）。不写失败测试步骤。
- **注释语言中文**，与代码库现有注释一致。
- **响应契约**：`fetchJSON<T>` 自动解包 `{data:T}`（`@/api`）；`fetchAuth` 返原始 Response。ApplicationDetail 已用 fetchJSON，保持。
- **删除应用逻辑不变**：仍走 `confirmDangerous({action:'删除应用', target:app.name, requireNameConfirm:true})` + `DELETE /api/applications/{id}` + 跳 `/applications`。
- **样式约定**：复用现有 CSS 变量（`--brand`/`--brand-soft`/`--surface`/`--border`/`--text-faint`/`--text-dim`/`--success`/`--success-soft`/`--radius` 等）；复用现有 `.nav-section-label`/`.nav-bar`/`.pulse-dot` 类。
- **折叠态**：侧栏收起（`collapsed=true`）时折叠交互不生效（图标条模式，子项本就隐藏），仅展开态生效。

---

### Task 1: useNavState composable（资源组折叠态记忆）

**Files:**
- Create: `frontend/console-user/src/composables/useNavState.ts`

**Interfaces:**
- Produces: `useNavState()` 返回 `{ isOpen(group): boolean; toggle(group): void }`；group 取值 `'resources' | 'workloads' | 'platform'`。状态单例（模块级 ref，跨组件共享），持久化到 `localStorage` key `paas:nav-open:<group>`。默认全部折叠（key 不存在视为 false）。

- [ ] **Step 1: 新建 composable 文件**

创建 `frontend/console-user/src/composables/useNavState.ts`，完整内容：

```ts
// 侧栏资源组折叠态记忆：localStorage 持久化，跨组件共享（模块级单例）。
// 默认全部折叠（key 不存在视为 false）；用户展开过的组下次保持。
import { ref } from 'vue'

export type NavGroup = 'resources' | 'workloads' | 'platform'

const STORAGE_PREFIX = 'paas:nav-open:'

// 模块级单例状态：首次 import 时从 localStorage 读一次，之后跨组件共享。
const openSet = ref<Set<NavGroup>>(new Set(readAll()))

function readAll(): NavGroup[] {
  // localStorage 在 SSR/异常环境可能不可用，容错。
  try {
    const groups: NavGroup[] = ['resources', 'workloads', 'platform']
    return groups.filter((g) => localStorage.getItem(STORAGE_PREFIX + g) === '1')
  } catch {
    return []
  }
}

function persist(g: NavGroup) {
  try {
    localStorage.setItem(STORAGE_PREFIX + g, openSet.value.has(g) ? '1' : '0')
  } catch {
    /* 忽略写入失败（隐私模式等） */
  }
}

export function useNavState() {
  function isOpen(g: NavGroup): boolean {
    return openSet.value.has(g)
  }
  function toggle(g: NavGroup) {
    const next = new Set(openSet.value)
    if (next.has(g)) next.delete(g)
    else next.add(g)
    openSet.value = next
    persist(g)
  }
  return { isOpen, toggle }
}
```

- [ ] **Step 2: 类型检查**

Run: `cd frontend/console-user && pnpm exec vue-tsc --noEmit`
Expected: 无错误（新文件被 tsc 解析通过）。

---

### Task 2: App.vue 侧栏三段分层 + 资源组折叠 + 应用强化

**Files:**
- Modify: `frontend/console-user/src/App.vue`（`nav` 数组 84-122、`<template>` nav 区 147-174、`<style>` nav 相关 344-466）

**Interfaces:**
- Consumes: Task 1 的 `useNavState`（`isOpen`/`toggle`）、`NavGroup` 类型。
- Produces: 侧栏新视觉结构（主操作 / 资源与能力 / 环境），资源组可折叠，应用项强化。

- [ ] **Step 1: 改造 nav 数据结构为三段**

在 `App.vue` `<script setup>` 中，替换现有 `interface NavItem`（77-82 行）与 `const nav`（84-122 行）为下面这版。核心变化：① 加 `section`（分组标签）/`primary`（应用强化）/`group`（折叠组 key）字段；② 顺序改为 应用→DevOps→Playground→AI服务（主操作）→资源中心→工作负载→平台能力（资源与能力）→环境。

```ts
interface NavChild {
  label: string
  icon: string
  to: string
}
interface NavItem {
  label: string
  icon: string
  to?: string
  section?: 'main' | 'resources' // 分组标签归属（决定上方 section label）
  primary?: boolean              // 「应用」视觉强化
  group?: NavGroup               // 可折叠资源组的 key
  children?: NavChild[]
}

const nav: NavItem[] = [
  // —— 主操作：高频开发动作 ——
  { label: '应用', icon: 'deploy', to: '/applications', section: 'main', primary: true },
  { label: 'DevOps', icon: 'pipeline', to: '/devops', section: 'main' },
  { label: 'Playground', icon: 'playground', to: '/playground', section: 'main' },
  // AI 服务：模型是平台共享的「能力调用」（非租户私有存储资源），与 Playground 配套属高频。
  { label: 'AI 服务', icon: 'market', to: '/resources/models', section: 'main' },
  // —— 资源与能力：创建后少动，默认折叠，各自记忆展开态 ——
  {
    label: '资源中心', icon: 'database', section: 'resources', group: 'resources',
    children: [
      { label: '数据库', icon: 'database', to: '/resources/db' },
      { label: '缓存', icon: 'zap', to: '/resources/cache' },
      { label: '消息队列', icon: 'message', to: '/resources/mq' },
      { label: '对象存储', icon: 'storage', to: '/resources/storage' },
      { label: '向量数据库', icon: 'layers', to: '/resources/vector' },
      { label: '搜索引擎', icon: 'search', to: '/resources/search' },
    ],
  },
  {
    label: '工作负载', icon: 'server', section: 'resources', group: 'workloads',
    children: [
      { label: '服务', icon: 'server', to: '/workloads/services' },
      { label: '任务', icon: 'job', to: '/workloads/jobs' },
      { label: '定时', icon: 'clock', to: '/workloads/cronjobs' },
    ],
  },
  {
    label: '平台能力', icon: 'service', section: 'resources', group: 'platform',
    children: [
      { label: '服务治理', icon: 'service', to: '/platform/governance' },
      { label: '配置中心', icon: 'layers', to: '/platform/config-center' },
      { label: '可观测', icon: 'activity', to: '/platform/observability' },
      { label: '安全', icon: 'shield', to: '/platform/security' },
    ],
  },
  // —— 环境：物理隔离单元（管理面 + 跨环境总览）——
  { label: '环境', icon: 'shield', to: '/environments' },
]
```

在 `<script setup>` 顶部 import 区加：
```ts
import { useNavState, type NavGroup } from '@/composables/useNavState'
```
并在 `const settings = [...]` 之前加：
```ts
const { isOpen: navGroupOpen, toggle: toggleNavGroup } = useNavState()
```

再加一个 computed，算出每个 section 首次出现的索引（用于模板插 section label）：
```ts
// section 首项索引：模板据此在对应项前插分组 label（主操作 / 资源与能力）。
const sectionStarts = computed(() => {
  const map: Record<string, number> = {}
  nav.forEach((item, i) => {
    if (item.section && !(item.section in map)) map[item.section] = i
  })
  return map
})
const sectionLabel: Record<string, string> = { main: '主操作', resources: '资源与能力' }
```
（`computed` 已在文件顶部 import，确认存在；若未 import 则补 `computed`。）

- [ ] **Step 2: 改造 template nav 区**

替换 `<nav class="nav">` 内的 `<template v-for="item in nav">` 块（147-173 行）为下面这版。核心变化：① 在 section 首项前插 `.nav-section-label`；② 资源组（`item.group` 存在）的标题改成可点击 toggle，`sub-nav` 用 `v-show` 控制展开，标题右侧加 chevron 旋转标记；③ 「应用」加 `.primary` class。

```html
      <nav class="nav">
        <template v-for="(item, i) in nav" :key="item.label">
          <!-- section 分组 label（主操作 / 资源与能力） -->
          <div
            v-if="item.section && sectionStarts[item.section] === i && !collapsed"
            class="nav-section-label"
          >
            {{ sectionLabel[item.section] }}
          </div>

          <!-- 普通导航项（含应用强化） -->
          <RouterLink
            v-if="item.to"
            :to="item.to"
            class="nav-item"
            :class="{ active: isActive(item.to!), primary: item.primary }"
          >
            <span class="nav-bar" />
            <Icon :name="item.icon" :size="19" class="nav-icon" />
            <span v-if="!collapsed" class="nav-label">{{ item.label }}</span>
          </RouterLink>

          <!-- 可折叠资源组 -->
          <div v-else class="nav-group" :class="{ open: item.group && navGroupOpen(item.group) }">
            <div class="nav-item static group-title" @click="item.group && toggleNavGroup(item.group)">
              <Icon :name="item.icon" :size="19" class="nav-icon" />
              <span v-if="!collapsed" class="nav-label">{{ item.label }}</span>
              <Icon
                v-if="!collapsed && item.group"
                name="chevron"
                :size="14"
                class="group-chev"
              />
            </div>
            <div v-if="!collapsed" v-show="item.group && navGroupOpen(item.group)" class="sub-nav">
              <RouterLink
                v-for="c in item.children"
                :key="c.to"
                :to="c.to"
                class="sub-item"
                :class="{ active: isActive(c.to) }"
              >
                <Icon :name="c.icon" :size="15" />
                <span>{{ c.label }}</span>
              </RouterLink>
            </div>
          </div>
        </template>
      </nav>
```

- [ ] **Step 3: 加样式（资源组折叠交互 + 应用强化）**

在 `<style scoped>` 的 `.sub-nav {...}` 规则之后（约 418-437 行附近）追加：

```css
/* 可折叠资源组标题：可点击 + chevron 展开/收起标记 */
.nav-item.group-title {
  cursor: pointer;
  user-select: none;
}
.nav-item.group-title:hover {
  background: var(--surface);
  color: var(--text-dim);
}
.group-chev {
  margin-left: auto;
  color: var(--text-faint);
  transition: transform 0.18s;
}
.nav-group.open .group-chev {
  transform: rotate(90deg);
}

/* 「应用」强化：brand 色左条常显 + brand 色文字 + 字重 600（主线锚点） */
.nav-item.primary {
  color: var(--brand);
  font-weight: 600;
}
.nav-item.primary .nav-bar {
  transform: translateY(-50%) scaleY(1);
  background: var(--brand);
}
.nav-item.primary:hover {
  background: var(--brand-soft);
  color: var(--brand);
}
.nav-item.primary.active {
  background: var(--brand-soft);
  color: var(--brand);
}
```

确认 `Icon` 组件支持 `name="chevron"`（ApplicationDetail.vue 已用 `<Icon name="chevron" ...>`，确认存在）。chevron 默认朝向需与 `.nav-group.open .group-chev { rotate(90deg) }` 配合：未展开 chevron 朝右（默认），展开朝下（旋转 90deg）。若 Icon 的 chevron 默认朝下，则把未展开态改 `rotate(-90deg)`、展开态 `rotate(0)`——以实际 Icon 形态为准，保证「未展开朝右、展开朝下」。

- [ ] **Step 4: 类型检查 + 构建**

Run: `cd frontend/console-user && pnpm exec vue-tsc --noEmit`
Expected: 无错误。

Run: `cd frontend && pnpm build`
Expected: 三套（console-user/console-admin/landing）构建成功。

- [ ] **Step 5: 人工核对（dev server 或 build 产物）**

侧栏视觉应满足：主操作区「应用」（brand 色强化，左竖条常显）置顶，下接 DevOps/Playground/AI 服务；「资源与能力」label 下三个资源组默认折叠（仅显示组标题 + 右侧 chevron），点击组标题展开子项，chevron 旋转，刷新页面后展开态保持；环境项在资源组下方。

---

### Task 3: ApplicationDetail.vue 头部去动作化 + 面包屑紧凑条

**Files:**
- Modify: `frontend/console-user/src/views/ApplicationDetail.vue`（删除 `goDeploy` 268-270 / `goObservability` 304-306；改造 `<template>` header 区 337-360；概览 tab 加 desc 422；`<style>` head 相关 632-725）

**Interfaces:**
- Consumes: 无新依赖（复用现有 `confirmDangerous`/`statusLabel`/`fetchAuth`/`Icon`）。
- Produces: 面包屑紧凑身份条 + ⋯ 下拉（删除应用）；`goDeploy`/`goObservability` 死代码清理（删按钮后无其他引用，已核实仅 356-357 用）。

- [ ] **Step 1: 删除死代码 goDeploy / goObservability**

删除 `<script setup>` 中的两个函数（268-270 行 `goDeploy`、304-306 行 `goObservability`）。它们仅被头部「监控」「部署」按钮引用，本任务会一并删除这两个按钮。`goDevOps`（308-310）保留（DevOps tab cross-link 在用）。删除函数上方的注释（「头部「部署」按钮…」「跳可观测…」）一并清理。

- [ ] **Step 2: 改造 header 模板为面包屑紧凑条**

替换模板 337-360 行（从 `<button class="back">` 到 `</header>` 结束），改为：

```html
    <div v-if="loading" class="crumb skel-bar" />
    <template v-else-if="app">
      <header class="crumb">
        <div class="crumb-left">
          <button class="crumb-back" @click="router.push('/applications')" title="返回应用列表">
            <Icon name="chevron" :size="16" style="transform: rotate(180deg)" />
          </button>
          <span class="crumb-root">应用</span>
          <Icon name="chevron" :size="13" class="crumb-sep" />
          <span class="crumb-name">{{ app.name }}</span>
          <span class="env" :class="{ prodenv: app.env === 'prod' }">{{ app.env }}</span>
          <span class="health"><span class="pulse-dot" /> {{ statusLabel[app.status] ?? app.status }}</span>
        </div>
        <el-dropdown trigger="click" placement="bottom-end">
          <button class="crumb-more" title="更多操作">
            <Icon name="collapse" :size="18" />
          </button>
          <template #dropdown>
            <el-dropdown-menu>
              <el-dropdown-item class="danger-item" @click="deleteApp">🗑 删除应用</el-dropdown-item>
            </el-dropdown-menu>
          </template>
        </el-dropdown>
      </header>
```

说明：① 不再渲染 `.a-icon`（大图标）与 `.head-info`/`.name-row`/`.desc`/`.head-actions`；② 删掉 `[监控][部署][删除应用]` 三按钮，监控/部署能力交由 tab（「可观测」「部署」），删除应用进 `⋯` 下拉；③ 应用名以面包屑形式呈现（`应用 / <name>`），环境与健康 inline。`deleteApp`（314-334）函数保持不变。`el-dropdown` 已是项目既有用法（App.vue 顶栏在用）。

- [ ] **Step 3: 应用描述（app.desc）移到概览 tab 顶部**

在概览 tab 容器（`<div v-else-if="activeTab === '概览'" class="overview">`，422 行）的开头、`<div class="metrics">` 之前插入：

```html
        <p v-if="app.desc" class="overview-desc">{{ app.desc }}</p>
```

（应用描述从 header 移到概览，概览本就该有应用说明。）

- [ ] **Step 4: 替换 head 样式为面包屑紧凑条样式**

删除 `<style scoped>` 中仅服务于旧 header 的规则：`.head {...}`（632-641）、`.a-icon {...}` 与 `.a-icon.small {...}` 保留 `.a-icon.small`（概览拓扑还在用，453 行），但 `.a-icon`（大图标，658-668）删除；`.head-info`（675-677）、`.name-row`/`.name-row h2`（678-687）、`.head-actions`（707-710）删除。`.env`/`.health`/`.desc`/`.ghost`/`.primary`/`.danger` 若仅旧 header 使用则删除——核对：`.env`/`.health` 新面包屑仍在用，保留并按需微调；`.desc` 概览已用独立 `.overview-desc`，旧 `.desc` 删除；`.ghost`/`.primary`/`.danger` 旧按钮样式，确认无其他引用后删除（grep 全文件 `.ghost`/`class="primary"`/`class="danger"` 无其他命中则删）。

在 `<style scoped>` 中 `.skel-bar` 规则之后追加面包屑条样式：

```css
/* 面包屑紧凑身份条：替代旧 header 大卡片，回收首屏垂直空间。 */
.crumb {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 14px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  margin-bottom: 14px;
}
.crumb.skel-bar {
  height: 44px;
  border: none;
}
.crumb-left {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.crumb-back {
  display: grid;
  place-items: center;
  width: 28px;
  height: 28px;
  flex-shrink: 0;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-dim);
  cursor: pointer;
  transition: all 0.12s;
}
.crumb-back:hover {
  border-color: var(--border-strong);
  color: var(--text);
}
.crumb-root {
  font-size: 13px;
  color: var(--text-faint);
  cursor: pointer;
}
.crumb-root:hover {
  color: var(--text-dim);
}
.crumb-sep {
  color: var(--text-faint);
}
.crumb-name {
  font-size: 15px;
  font-weight: 650;
  letter-spacing: -0.01em;
  color: var(--text);
}
.crumb .env {
  padding: 1px 7px;
  border-radius: 4px;
  font-size: 11px;
  background: var(--success-soft);
  color: var(--success);
}
.crumb .env.prodenv {
  background: rgba(244, 63, 94, 0.12);
  color: #f43f5e;
}
.crumb .health {
  display: flex;
  align-items: center;
  gap: 5px;
  font-size: 12px;
  color: var(--success);
}
.crumb-more {
  display: grid;
  place-items: center;
  width: 30px;
  height: 30px;
  flex-shrink: 0;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-dim);
  cursor: pointer;
  transition: all 0.12s;
}
.crumb-more:hover {
  border-color: var(--border-strong);
  color: var(--text);
}
.overview-desc {
  margin: 0 0 14px;
  font-size: 13px;
  color: var(--text-dim);
  line-height: 1.6;
}
/* 下拉内删除项红色 */
:deep(.danger-item) {
  color: var(--el-color-danger, #f43f5e);
}
```

注：`collapse` Icon 在 App.vue 折叠按钮已用（横向三点/箭头形态），此处复用作 `⋯` 更多按钮。若 `collapse` 形态不适合做「更多」图标，改用纯文本 `⋯`：把 `<button class="crumb-more">` 内容换成 `<span style="font-size:20px;line-height:1">⋯</span>`，样式不变。以实际视觉合理为准（implementer 二选一）。

- [ ] **Step 5: 类型检查 + 构建**

Run: `cd frontend/console-user && pnpm exec vue-tsc --noEmit`
Expected: 无错误（特别注意删 `goDeploy`/`goObservability` 后无「is declared but never read」之外的残留引用错误；模板不再引用这两个函数）。

Run: `cd frontend && pnpm build`
Expected: 三套构建成功。

- [ ] **Step 6: 人工核对**

应用详情页：① 顶部为一行面包屑紧凑条（返回箭头 + 应用 / 应用名 + 环境 chip + 健康 + 右端 ⋯），无大图标卡片；② 首屏 tab 内容垂直空间较改前明显增多；③ 概览 tab 顶部显示应用描述；④ ⋯ 下拉点开有「🗑 删除应用」，点击走输入应用名确认对话框，确认后删除并跳应用列表；⑤ 原「监控」「部署」按钮已不存在（用「可观测」「部署」tab）。

---

### Task 4: k8s 部署 + e2e 验证 + CLAUDE.md 更新

**Files:**
- Modify: `CLAUDE.md`（前端架构 / 垂直切片章节补「console-user IA 按频率重组」小节）

- [ ] **Step 1: 全量构建 + 后端测试回归**

Run: `cd frontend && pnpm build`
Expected: 三套构建成功。

Run: `make test`
Expected: 后端全绿（本次零后端改动，仅回归确认）。

- [ ] **Step 2: k8s 部署**

Run: `./scripts/deploy-k8s.sh`
Expected: 退出码 0（构建前端 embed 镜像 + push registry + helm upgrade + rollout restart）。这是预授权操作（[[k8s-always-latest]]）。

- [ ] **Step 3: e2e 验证核心端点**

部署后 core 端点全 200（回归）：
```bash
curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer sk-acme-admin" http://paas.k8s.dd/api/applications
curl -s -o /dev/null -w "%{http_code}\n" -H "Authorization: Bearer sk-acme-admin" http://paas.k8s.dd/api/applications/app-cs
```
前端路由可达（embed serve）：
```bash
curl -s -o /dev/null -w "%{http_code}\n" http://paas.k8s.dd/console/
curl -s -o /dev/null -w "%{http_code}\n" http://paas.k8s.dd/console/applications
```
Expected: 200/200（API）、200/200（前端）。

- [ ] **Step 4: 更新 CLAUDE.md**

在 `CLAUDE.md`「垂直切片（已落地）」章节「P1.5 应用工作台」之后追加一小节（标题如「P1.6 console-user IA 按频率重组（侧栏分层 + 头部紧凑化，2026-08-06）」），概述：① 侧栏三段分层（主操作 应用强化/DevOps/Playground/AI 服务 → 资源与能力默认折叠记忆 → 环境）+ `useNavState` composable（localStorage 持久化）；② 应用详情头部去动作化，删 header 大卡片与冗余监控/部署按钮（tab 覆盖），改面包屑紧凑身份条，描述移概览 tab，删除应用降权到 ⋯ 下拉；③ 纯前端零后端。设计见 `docs/superpowers/specs/2026-08-06-console-user-ia-by-frequency.md`，留后续（命令面板 Cmd+K / 编辑应用端点）。

- [ ] **Step 5: 汇报**

向用户汇报：改动文件、验证结果（vue-tsc/build/test/e2e 全绿）、k8s 已部署。工作区改动保持未提交（等用户明确要求再提交）。

---

## 验证汇总（全 plan）

- 类型：`pnpm exec vue-tsc --noEmit`（console-user）无错。
- 构建：`cd frontend && pnpm build` 三套通过。
- 后端回归：`make test` 全绿（零后端改动）。
- 部署 e2e：`./scripts/deploy-k8s.sh` 退出 0 + 核心 API/前端路由 200。
- 文档：CLAUDE.md 补 P1.6 小节。
