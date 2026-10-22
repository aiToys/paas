import type { Span } from '../composables/useSpanTree'

// 只解释实际采集的 Skill 加载 span；不根据 Agent 当前绑定回填历史证据。
export function skillLoadEvidence(spans: Span[]) {
  const byId = new Map(spans.map((span) => [span.id, span]))
  const hash = (value = '') => /^[0-9a-f]{64}$/.test(value) ? value : ''
  return spans
    .filter((span) => span.operation === 'paas.skill.load' && span.tags?.['paas.skill.load.mode'] === 'instruction_injection')
    .map((span) => {
      const tags = span.tags || {}
      const parent = byId.get(span.parentId || '')
      const ready = parent?.tags?.['paas.ai.context.ready']
      const status: 'failed' | 'loaded' | 'unknown' = span.isError || tags['paas.skill.load.status'] === 'failed'
        ? 'failed' : tags['paas.skill.load.status'] === 'loaded' ? 'loaded' : 'unknown'
      return {
        spanId: span.id,
        id: tags['paas.skill.id'] || '',
        name: tags['gen_ai.skill.name'] || tags['paas.skill.id'] || '未知技能',
        agent: parent?.tags?.['gen_ai.agent.name'] || tags['gen_ai.agent.id'] || '未上报',
        status,
        instructionsHash: hash(tags['paas.skill.instructions.sha256']),
        source: tags['gen_ai.skill.source.uri'] || '',
        templateVersion: tags['paas.skill.template.version'] || '',
        templateSlug: tags['paas.skill.template.slug'] || '',
        templateHash: hash(tags['paas.skill.template.sha256']),
        updatedAt: tags['paas.skill.updated_at'] || '',
        contextReady: ready === 'true' ? true : ready === 'false' ? false : undefined,
        error: span.errorMessage || tags['error.type'] || '',
      }
    })
}
