<script setup lang="ts">
import { confirmAction } from '@/composables/useConfirmAction'
// API 密钥（v2 原生闭环）：列表 + 创建（空 body 零提权，明文仅显示一次）+ 轮换 + 吊销 + 删除。
// 程序化调用平台 API 的凭证（OpenAI 兼容 /v1/* 与 /api/*）。
// 专项06 §3：明文 ref 在关闭/切页/卸载时全路径清除；创建确认明确「与当前账号相同权限」；
// 接入说明给可执行示例。D5-3：状态/到期/最近使用来自后端派生字段（state 由 KeyState 计算，
// 前端只做文案映射）；到期时间仅平台管理员可设置，此处如实展示「永不过期」。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import {
  listApiKeys,
  createApiKey,
  deleteApiKey,
  revokeApiKey,
  rotateApiKey,
  type ApiKeyEntity,
  type ApiKeyState
} from '@/api/platform'
import { apiError } from '@/api'
import { copyText } from '@/utils/clipboard'
import { useSessionStore } from '@/stores/session'

const session = useSessionStore()

const keys = ref<ApiKeyEntity[]>([])
const loading = ref(true)
// 列表失败文案（''=正常）：失败≠「暂无密钥」
const loadError = ref('')
const creating = ref(false)
// 创建成功一次性展示完整明文（列表永远是掩码）；任何关闭路径（按钮/X/Esc/遮罩）+
// 切页卸载都清除 ref（AC-02 完整版：不在组件状态里残留明文）。
const createdKey = ref('')
const showCreated = ref(false)
const createdTitle = ref('密钥已创建')

function clearCreated() {
  createdKey.value = ''
}

// —— 状态 / 时间文案（state 由后端派生，前端不重复「7 天内到期」判定）——
const STATE_LABEL: Record<ApiKeyState, string> = {
  active: '启用',
  expiring: '即将到期',
  expired: '已过期',
  revoked: '已吊销'
}
const stateLabel = (k: ApiKeyEntity) => STATE_LABEL[k.state ?? 'active']
const stateClass = (k: ApiKeyEntity) => `st st-${k.state ?? 'active'}`
const fmtTime = (s?: string) => (s ? new Date(s).toLocaleString() : '')
const fmtDay = (s?: string) => (s ? new Date(s).toLocaleDateString() : '')
// 可用 = 既未吊销也未过期（expiring 仍可用，只是提醒轮换）。
const usable = computed(
  () => keys.value.filter((k) => k.state !== 'revoked' && k.state !== 'expired').length
)

// 写权限与后端 apiKeyWritable 同源：tenant-admin 可管全租户，其余仅本人。
// 前端只做入口显隐，后端 403 兜底（轮换他人 Key 会返回继承其角色的明文 = 提权链）。
const canManage = (k: ApiKeyEntity) =>
  (session.profile?.roles ?? []).includes('tenant-admin') || k.userId === session.profile?.id

async function load() {
  loading.value = true
  loadError.value = ''
  try {
    keys.value = await listApiKeys()
  } catch (e) {
    loadError.value = apiError(e, '加载密钥失败')
  } finally {
    loading.value = false
  }
}
onMounted(async () => {
  document.title = 'API 密钥 · 控制台'
  await load()
})
onUnmounted(clearCreated)

async function create() {
  if (!await confirmAction('创建一个与当前账号权限相同的 API 密钥？完整值仅显示一次。')) return
  creating.value = true
  try {
    const k = await createApiKey()
    showPlaintext(k.key, '密钥已创建')
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '创建失败'))
  } finally {
    creating.value = false
  }
}

// 明文只在弹窗里出现一次；关闭即清除（@close → clearCreated）。
function showPlaintext(key: string, title: string) {
  createdKey.value = key
  createdTitle.value = title
  showCreated.value = true
}

// 轮换：新 Key 不继承原到期时间（后端语义），旧 Key 立即吊销。
async function rotate(k: ApiKeyEntity) {
  if (!await confirmAction(
    `为密钥 ${k.key} 生成同权限的新密钥并立即吊销旧密钥？使用旧密钥的程序会立即失效；` +
    '新密钥不继承原到期时间（默认永不过期），完整值仅显示一次。')) return
  try {
    const nk = await rotateApiKey(k.id)
    showPlaintext(nk.key, '密钥已轮换（旧密钥已吊销）')
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '轮换失败'))
  }
}

// 吊销（软吊销）：保留记录供审计，与删除（移除记录）语义不同。
async function revoke(k: ApiKeyEntity) {
  if (!await confirmAction(
    `吊销密钥 ${k.key} ？调用方将立即无法使用；记录会保留（区别于「删除」）。`)) return
  try {
    await revokeApiKey(k.id)
    ElMessage.success('已吊销')
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '吊销失败'))
  }
}

async function remove(k: ApiKeyEntity) {
  if (!await confirmAction(
    `删除密钥 ${k.key} ？它持有与当前账号相同的全部权限，使用它的程序将立即失效（后端即时拒绝）。`)) return
  try {
    await deleteApiKey(k.id)
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '删除失败'))
  }
}
</script>

<template>
  <div class="page">
    <div class="head-row">
      <h2 class="title">API 密钥</h2>
      <span class="sub">程序化调用平台 API 的凭证<span v-if="keys.length"> · {{ usable }} 个可用</span></span>
      <button class="head-create" :disabled="creating" @click="create">
        {{ creating ? '创建中…' : '新建密钥' }}
      </button>
    </div>

    <!-- 接入说明（专项06 §3）：环境变量 → 最小调用示例 → 排错入口 -->
    <div class="usage-guide">
      <div class="ug-title">接入说明</div>
      <div class="ug-row"><span class="ug-k">环境变量</span><code class="mono">export PAAS_API_KEY="sk-…"</code></div>
      <div class="ug-row"><span class="ug-k">最小调用</span><code class="mono">curl -H "Authorization: Bearer $PAAS_API_KEY" $PAAS_ADDR/v1/models</code></div>
      <div class="ug-row"><span class="ug-k">排错入口</span><a class="ug-link" href="/docs" target="_blank">API 文档（OpenAPI）</a><span class="ug-dim">401=密钥失效；403=权限不足（与创建者账号同权限）</span></div>
    </div>

    <div v-if="loading" class="loading-block" v-loading="true" element-loading-text="加载密钥…"></div>
    <div v-else-if="loadError" class="empty err">⚠ {{ loadError }}<button class="act" @click="load">重新加载</button></div>
    <div v-else-if="!keys.length" class="empty">暂无 API 密钥</div>
    <div class="table-scroll" data-table-scroll tabindex="0" v-else>
<table class="list-table">
      <thead>
        <tr>
          <th>密钥</th>
          <th>状态</th>
          <th>权限</th>
          <th>到期</th>
          <th>最近使用</th>
          <th>用量归因</th>
          <th>创建于</th>
          <th class="col-act"></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="k in keys" :key="k.id">
          <td>
            <span class="s-name mono">{{ k.key }}</span>
            <div class="dim">归属 {{ k.userId }}</div>
            <div v-if="k.rotatedFrom" class="dim">由 {{ k.rotatedFrom }} 轮换而来</div>
          </td>
          <td><span :class="stateClass(k)">{{ stateLabel(k) }}</span></td>
          <td>
            <span v-for="r in k.roles" :key="r" class="cap-tag">{{ r }}</span>
          </td>
          <td class="dim">{{ k.expiresAt ? fmtTime(k.expiresAt) : '永不过期' }}</td>
          <td class="dim">{{ k.lastUsedAt ? fmtTime(k.lastUsedAt) : '未使用' }}</td>
          <td>
            <span v-if="k.appId" class="mono dim">{{ k.appId }}</span>
            <span v-else class="dim">组织级（不归因应用）</span>
          </td>
          <td class="dim">{{ fmtDay(k.createdAt) || '—' }}</td>
          <td class="col-act">
            <template v-if="canManage(k)">
              <template v-if="k.state !== 'revoked'">
                <button class="act" @click="rotate(k)">轮换</button>
                <button class="act danger" @click="revoke(k)">吊销</button>
              </template>
              <button class="act danger" @click="remove(k)">删除</button>
            </template>
            <span v-else class="dim">他人密钥（只读）</span>
          </td>
        </tr>
      </tbody>
    </table>
</div>

    <!-- 完整密钥一次性展示：@close 覆盖按钮/X/Esc/遮罩全部关闭路径 -->
    <el-dialog v-model="showCreated" :title="createdTitle" width="480px" append-to-body @close="clearCreated">
      <p class="created-tip">完整值仅此一次显示，请立即复制保存：</p>
      <div class="created-key mono">{{ createdKey }}</div>
      <template #footer>
        <el-button @click="showCreated = false">关闭</el-button>
        <el-button type="primary" @click="copyText(createdKey, '密钥已复制')">复制</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
@import './list-page.css';
.empty.err { color: var(--danger); display: flex; flex-direction: column; gap: 10px; align-items: center; }
.col-act { text-align: right; }
.dim { color: var(--text-faint); font-size: 12px; }
/* 状态徽章：颜色与文案同源（state 由后端派生），不额外叠图标避免噪声 */
.st { display: inline-block; padding: 1px 7px; border-radius: 4px; font-size: 11px; white-space: nowrap; }
.st-active { color: var(--success); background: var(--success-soft); }
.st-expiring { color: var(--warning); background: var(--warning-soft); }
.st-expired { color: var(--danger); background: var(--danger-soft); }
.st-revoked { color: var(--text-faint); background: var(--surface-2); }
.created-key {
  padding: 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface-2);
  font-size: 13px;
  word-break: break-all;
}
.created-tip { margin: 0 0 8px; font-size: 13px; }
.usage-guide {
  padding: 12px 14px;
  margin-bottom: 16px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface-2);
  font-size: 12.5px;
}
.ug-title { font-weight: 600; margin-bottom: 8px; }
.ug-row { display: flex; align-items: center; gap: 10px; padding: 3px 0; flex-wrap: wrap; }
.ug-k { width: 64px; color: var(--text-faint); flex-shrink: 0; }
.ug-row code {
  padding: 2px 8px;
  border-radius: 6px;
  background: var(--surface);
  border: 1px solid var(--border);
  font-size: 12px;
  word-break: break-all;
}
.ug-link { color: var(--brand); }
.ug-link:hover { text-decoration: underline; }
.ug-dim { color: var(--text-faint); font-size: 12px; }
</style>
