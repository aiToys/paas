<script setup lang="ts">
import { ref } from 'vue'
import { ElMessage } from 'element-plus'

interface Key {
  id: string
  name: string
  masked: string
  status: 'active' | 'revoked'
  createdAt: string
  lastUsed: string
}

const keys = ref<Key[]>([
  { id: 'k1', name: '默认密钥', masked: 'sk-paas-••••••••a1b2', status: 'active', createdAt: '2026-07-20', lastUsed: '2 分钟前' },
  { id: 'k2', name: 'CI/CD 流水线', masked: 'sk-paas-••••••••9f3e', status: 'active', createdAt: '2026-07-15', lastUsed: '1 小时前' },
  { id: 'k3', name: '内部测试（已吊销）', masked: 'sk-paas-••••••••2c7d', status: 'revoked', createdAt: '2026-06-01', lastUsed: '—' },
])

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已复制（掩码值，仅作参考）')
  } catch {
    ElMessage.error('复制失败')
  }
}

function revoke(k: Key) {
  ElMessage.warning(`已请求吊销「${k.name}」（演示，未真正执行）`)
}
</script>

<template>
  <div class="page">
    <div class="banner">
      <div>
        <div class="banner-title">API 密钥</div>
        <div class="banner-desc">密钥用于访问推理 API。创建后仅显示一次完整值，请妥善保存。</div>
      </div>
      <button class="create-btn">+ 创建密钥</button>
    </div>

    <div class="list">
      <div v-for="k in keys" :key="k.id" class="key-row">
        <div class="key-main">
          <div class="key-name">{{ k.name }}</div>
          <button class="key-val mono" @click="copy(k.masked)">
            {{ k.masked }}
            <span class="copy-tag">复制</span>
          </button>
        </div>
        <span class="badge" :class="k.status">
          <span v-if="k.status === 'active'" class="pulse-dot" />
          {{ k.status === 'active' ? '启用' : '已吊销' }}
        </span>
        <div class="key-meta">
          <div>创建 <span class="mono">{{ k.createdAt }}</span></div>
          <div>最后使用 <span class="mono">{{ k.lastUsed }}</span></div>
        </div>
        <div class="key-actions">
          <button v-if="k.status === 'active'" class="act danger" @click="revoke(k)">吊销</button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page {
  max-width: 960px;
  margin: 0 auto;
}
.banner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 22px 24px;
  background: linear-gradient(135deg, var(--brand-soft), transparent);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  margin-bottom: 20px;
}
.banner-title {
  font-size: 15px;
  font-weight: 600;
}
.banner-desc {
  font-size: 13px;
  color: var(--text-dim);
  margin-top: 4px;
}
.create-btn {
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
}
.list {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  overflow: hidden;
}
.key-row {
  display: grid;
  grid-template-columns: 1fr auto auto auto;
  align-items: center;
  gap: 20px;
  padding: 16px 20px;
  border-bottom: 1px solid var(--border);
}
.key-row:last-child {
  border-bottom: none;
}
.key-name {
  font-weight: 600;
  font-size: 13.5px;
  margin-bottom: 4px;
}
.key-val {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  border: none;
  background: var(--bg);
  padding: 4px 8px;
  border-radius: 6px;
  border: 1px solid var(--border);
  color: var(--text-dim);
  font-size: 12px;
  cursor: pointer;
  transition: border-color 0.12s;
}
.key-val:hover {
  border-color: var(--brand);
  color: var(--text);
}
.copy-tag {
  color: var(--brand);
}
.badge {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border-radius: 20px;
  font-size: 12px;
}
.badge.active {
  background: var(--success-soft);
  color: var(--success);
}
.badge.revoked {
  background: var(--surface-2);
  color: var(--text-faint);
}
.badge.revoked .pulse-dot {
  display: none;
}
.key-meta {
  font-size: 11.5px;
  color: var(--text-faint);
  text-align: right;
  line-height: 1.7;
}
.key-meta .mono {
  color: var(--text-dim);
}
.key-actions {
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
.act.danger:hover {
  border-color: var(--danger);
  color: var(--danger);
  background: var(--danger-soft);
}
</style>
