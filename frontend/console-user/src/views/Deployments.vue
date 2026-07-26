<script setup lang="ts">
import { ref } from 'vue'
import { ElMessage } from 'element-plus'

interface Deployment {
  id: string
  model: string
  short: string
  gradient: string
  replicasReady: number
  replicasTotal: number
  gpu: string
  status: 'running' | 'deploying' | 'failed'
  endpoint: string
  createdAt: string
  tps: number // tokens/s
}

const deployments = ref<Deployment[]>([
  { id: 'dep-1', model: 'Qwen2.5-7B-Instruct', short: 'Qwen', gradient: 'linear-gradient(135deg,#6366f1,#8b5cf6)', replicasReady: 2, replicasTotal: 2, gpu: 'A100 ×2', status: 'running', endpoint: 'https://api.paas.dev/v1/chat/qwen2.5-7b', createdAt: '2026-07-22', tps: 1280 },
  { id: 'dep-2', model: 'DeepSeek-V3', short: 'DS', gradient: 'linear-gradient(135deg,#10b981,#06b6d4)', replicasReady: 1, replicasTotal: 3, gpu: 'A100 ×3', status: 'deploying', endpoint: 'https://api.paas.dev/v1/chat/deepseek-v3', createdAt: '2026-07-25', tps: 0 },
  { id: 'dep-3', model: 'bge-m3', short: 'BGE', gradient: 'linear-gradient(135deg,#64748b,#475569)', replicasReady: 4, replicasTotal: 4, gpu: 'L4 ×4', status: 'running', endpoint: 'https://api.paas.dev/v1/embed/bge-m3', createdAt: '2026-07-18', tps: 8600 },
])

const statusMeta: Record<Deployment['status'], { label: string; cls: string }> = {
  running: { label: '运行中', cls: 'running' },
  deploying: { label: '部署中', cls: 'deploying' },
  failed: { label: '异常', cls: 'failed' },
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('端点已复制')
  } catch {
    ElMessage.error('复制失败')
  }
}
</script>

<template>
  <div class="page">
    <div class="summary">
      <div class="sum-item">
        <div class="sum-val mono">3</div>
        <div class="sum-label">部署总数</div>
      </div>
      <div class="sum-item">
        <div class="sum-val mono">7<span class="unit">/9</span></div>
        <div class="sum-label">就绪副本</div>
      </div>
      <div class="sum-item">
        <div class="sum-val mono">A100 ×9</div>
        <div class="sum-label">GPU 占用</div>
      </div>
      <button class="new-btn">+ 部署新模型</button>
    </div>

    <div class="grid">
      <article v-for="d in deployments" :key="d.id" class="dep-card">
        <div class="dep-head">
          <div class="m-icon" :style="{ background: d.gradient }">{{ d.short }}</div>
          <div class="dep-titles">
            <h3 class="dep-name">{{ d.model }}</h3>
            <div class="dep-id mono">{{ d.id }}</div>
          </div>
          <span class="status" :class="statusMeta[d.status].cls">
            <span v-if="d.status === 'running'" class="pulse-dot" />
            {{ statusMeta[d.status].label }}
          </span>
        </div>

        <div class="dep-stats">
          <div class="stat">
            <div class="stat-k">副本</div>
            <div class="stat-v mono">{{ d.replicasReady }}<span class="muted">/{{ d.replicasTotal }}</span></div>
          </div>
          <div class="stat">
            <div class="stat-k">GPU</div>
            <div class="stat-v mono">{{ d.gpu }}</div>
          </div>
          <div class="stat">
            <div class="stat-k">吞吐</div>
            <div class="stat-v mono">{{ d.tps.toLocaleString() }}<span class="muted"> tok/s</span></div>
          </div>
        </div>

        <div class="endpoint" @click="copy(d.endpoint)">
          <span class="ep-label">推理端点</span>
          <span class="ep-url mono">{{ d.endpoint }}</span>
          <span class="ep-copy">复制</span>
        </div>

        <div class="dep-foot">
          <span class="created">创建于 {{ d.createdAt }}</span>
          <div class="actions">
            <button class="act">扩缩容</button>
            <button class="act">日志</button>
            <button class="act danger">下线</button>
          </div>
        </div>
      </article>
    </div>
  </div>
</template>

<style scoped>
.page {
  max-width: 1200px;
  margin: 0 auto;
}
.summary {
  display: flex;
  align-items: center;
  gap: 40px;
  padding: 20px 24px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  margin-bottom: 20px;
}
.sum-val {
  font-size: 22px;
  font-weight: 700;
  letter-spacing: -0.02em;
}
.sum-val .unit,
.muted {
  color: var(--text-faint);
  font-weight: 500;
}
.sum-label {
  font-size: 12px;
  color: var(--text-faint);
  margin-top: 2px;
}
.new-btn {
  margin-left: auto;
  padding: 9px 18px;
  border: none;
  border-radius: var(--radius);
  background: var(--brand);
  color: #fff;
  font-family: inherit;
  font-size: 13px;
  font-weight: 600;
  cursor: pointer;
  box-shadow: 0 4px 14px var(--brand-glow);
  transition: background 0.12s;
}
.new-btn:hover {
  background: var(--brand-hover);
}

.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(380px, 1fr));
  gap: 16px;
}
.dep-card {
  padding: 20px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  transition: border-color 0.15s;
}
.dep-card:hover {
  border-color: var(--border-strong);
}
.dep-head {
  display: flex;
  align-items: center;
  gap: 12px;
}
.m-icon {
  width: 42px;
  height: 42px;
  flex-shrink: 0;
  border-radius: 10px;
  display: grid;
  place-items: center;
  font-weight: 700;
  font-size: 14px;
  color: #fff;
}
.dep-name {
  margin: 0;
  font-size: 14.5px;
  font-weight: 600;
}
.dep-id {
  font-size: 11.5px;
  color: var(--text-faint);
  margin-top: 2px;
}
.status {
  margin-left: auto;
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border-radius: 20px;
  font-size: 12px;
  font-weight: 500;
}
.status.running {
  background: var(--success-soft);
  color: var(--success);
}
.status.deploying {
  background: var(--warning-soft);
  color: var(--warning);
}
.status.failed {
  background: var(--danger-soft);
  color: var(--danger);
}
.status.deploying .pulse-dot,
.status.failed .pulse-dot {
  display: none;
}

.dep-stats {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 1px;
  margin: 16px 0;
  background: var(--border);
  border-radius: var(--radius);
  overflow: hidden;
}
.stat {
  padding: 12px;
  background: var(--surface-2);
}
.stat-k {
  font-size: 11px;
  color: var(--text-faint);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}
.stat-v {
  font-size: 15px;
  font-weight: 600;
  margin-top: 3px;
}

.endpoint {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 10px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  cursor: pointer;
  transition: border-color 0.12s;
}
.endpoint:hover {
  border-color: var(--brand);
}
.ep-label {
  font-size: 11px;
  color: var(--text-faint);
  flex-shrink: 0;
}
.ep-url {
  font-size: 11.5px;
  color: var(--text-dim);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
}
.ep-copy {
  font-size: 11px;
  color: var(--brand);
  flex-shrink: 0;
}

.dep-foot {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-top: 16px;
}
.created {
  font-size: 11.5px;
  color: var(--text-faint);
}
.actions {
  display: flex;
  gap: 6px;
}
.act {
  padding: 5px 11px;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: transparent;
  color: var(--text-dim);
  font-family: inherit;
  font-size: 12px;
  cursor: pointer;
  transition: all 0.12s;
}
.act:hover {
  background: var(--surface-2);
  color: var(--text);
}
.act.danger:hover {
  border-color: var(--danger);
  color: var(--danger);
  background: var(--danger-soft);
}
</style>
