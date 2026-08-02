<script setup lang="ts">
// 应用详情 - 发布 tab：创建发布（选镜像+环境+策略）+ 历史记录 + 回滚。
// 生产发布/回滚受 prod:write 保护（后端）；前端回滚走 confirmDangerous（生产输入名称确认）。
// 环境列表自加载；默认环境取全局 env store（顶栏当前环境）。
import { ref, onMounted, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'
import { confirmDangerous } from '@/composables/useDangerConfirm'

const props = defineProps<{ appId: string; pickedImageId?: string }>()
const envStore = useEnvStore()

interface Image {
  id: string
  registry: string
  tag: string
  digest: string
}
interface Release {
  id: string
  envId: string
  imageId: string
  strategy: string
  status: string
  previousImageId: string
  isRollback: boolean
  createdAt: string
}
interface Env {
  id: string
  name: string
  type: string
}

const releases = ref<Release[]>([])
const images = ref<Image[]>([])
const envs = ref<Env[]>([])
const loading = ref(false)
const showCreate = ref(false)
const form = ref({ envId: '', imageId: '', strategy: 'rolling' })

const strategies = [
  { value: 'rolling', label: '滚动' },
  { value: 'blue-green', label: '蓝绿（预留）' },
  { value: 'canary', label: '金丝雀（预留）' },
]

async function loadImages() {
  const resp = await fetchAuth(`/api/applications/${props.appId}/images`)
  if (resp.ok) images.value = (await resp.json()).data ?? []
}
async function loadEnvs() {
  const resp = await fetchAuth('/api/environments')
  if (resp.ok) envs.value = (await resp.json()).data ?? []
}
async function load() {
  loading.value = true
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/releases`)
    if (resp.ok) releases.value = (await resp.json()).data ?? []
  } finally {
    loading.value = false
  }
}

function openCreate(imageId?: string) {
  form.value = {
    envId: envStore.currentEnvId || '',
    imageId: imageId || '',
    strategy: 'rolling',
  }
  showCreate.value = true
}

async function create() {
  if (!form.value.envId || !form.value.imageId) {
    ElMessage.warning('请选择环境与镜像')
    return
  }
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/releases`, {
      method: 'POST',
      body: JSON.stringify(form.value),
    })
    if (resp.ok) {
      ElMessage.success('发布成功')
      showCreate.value = false
      load()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '发布失败')
    }
  } catch (e) {
    ElMessage.error('发布失败：' + (e as Error).message)
  }
}

async function rollback(r: Release) {
  const ok = await confirmDangerous({
    action: '回滚',
    target: r.id.slice(0, 12),
    requireNameConfirm: envStore.isProd,
  })
  if (!ok) return
  try {
    const resp = await fetchAuth(`/api/releases/${r.id}/rollback`, { method: 'POST' })
    if (resp.ok) {
      ElMessage.success('已回滚')
      load()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '回滚失败')
    }
  } catch (e) {
    ElMessage.error('回滚失败：' + (e as Error).message)
  }
}

const envName = (id: string) => envs.value.find((e) => e.id === id)?.name ?? id
const imgTag = (id: string) => images.value.find((i) => i.id === id)?.tag ?? (id || '').slice(0, 12)
const statusType = (s: string) =>
  (({ succeeded: 'success', failed: 'danger', 'rolled-back': 'info', deploying: 'warning', pending: 'info' } as Record<string, string>)[s] || 'info')

onMounted(async () => {
  await Promise.all([loadImages(), loadEnvs(), load()])
})
watch(() => props.appId, async () => {
  await Promise.all([loadImages(), load()])
})
// 镜像 tab 点「发布」预选镜像 -> 切到本 tab 触发创建弹窗
watch(() => props.pickedImageId, (id) => {
  if (id) openCreate(id)
})
</script>

<template>
  <div class="devops-tab">
    <div class="tab-head">
      <span class="tab-title">发布</span>
      <el-button type="primary" size="small" @click="openCreate()">+ 创建发布</el-button>
    </div>
    <el-table :data="releases" v-loading="loading" size="small" empty-text="尚无发布记录">
      <el-table-column label="状态" width="130">
        <template #default="{ row }">
          <el-tag :type="statusType(row.status)" size="small">{{ row.status }}</el-tag>
          <el-tag v-if="row.isRollback" type="warning" size="small" style="margin-left: 4px">回滚</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="环境" width="120">
        <template #default="{ row }">{{ envName(row.envId) }}</template>
      </el-table-column>
      <el-table-column label="镜像" width="150">
        <template #default="{ row }"><span class="mono">{{ imgTag(row.imageId) }}</span></template>
      </el-table-column>
      <el-table-column prop="strategy" label="策略" width="90" />
      <el-table-column label="发布时间" width="160">
        <template #default="{ row }">{{ new Date(row.createdAt).toLocaleString() }}</template>
      </el-table-column>
      <el-table-column label="操作" width="80">
        <template #default="{ row }">
          <el-button v-if="row.previousImageId" text type="warning" size="small" @click="rollback(row)">回滚</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="showCreate" title="创建发布" width="460px">
      <el-form label-width="70px">
        <el-form-item label="镜像">
          <el-select v-model="form.imageId" placeholder="选择镜像" style="width: 100%">
            <el-option v-for="im in images" :key="im.id" :label="`${im.registry}:${im.tag}`" :value="im.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="环境">
          <el-select v-model="form.envId" placeholder="选择目标环境" style="width: 100%">
            <el-option v-for="e in envs" :key="e.id" :label="e.name" :value="e.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="策略">
          <el-select v-model="form.strategy" style="width: 100%">
            <el-option v-for="s in strategies" :key="s.value" :label="s.label" :value="s.value" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showCreate = false">取消</el-button>
        <el-button type="primary" :disabled="!form.imageId || !form.envId" @click="create">发布</el-button>
      </template>
    </el-dialog>
  </div>
</template>
