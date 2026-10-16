<script setup lang="ts">
// 统一详情页骨架 —— 全站「打开详情」唯一语义：面包屑返回 + 紧凑身份条 + 可选 tab。
// 蓝本取自 ApplicationDetail（P1.6 紧凑身份条），各详情页复用本壳，消除自造 header 漂移。
import { useRouter } from 'vue-router'
import Icon from '@/components/Icon.vue'

export interface Crumb {
  label: string
  to?: string // 有 to 的 crumb 可点击（根 crumb = 返回上级）
}

export interface ShellTag {
  label: string
  type?: 'success' | 'warning' | 'danger' | 'info' | 'primary'
  // 自定义 chip（如 ApplicationDetail 的 env/health）：给出 cls 由页面自带样式
  cls?: string
}

defineProps<{
  crumbs: Crumb[] // 末段为当前实体名（高亮，不可点）
  tags?: ShellTag[] // 身份 chips（状态/环境/类型），紧跟名称
  loading?: boolean // 骨架占位
  fallback?: { to: string; label: string } | null // 实体不存在时的引导（书签直进可感知）
}>()

const router = useRouter()

// 返回：优先历史回退（列表滚动位置不丢），书签直进（无历史）兜底根 crumb 路径。
function goBack(root: string | undefined) {
  if (window.history.state?.back) {
    router.back()
  } else if (root) {
    router.push(root)
  }
}
</script>

<template>
  <div class="shell">
    <div v-if="loading" class="shell-crumb skel" />
    <template v-else-if="!fallback">
      <header class="shell-crumb">
        <div class="shell-left">
          <button class="shell-back" title="返回" @click="goBack(crumbs[0]?.to)">
            <Icon name="chevron" :size="16" style="transform: rotate(-90deg)" />
          </button>
          <template v-for="(c, i) in crumbs" :key="i">
            <a v-if="c.to && i < crumbs.length - 1" class="shell-root" @click="router.push(c.to)">{{ c.label }}</a>
            <span v-else-if="i < crumbs.length - 1" class="shell-root">{{ c.label }}</span>
            <template v-if="i < crumbs.length - 1">
              <Icon name="chevron" :size="13" class="shell-sep" />
            </template>
          </template>
          <span class="shell-name">{{ crumbs[crumbs.length - 1]?.label }}</span>
          <span
            v-for="(t, i) in tags"
            :key="i"
            class="shell-chip"
            :class="[t.cls, t.type ? `chip-${t.type}` : '']"
          >{{ t.label }}</span>
        </div>
        <div class="shell-actions">
          <slot name="actions" />
        </div>
      </header>
      <slot name="tabs" />
      <slot />
    </template>
    <!-- 实体不存在（已删/跨租户/书签失效）：明确引导返回，不留静默空页 -->
    <div v-else class="shell-fallback">
      <p>内容不存在或已被删除</p>
      <el-button size="small" @click="router.push(fallback.to)">{{ fallback.label }}</el-button>
    </div>
  </div>
</template>

<style scoped>
.shell {
  max-width: 1100px;
  margin: 0 auto;
}
.shell-crumb {
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
.shell-crumb.skel {
  height: 44px;
  border: none;
  background: linear-gradient(90deg, var(--surface) 25%, var(--surface-2) 50%, var(--surface) 75%);
  background-size: 200% 100%;
  animation: shell-shimmer 1.4s infinite;
}
@keyframes shell-shimmer {
  to {
    background-position: -200% 0;
  }
}
@media (prefers-reduced-motion: reduce) {
  .shell-crumb.skel {
    animation: none;
  }
}
.shell-left {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}
.shell-back {
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
.shell-back:hover {
  border-color: var(--border-strong);
  color: var(--text);
}
.shell-root {
  font-size: 13px;
  color: var(--text-faint);
  cursor: pointer;
  white-space: nowrap;
}
a.shell-root:hover {
  color: var(--text);
}
.shell-sep {
  color: var(--text-faint);
  flex-shrink: 0;
}
.shell-name {
  font-size: 15px;
  font-weight: 650;
  letter-spacing: -0.01em;
  color: var(--text);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
/* 身份 chips：默认轻量描边，type 变体着色，cls 允许页面自定义（如 prodenv 红底） */
.shell-chip {
  padding: 1px 7px;
  border-radius: 4px;
  font-size: 11px;
  background: var(--surface-2, var(--surface));
  color: var(--text-dim);
  white-space: nowrap;
  flex-shrink: 0;
}
.shell-chip.chip-success {
  background: var(--success-soft);
  color: var(--success);
}
.shell-chip.chip-warning {
  background: rgba(230, 162, 60, 0.12);
  color: #e6a23c;
}
.shell-chip.chip-danger {
  background: rgba(244, 63, 94, 0.12);
  color: #f43f5e;
}
.shell-chip.chip-info {
  background: var(--surface-2, var(--surface));
  color: var(--text-dim);
}
.shell-chip.chip-primary {
  background: var(--success-soft);
  color: var(--success);
}
.shell-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}
.shell-fallback {
  padding: 48px 0;
  text-align: center;
  color: var(--text-dim);
}
</style>
