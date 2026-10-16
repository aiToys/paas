<script setup lang="ts">
import { computed, defineAsyncComponent } from 'vue'
import { useRoute } from 'vue-router'
import { aiSections } from '@/navigation'
import PageHeader from '@/components/PageHeader.vue'
const route = useRoute()
const section = computed(() => String(route.params.section || ({ kbs: 'knowledgebases', market: 'explore' } as Record<string, string>)[String(route.query.tab)] || route.query.tab || 'agents'))
const components = {
  agents: defineAsyncComponent(() => import('./ai/Agents.vue')),
  workflows: defineAsyncComponent(() => import('./ai/Workflow.vue')),
  knowledgebases: defineAsyncComponent(() => import('./ai/KnowledgeBases.vue')),
  tools: defineAsyncComponent(() => import('./ai/Tools.vue')),
  prompts: defineAsyncComponent(() => import('./ai/Prompts.vue')),
  skills: defineAsyncComponent(() => import('./ai/Skills.vue')),
  explore: defineAsyncComponent(() => import('./ai/Explore.vue')),
  runs: defineAsyncComponent(() => import('./AgentRuns.vue')),
}
const current = computed(() => components[section.value as keyof typeof components] ?? components.agents)
</script>
<template>
  <div class="ai-workspace">
    <PageHeader title="AI 开发" description="从模型到智能体与工作流。复用知识、工具和提示词，跟进执行结果。" eyebrow="AI 平台"><RouterLink to="/models" class="act">模型目录</RouterLink><RouterLink to="/playground" class="act">Playground</RouterLink></PageHeader>
    <nav class="section-nav" aria-label="AI 开发能力"><RouterLink v-for="s in aiSections" :key="s.id" :to="'/ai/' + s.id" :class="{ active: section === s.id }" :aria-current="section === s.id ? 'page' : undefined">{{ s.label }}</RouterLink></nav>
    <div class="ai-content"><component :is="current" :key="section" /></div>
  </div>
</template>
<style scoped>
.ai-content { min-width: 0; }
.ai-content :deep(.page-head), .ai-content :deep(.page-header) { margin-bottom: 18px; }
.ai-content :deep(h1) { font-size: 19px; }
.ai-content :deep(.el-table) { border: 1px solid var(--border); border-radius: var(--radius); }
</style>
