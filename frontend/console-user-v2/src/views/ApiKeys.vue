<script setup lang="ts">
import { confirmAction } from '@/composables/useConfirmAction'
// API 密钥（v2 原生闭环）：列表 + 创建（空 body 零提权，明文仅显示一次）+ 删除。
// 程序化调用平台 API 的凭证（OpenAI 兼容 /v1/* 与 /api/*）。
// 专项06 §3：明文 ref 在关闭/切页/卸载时全路径清除；创建确认明确「与当前账号相同权限」；
// 接入说明给可执行示例；到期/最近使用后端暂无字段，不渲染假列。
import { onMounted, onUnmounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { listApiKeys, createApiKey, deleteApiKey, type ApiKeyEntity } from '@/api/platform'
import { apiError } from '@/api'
import { copyText } from '@/utils/clipboard'

const keys = ref<ApiKeyEntity[]>([])
const loading = ref(true)
// 列表失败文案（''=正常）：失败≠「暂无密钥」
const loadError = ref('')
const creating = ref(false)
// 创建成功一次性展示完整明文（列表永远是掩码）；任何关闭路径（按钮/X/Esc/遮罩）+
// 切页卸载都清除 ref（AC-02 完整版：不在组件状态里残留明文）。
const createdKey = ref('')
const showCreated = ref(false)

function clearCreated() {
  createdKey.value = ''
}

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
    createdKey.value = k.key
    showCreated.value = true
    await load()
  } catch (e) {
    ElMessage.error(apiError(e, '创建失败'))
  } finally {
    creating.value = false
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
      <span class="sub">程序化调用平台 API 的凭证</span>
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
          <th>权限</th>
          <th>用量归因</th>
          <th>创建于</th>
          <th class="col-act"></th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="k in keys" :key="k.id">
          <td><span class="s-name mono">{{ k.key }}</span></td>
          <td>
            <span v-for="r in k.roles" :key="r" class="cap-tag">{{ r }}</span>
          </td>
          <td>
            <span v-if="k.appId" class="mono dim">{{ k.appId }}</span>
            <span v-else class="dim">组织级（不归因应用）</span>
          </td>
          <td class="dim">{{ (k.createdAt ?? '').slice(0, 10) || '—' }}</td>
          <td class="col-act">
            <button class="act danger" @click="remove(k)">删除</button>
          </td>
        </tr>
      </tbody>
    </table>
</div>

    <!-- 完整密钥一次性展示：@close 覆盖按钮/X/Esc/遮罩全部关闭路径 -->
    <el-dialog v-model="showCreated" title="密钥已创建" width="480px" append-to-body @close="clearCreated">
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
