<script setup lang="ts">
// 应用详情 - 镜像 tab：构建产物列表。digest 不可变真源，生产部署锁定。
// 「发布」按钮通知父组件切到发布 tab 并预选该镜像；支持 ?image= 深链自动展开定位行。
import { ref, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import { buildLink, repoLink } from '@/composables/useDevopsLinks'

const props = defineProps<{ appId: string; focusImageId?: string }>()
const emit = defineEmits<{
  (e: 'pick', image: Image): void
  (e: 'imageFocused'): void
}>()

interface Image {
  id: string
  registry: string
  tag: string
  digest: string
  source: string
  branch: string
  builtAt: string
  status: string
  buildRunId?: string
}

const router = useRouter()
const images = ref<Image[]>([])
const loading = ref(false)
const tableRef = ref<{ toggleRowExpansion: (row: Image, expanded?: boolean) => void }>()

async function load() {
  loading.value = true
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/images`)
    if (resp.ok) images.value = (await resp.json()).data ?? []
    else ElMessage.error(`加载镜像列表失败：HTTP ${resp.status}`)
  } catch (e) {
    ElMessage.error('加载镜像列表失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(() => props.appId, load)

// 深链定位：?image=<id> 自动展开该镜像行（从构建/发布/运行视图跳入）
watch(() => [props.focusImageId, images.value.length] as const, ([iid]) => {
  if (!iid) return
  const row = images.value.find((x) => x.id === iid)
  if (row) {
    tableRef.value?.toggleRowExpansion(row, true)
    emit('imageFocused')
  }
})

function shortDigest(d: string) {
  return d.length > 28 ? d.slice(0, 28) + '…' : d
}
</script>

<template>
  <div class="devops-tab">
    <div class="tab-head">
      <span class="tab-title">镜像（构建产物）</span>
      <span class="tab-hint">digest 不可变真源，生产部署锁定</span>
    </div>
    <el-table
ref="tableRef" :data="images" v-loading="loading" size="small" row-key="id"
      empty-text="尚无镜像，先在「构建」tab 触发构建"
>
      <el-table-column type="expand">
        <template #default="{ row }">
          <div class="expand-detail">
            <div><span class="k">完整 Digest</span><code class="mono">{{ row.digest }}</code></div>
            <div v-if="row.buildRunId">
              <span class="k">来源构建</span>
              <a class="link mono" @click="router.push(buildLink(row.buildRunId!))">{{ row.buildRunId }}</a>
            </div>
          </div>
        </template>
      </el-table-column>
      <el-table-column label="镜像" min-width="240">
        <template #default="{ row }">
          <div class="img-cell">
            <span class="mono">{{ row.registry }}:{{ row.tag }}</span>
            <span class="digest mono">{{ shortDigest(row.digest) }}</span>
          </div>
        </template>
      </el-table-column>
      <el-table-column prop="branch" label="分支" width="90" />
      <el-table-column label="来源 commit" width="110">
        <template #default="{ row }">
          <a class="mono link" @click="router.push(repoLink(props.appId))">{{ (row.source || '').slice(0, 8) }}</a>
        </template>
      </el-table-column>
      <el-table-column label="构建时间" width="160">
        <template #default="{ row }">{{ new Date(row.builtAt).toLocaleString() }}</template>
      </el-table-column>
      <el-table-column label="操作" width="130">
        <template #default="{ row }">
          <el-button size="small" type="primary" @click="emit('pick', row)">发布</el-button>
          <el-button v-if="row.buildRunId" size="small" text type="primary" @click="router.push(buildLink(row.buildRunId!))">来源构建</el-button>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>

<style scoped>
.link { color: var(--el-color-primary); cursor: pointer; }
.link:hover { text-decoration: underline; }
.expand-detail { padding: 4px 12px; display: grid; gap: 6px; font-size: 12.5px; }
.expand-detail .k { display: inline-block; min-width: 72px; color: var(--el-text-color-secondary); }
</style>
