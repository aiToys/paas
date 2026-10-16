<script setup lang="ts">
import { confirmAction } from '@/composables/useConfirmAction'
// API 密钥（v2 原生闭环）：列表 + 创建（空 body 零提权，明文仅显示一次）+ 删除。
// 程序化调用平台 API 的凭证（OpenAI 兼容 /v1/* 与 /api/*）。
import { onMounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import { listApiKeys, createApiKey, deleteApiKey, type ApiKeyEntity } from '@/api/platform'
import { apiError } from '@/api'
import { copyText } from '@/utils/clipboard'

const keys = ref<ApiKeyEntity[]>([])
const loading = ref(true)
const creating = ref(false)
// 创建成功一次性展示完整明文（列表永远是掩码）。
const createdKey = ref('')
const showCreated = ref(false)

async function load() {
  loading.value = true
  try {
    keys.value = await listApiKeys()
  } catch (e) {
    ElMessage.error(apiError(e, '加载密钥失败'))
  } finally {
    loading.value = false
  }
}
onMounted(async () => {
  document.title = 'API 密钥 · 控制台'
  await load()
})

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
  if (!await confirmAction(`删除密钥 ${k.key} ？使用它的程序将立即失效。`)) return
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

    <div v-if="loading" class="loading-block" v-loading="true" element-loading-text="加载密钥…"></div>
    <div v-else-if="!keys.length" class="empty">暂无 API 密钥</div>
    <div class="table-scroll" data-table-scroll tabindex="0" v-else>
<table class="list-table">
      <thead>
        <tr>
          <th>密钥</th>
          <th>角色</th>
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
          <td class="dim">{{ (k.createdAt ?? '').slice(0, 10) || '—' }}</td>
          <td class="col-act">
            <button class="act danger" @click="remove(k)">删除</button>
          </td>
        </tr>
      </tbody>
    </table>
</div>

    <!-- 完整密钥一次性展示 -->
    <el-dialog v-model="showCreated" title="密钥已创建" width="480px" append-to-body>
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
.col-act { text-align: right; }
.created-key {
  padding: 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface-2);
  font-size: 13px;
  word-break: break-all;
}
</style>
