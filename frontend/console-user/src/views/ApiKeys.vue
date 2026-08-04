<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchJSON } from '@/api'

// API Key 真实模型（core identity.APIKey，列表 key 已掩码）。
interface ApiKey {
  id: string
  tenantId: string
  userId: string
  roles: string[]
  key: string
  createdAt?: string
}

const keys = ref<ApiKey[]>([])
const loading = ref(false)
const creating = ref(false)
// 新建后明文仅显示一次：showCreated 控制弹窗，createdKey 持明文。
const showCreated = ref(false)
const createdKey = ref<ApiKey | null>(null)

async function load() {
  loading.value = true
  try {
    keys.value = (await fetchJSON<ApiKey[]>('/api/api-keys')) ?? []
  } catch (e) {
    ElMessage.error((e as Error).message || '加载失败')
  } finally {
    loading.value = false
  }
}

async function create() {
  try {
    await ElMessageBox.confirm(
      '将创建一个与当前账号权限相同的 API 密钥。创建后完整值仅显示一次，请妥善保存。',
      '创建 API 密钥',
      { type: 'warning', confirmButtonText: '创建', cancelButtonText: '取消' },
    )
  } catch {
    return
  }
  creating.value = true
  try {
    // 空 body：后端从会话 ctx 补全租户/用户 + roles 封顶为自身持有角色（零提权）。
    const k = await fetchJSON<ApiKey>('/api/api-keys', { method: 'POST', body: '{}' })
    createdKey.value = k
    showCreated.value = true
    ElMessage.success('密钥已创建，请立即复制保存')
    load()
  } catch (e) {
    ElMessage.error((e as Error).message || '创建失败')
  } finally {
    creating.value = false
  }
}

async function copy(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已复制')
  } catch {
    ElMessage.error('复制失败，请手动选择复制')
  }
}

async function revoke(k: ApiKey) {
  // API Key 租户级资产，不按物理环境隔离，用普通确认（不走 prod gated）。
  try {
    await ElMessageBox.confirm(`确认吊销密钥「${k.id}」？此操作不可逆。`, '吊销密钥', {
      type: 'warning',
      confirmButtonText: '确认吊销',
      cancelButtonText: '取消',
    })
  } catch {
    return
  }
  try {
    await fetchJSON(`/api/api-keys/${k.id}`, { method: 'DELETE' })
    ElMessage.success('已吊销')
    load()
  } catch (e) {
    ElMessage.error((e as Error).message || '吊销失败')
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="banner">
      <div>
        <div class="banner-title">API 密钥</div>
        <div class="banner-desc">密钥用于访问推理 API 与 /dp 数据面。创建后仅显示一次完整值，请妥善保存。</div>
      </div>
      <button class="create-btn" :disabled="creating" @click="create">
        {{ creating ? '创建中…' : '+ 创建密钥' }}
      </button>
    </div>

    <div v-loading="loading" class="list">
      <div v-if="!loading && keys.length === 0" class="empty">
        <div class="empty-icon">🔑</div>
        <div class="empty-title">暂无 API 密钥</div>
        <div class="empty-desc">创建一个密钥开始调用平台 API</div>
      </div>
      <div v-for="k in keys" :key="k.id" class="key-row">
        <div class="key-main">
          <div class="key-name">{{ k.id }}</div>
          <button class="key-val mono" @click="copy(k.key)">
            {{ k.key }}
            <span class="copy-tag">复制</span>
          </button>
          <div class="key-roles">
            <span v-for="r in k.roles" :key="r" class="role-tag">{{ r }}</span>
          </div>
        </div>
        <div class="key-meta">
          <div>归属 <span class="mono">{{ k.userId }}</span></div>
          <div>创建 <span class="mono">{{ k.createdAt ? new Date(k.createdAt).toLocaleString() : '—' }}</span></div>
        </div>
        <div class="key-actions">
          <button class="act danger" @click="revoke(k)">吊销</button>
        </div>
      </div>
    </div>

    <!-- 明文仅展示一次 -->
    <el-dialog v-model="showCreated" title="密钥已创建" width="560px" :close-on-click-modal="false">
      <template v-if="createdKey">
        <div class="warn-line">⚠️ 完整密钥仅显示这一次，关闭后无法再次查看。请立即复制保存。</div>
        <div class="plaintext-row">
          <code class="mono">{{ createdKey.key }}</code>
          <el-button type="primary" size="small" @click="copy(createdKey.key)">复制</el-button>
        </div>
        <div class="key-roles" style="margin-top: 12px">
          权限范围：
          <span v-for="r in createdKey.roles" :key="r" class="role-tag">{{ r }}</span>
        </div>
      </template>
      <template #footer>
        <el-button type="primary" @click="showCreated = false">我已保存</el-button>
      </template>
    </el-dialog>
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
.create-btn:disabled {
  opacity: 0.6;
  cursor: not-allowed;
}
.list {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  overflow: hidden;
  min-height: 120px;
}
.empty {
  padding: 56px 24px;
  text-align: center;
  color: var(--text-faint);
}
.empty-icon {
  font-size: 36px;
  margin-bottom: 8px;
}
.empty-title {
  font-size: 14px;
  color: var(--text-dim);
  font-weight: 600;
}
.empty-desc {
  font-size: 12px;
  margin-top: 4px;
}
.key-row {
  display: grid;
  grid-template-columns: 1fr auto auto;
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
  margin-bottom: 6px;
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
.key-roles {
  margin-top: 6px;
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}
.role-tag {
  font-size: 11px;
  padding: 1px 8px;
  border-radius: 10px;
  background: var(--surface-2);
  color: var(--text-dim);
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
.warn-line {
  color: var(--danger);
  font-size: 13px;
  margin-bottom: 12px;
}
.plaintext-row {
  display: flex;
  align-items: center;
  gap: 8px;
}
.plaintext-row code {
  flex: 1;
  padding: 8px 10px;
  background: var(--bg);
  border: 1px solid var(--border);
  border-radius: 6px;
  font-size: 12.5px;
  word-break: break-all;
}
</style>
