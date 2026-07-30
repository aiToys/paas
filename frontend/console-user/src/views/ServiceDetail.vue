<script setup lang="ts">
// 服务治理 → 服务详情：实例列表（发现）+ 注册/注销实例 + 心跳。
// 生产注册/注销实例受 prod:write 保护（后端）；前端注销走 confirmDangerous（生产输入地址确认）。
import { onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'
import { confirmDangerous } from '@/composables/useDangerConfirm'

const route = useRoute()
const router = useRouter()
const envStore = useEnvStore()

interface Service {
  id: string
  name: string
  appId?: string
  envId: string
  protocol: string
  port: number
  desc?: string
}
interface Instance {
  id: string
  serviceId: string
  addr: string
  status: string
  laneId: string
  updatedAt: string
}
interface Env { id: string; name: string; type: string }

const svc = ref<Service | null>(null)
const instances = ref<Instance[]>([])
const envs = ref<Env[]>([])
const loading = ref(false)
const showCreate = ref(false)
const submitting = ref(false)
const form = ref({ addr: '', laneId: 'default' })

const envName = (id: string) => envs.value.find((e) => e.id === id)?.name ?? id
const isProd = () => envs.value.find((e) => e.id === svc.value?.envId)?.type === 'prod'

async function loadEnvs() {
  const resp = await fetchAuth('/api/environments')
  if (resp.ok) envs.value = (await resp.json()).data ?? []
}

async function load() {
  const id = route.params.id as string
  loading.value = true
  try {
    const resp = await fetchAuth(`/api/services/${id}`)
    if (resp.ok) {
      const json = await resp.json()
      svc.value = json.service
      instances.value = json.instances ?? []
    }
  } finally {
    loading.value = false
  }
}

function openCreate() {
  form.value = { addr: '', laneId: 'default' }
  showCreate.value = true
}

async function create() {
  if (!form.value.addr.trim()) {
    ElMessage.warning('请填写实例地址')
    return
  }
  submitting.value = true
  try {
    const resp = await fetchAuth(`/api/services/${svc.value!.id}/instances`, {
      method: 'POST',
      body: JSON.stringify(form.value),
    })
    if (resp.ok) {
      ElMessage.success('已注册实例')
      showCreate.value = false
      load()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '注册失败')
    }
  } finally {
    submitting.value = false
  }
}

async function heartbeat(row: Instance) {
  const resp = await fetchAuth(`/api/instances/${row.id}/heartbeat`, { method: 'PUT' })
  if (resp.ok) {
    ElMessage.success('心跳已更新')
    load()
  } else {
    const err = await resp.json().catch(() => ({}))
    ElMessage.error(err.error || '心跳失败')
  }
}

async function remove(row: Instance) {
  const ok = await confirmDangerous({
    action: '注销实例',
    target: row.addr,
    requireNameConfirm: isProd(),
  })
  if (!ok) return
  const resp = await fetchAuth(`/api/services/${svc.value!.id}/instances/${row.id}`, { method: 'DELETE' })
  if (resp.ok) {
    ElMessage.success('已注销实例')
    load()
  } else {
    const err = await resp.json().catch(() => ({}))
    ElMessage.error(err.error || '注销失败')
  }
}

onMounted(async () => {
  await Promise.all([loadEnvs(), load()])
})
watch(() => route.params.id, load)
</script>

<template>
  <div class="gov-page">
    <button class="back" @click="router.push('/platform/governance')">← 返回服务列表</button>
    <div v-if="svc" class="svc-head">
      <h2>{{ svc.name }}</h2>
      <div class="svc-meta">
        <el-tag size="small" :type="svc.protocol === 'grpc' ? 'warning' : 'info'">{{ svc.protocol.toUpperCase() }}:{{ svc.port }}</el-tag>
        <span class="kv">环境：<b>{{ envName(svc.envId) }}</b></span>
        <span v-if="svc.appId" class="kv">应用：<b>{{ svc.appId }}</b></span>
        <span v-if="svc.desc" class="kv">{{ svc.desc }}</span>
      </div>
    </div>

    <div class="inst-head">
      <span class="inst-title">服务实例（{{ instances.length }}）</span>
      <el-button type="primary" size="small" @click="openCreate">+ 注册实例</el-button>
    </div>

    <el-table :data="instances" v-loading="loading" size="default" empty-text="该服务暂无实例">
      <el-table-column label="地址" min-width="200">
        <template #default="{ row }"><span class="mono">{{ row.addr }}</span></template>
      </el-table-column>
      <el-table-column label="状态" width="110">
        <template #default="{ row }">
          <el-tag :type="row.status === 'healthy' ? 'success' : 'danger'" size="small">{{ row.status }}</el-tag>
        </template>
      </el-table-column>
      <el-table-column prop="laneId" label="泳道" width="110" />
      <el-table-column label="最后心跳" width="180">
        <template #default="{ row }">{{ new Date(row.updatedAt).toLocaleString() }}</template>
      </el-table-column>
      <el-table-column label="操作" width="150">
        <template #default="{ row }">
          <el-button text type="primary" size="small" @click="heartbeat(row)">心跳</el-button>
          <el-button text type="danger" size="small" @click="remove(row)">注销</el-button>
        </template>
      </el-table-column>
    </el-table>

    <el-dialog v-model="showCreate" title="注册实例" width="440px">
      <el-form label-width="80px">
        <el-form-item label="地址">
          <el-input v-model="form.addr" placeholder="host:port，如 10.0.1.20:8080" />
        </el-form-item>
        <el-form-item label="泳道">
          <el-input v-model="form.laneId" placeholder="default=基线（本期仅基线）" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showCreate = false">取消</el-button>
        <el-button type="primary" :disabled="submitting" @click="create">
          {{ submitting ? '注册中…' : '注册' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.gov-page {
  max-width: 1100px;
  margin: 0 auto;
}
.back {
  border: none;
  background: transparent;
  color: var(--text-faint);
  font-family: inherit;
  font-size: 13px;
  cursor: pointer;
  margin-bottom: 12px;
}
.back:hover {
  color: var(--text);
}
.svc-head {
  margin-bottom: 20px;
}
.svc-head h2 {
  margin: 0 0 8px;
  font-size: 18px;
}
.svc-meta {
  display: flex;
  align-items: center;
  gap: 14px;
  flex-wrap: wrap;
  font-size: 12.5px;
  color: var(--text-dim);
}
.kv b {
  color: var(--text);
}
.inst-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.inst-title {
  font-size: 14px;
  font-weight: 600;
}
</style>
