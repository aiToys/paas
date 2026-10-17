<script setup lang="ts">
import { formatDateTime } from '@/utils/format'
// 应用详情 - 发布 tab：创建发布（选镜像+环境+策略）+ 历史记录 + 回滚。
// 生产发布/回滚受 prod:write 保护（后端）；前端回滚走 confirmDangerous（生产输入名称确认）。
// 环境列表自加载；默认环境取全局 env store（顶栏当前环境）。
import { ref, computed, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth, fetchList, apiError } from '@/api'
import { useEnvStore } from '@/stores/env'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import { imageLink, releaseLink } from '@/composables/useDevopsLinks'
import StatePanel from './StatePanel.vue'
import { listServices, type ServiceEntity } from '@/api/service'
import { RELEASE_STATUS, statusOf } from '@/composables/useStatus'

const router = useRouter()

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

const services = ref<ServiceEntity[]>([])
const error = ref('')
const servicesError = ref('')
const releases = ref<Release[]>([])
const images = ref<Image[]>([])
const envs = ref<Env[]>([])
const loading = ref(false)
const showCreate = ref(false)
const form = ref({ envIds: [] as string[], imageId: '', strategy: 'rolling', serviceId: '', laneId: 'default' })
const submitting = ref(false)

const strategies = [
  { value: 'rolling', label: '滚动' },
  { value: 'blue-green', label: '蓝绿（预留）' },
  { value: 'canary', label: '金丝雀（预留）' },
]

async function loadImages() {
  images.value = await fetchList<Image>(`/api/applications/${props.appId}/images`)
}
async function loadEnvs() {
  envs.value = await fetchList<Env>('/api/environments')
}
async function load() {
  loading.value = true
  try {
    releases.value = await fetchList<Release>(`/api/applications/${props.appId}/releases`)
    error.value = ''
  } catch (e) { error.value = apiError(e)
  } finally {
    loading.value = false
  }
}

function openCreate(imageId?: string) {
  // 默认填顶栏当前环境（若非「全部」）。
  const defaults = envStore.currentEnvId ? [envStore.currentEnvId] : []
  form.value = {
    envIds: defaults,
    imageId: imageId || '',
    strategy: 'rolling',
    serviceId: services.value.length === 1 ? services.value[0]!.id : '',
    laneId: 'default',
  }
  showCreate.value = true
}

// 选中的 prod 环境名（用于生产发布二次确认提示）。
const selectedProdNames = computed(() =>
  form.value.envIds
    .map((id) => envs.value.find((e) => e.id === id))
    .filter((e): e is Env => !!e && e.type === 'prod')
    .map((e) => e.name),
)

async function create() {
  if (!form.value.envIds.length || !form.value.imageId || !form.value.serviceId || servicesError.value) {
    ElMessage.warning('请选择目标服务、环境与镜像')
    return
  }
  // 勾选含生产环境时显式 isProd 二次确认（按目标 env.type，覆盖顶栏 scope）。
  if (selectedProdNames.value.length) {
    const ok = await confirmDangerous({
      action: '发布',
      target: selectedProdNames.value.join('、'),
      requireNameConfirm: true,
      isProd: true,
    })
    if (!ok) return
  }
  submitting.value = true
  // 多环境发布：每环境独立 POST（后端每 env 独立基线，部分失败不互斥）。
  const results = await Promise.allSettled(
    form.value.envIds.map((envId) =>
      fetchAuth(`/api/applications/${props.appId}/releases`, {
        method: 'POST',
        body: JSON.stringify({ envId, imageId: form.value.imageId, strategy: form.value.strategy, serviceId: form.value.serviceId, service: services.value.find(s => s.id === form.value.serviceId)?.name, laneId: form.value.laneId.trim() || 'default' }),
      }).then(async (resp) => {
        if (!resp.ok) {
          const err = await resp.json().catch(() => ({}))
          throw new Error(err.error || `HTTP ${resp.status}`)
        }
        return envId
      }),
    ),
  )
  submitting.value = false
  const failed = results
    .map((r, i) => ({ r, envId: form.value.envIds[i] }))
    .filter((x) => x.r.status === 'rejected')
  if (failed.length === 0) {
    ElMessage.success(`已发布到 ${form.value.envIds.length} 个环境`)
    showCreate.value = false
    load()
  } else {
    const failedNames = failed.map((x) => envName(x.envId)).join('、')
    const ok = results.length - failed.length
    ElMessage.error(`${ok}/${results.length} 成功，失败：${failedNames}`)
    if (ok > 0) load()
  }
}

async function rollback(r: Release) {
  // 按资源所在 env.type 显式判定（覆盖顶栏 scope，防顶栏与资源环境不一致）。
  const isProdEnv = envs.value.find((e) => e.id === r.envId)?.type === 'prod'
  const ok = await confirmDangerous({
    action: '回滚',
    target: r.id.slice(0, 12),
    requireNameConfirm: isProdEnv,
    isProd: isProdEnv,
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
    ElMessage.error(apiError(e, '回滚失败'))
  }
}

const envName = (id: string) => envs.value.find((e) => e.id === id)?.name ?? id
const imgTag = (id: string) => images.value.find((i) => i.id === id)?.tag ?? (id || '').slice(0, 12)
const statusType = (s: string) => statusOf(RELEASE_STATUS, s).type
const statusLabel = (s: string) => statusOf(RELEASE_STATUS, s).label

onMounted(async () => {
  await Promise.all([loadImages(), loadEnvs(), load(), listServices(props.appId).then(v => { services.value = v; servicesError.value = '' })]).catch(e => { servicesError.value = apiError(e); ElMessage.error(servicesError.value) })
})
watch(() => props.appId, async () => {
  await Promise.all([loadImages(), load(), listServices(props.appId).then(v => { services.value = v })]).catch(e => { servicesError.value = apiError(e) })
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
    <StatePanel v-if="error" title="发布记录加载失败" :description="error" error><button class="act" @click="load">重试</button></StatePanel>
    <el-table v-else :data="releases" v-loading="loading" size="small" empty-text="尚无发布记录">
      <el-table-column label="状态" width="130">
        <template #default="{ row }">
          <el-tag :type="statusType(row.status)" size="small">{{ statusLabel(row.status) }}</el-tag>
          <el-tag v-if="row.isRollback" type="warning" size="small" style="margin-left: 4px">回滚</el-tag>
        </template>
      </el-table-column>
      <el-table-column label="环境" width="120">
        <template #default="{ row }">{{ envName(row.envId) }}</template>
      </el-table-column>
      <el-table-column label="镜像" width="150">
        <template #default="{ row }">
          <a class="mono link" @click="router.push(imageLink(props.appId, row.imageId))">{{ imgTag(row.imageId) }}</a>
        </template>
      </el-table-column>
      <el-table-column prop="strategy" label="策略" width="90" />
      <el-table-column label="发布时间" width="160">
        <template #default="{ row }">{{ formatDateTime(row.createdAt) }}</template>
      </el-table-column>
      <el-table-column label="操作" width="140">
        <template #default="{ row }">
          <el-button text type="primary" size="small" @click="router.push(releaseLink(row.id))">详情</el-button>
          <el-button v-if="row.status === 'succeeded' && row.previousImageId" text type="warning" size="small" @click="rollback(row)">回滚</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="showCreate" title="创建发布" width="460px">
      <StatePanel v-if="servicesError" title="发布目标加载失败" :description="servicesError" error />
      <el-form label-width="70px">
        <el-form-item label="目标服务" required><el-select v-model="form.serviceId" filterable placeholder="选择明确的服务"><el-option v-for="s in services" :key="s.id" :label="s.name" :value="s.id" /></el-select></el-form-item>
        <el-form-item label="泳道"><el-input v-model="form.laneId" placeholder="default" /></el-form-item>
        <el-form-item label="镜像">
          <el-select v-model="form.imageId" placeholder="选择镜像" style="width: 100%">
            <el-option v-for="im in images" :key="im.id" :label="`${im.registry}:${im.tag}`" :value="im.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="环境">
          <el-select v-model="form.envIds" multiple placeholder="可多选目标环境" style="width: 100%">
            <el-option v-for="e in envs" :key="e.id" :label="e.name" :value="e.id" />
          </el-select>
          <div v-if="selectedProdNames.length" class="prod-warn">⚠️ 含生产环境：{{ selectedProdNames.join('、') }}（发布需确认）</div>
        </el-form-item>
        <el-form-item label="策略">
          <el-select v-model="form.strategy" style="width: 100%">
            <el-option v-for="s in strategies" :key="s.value" :label="s.label" :value="s.value" />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showCreate = false">取消</el-button>
        <el-button type="primary" :disabled="!form.imageId || !form.envIds.length || !form.serviceId || !!servicesError" :loading="submitting" @click="create">
          发布（{{ form.envIds.length || 0 }} 环境）
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.link { color: var(--el-color-primary); cursor: pointer; }
.link:hover { text-decoration: underline; }
.prod-warn {
  margin-top: 6px;
  font-size: 12px;
  color: var(--el-color-danger);
  line-height: 1.4;
}
</style>
