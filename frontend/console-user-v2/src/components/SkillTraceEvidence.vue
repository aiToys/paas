<script setup lang="ts">
import { computed } from 'vue'
import type { Span } from '@/composables/useSpanTree'
import { skillLoadEvidence } from '@/domain/skillTrace'

const props = defineProps<{ spans: Span[] }>()
defineEmits<{ select: [spanId: string] }>()
const loads = computed(() => skillLoadEvidence(props.spans))
const hasAgent = computed(() => props.spans.some((span) => span.tags?.['gen_ai.operation.name'] === 'invoke_agent'))
</script>

<template>
  <section v-if="loads.length || hasAgent" class="skill-evidence" aria-label="Skill 加载证据">
    <h3>Skill 加载证据</h3>
    <p class="note">记录本轮读取并加入待用上下文的技能。加载成功不代表后续推理成功或模型已遵循技能。</p>
    <p v-if="!loads.length" class="note">本链路没有上报 Skill 加载证据；不能据此判断本轮是否使用技能。</p>
    <details v-for="item in loads" :key="item.spanId">
      <summary>
        <strong>{{ item.name }}</strong>
        <span :class="item.status">{{ ({ loaded: '已加载', failed: '加载失败', unknown: '状态未知' })[item.status] }}</span>
        <span>Agent：{{ item.agent }}</span>
      </summary>
      <dl>
        <dt>Skill ID</dt><dd>{{ item.id || '未上报' }}</dd>
        <dt>上下文准备</dt><dd>{{ item.contextReady === true ? '已完成；推理结果需另行核对' : item.contextReady === false ? '未完成，后续依赖准备失败或中断' : '未上报' }}</dd>
        <dt>内容 SHA-256</dt><dd><code>{{ item.instructionsHash || '无内容快照' }}</code></dd>
        <dt>加载来源</dt><dd>{{ item.source || '未上报' }}</dd>
        <dt>副本更新时间</dt><dd>{{ item.updatedAt || '未上报' }}</dd>
        <dt>来源模板版本</dt><dd>{{ item.templateVersion ? `${item.templateSlug} @ ${item.templateVersion}` : '未提供；使用内容 SHA-256 核对修订' }}</dd>
        <template v-if="item.templateHash">
          <dt>原模板 SHA-256</dt><dd><code>{{ item.templateHash }}</code>（与当前正文哈希的计算范围不同）</dd>
        </template>
        <template v-if="item.error"><dt>加载错误</dt><dd>{{ item.error }}</dd></template>
      </dl>
      <button type="button" @click="$emit('select', item.spanId)">查看加载 Span</button>
    </details>
  </section>
</template>

<style scoped>
.skill-evidence { margin: 12px 0; padding: 14px; border: 1px solid var(--border); border-radius: 8px; background: var(--surface); }
h3 { margin: 0 0 8px; font-size: 14px; }
.note { color: var(--text-dim); font-size: 12px; margin: 8px 0; }
details { padding: 10px 0; border-top: 1px solid var(--border); }
summary { display: flex; flex-wrap: wrap; gap: 12px; cursor: pointer; font-size: 13px; }
.loaded { color: var(--success); }
.failed { color: var(--danger); }
.unknown { color: var(--text-dim); }
dl { display: grid; grid-template-columns: 120px minmax(0, 1fr); gap: 8px 12px; font-size: 12px; }
dt { color: var(--text-dim); }
dd { margin: 0; overflow-wrap: anywhere; }
button { cursor: pointer; color: var(--brand); border: 1px solid var(--border); background: var(--surface); border-radius: 4px; padding: 5px 10px; }
@media (max-width: 600px) { dl { grid-template-columns: 1fr; } }
</style>
