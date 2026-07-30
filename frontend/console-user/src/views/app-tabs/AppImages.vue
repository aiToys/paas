<script setup lang="ts">
// 应用详情 - 镜像 tab：构建产物列表。digest 不可变真源，生产部署锁定。
// 「发布」按钮通知父组件切到发布 tab 并预选该镜像。
import { ref, onMounted, watch } from 'vue'
import { fetchAuth } from '@/api'

const props = defineProps<{ appId: string }>()
const emit = defineEmits<{ (e: 'pick', image: Image): void }>()

interface Image {
  id: string
  registry: string
  tag: string
  digest: string
  source: string
  branch: string
  builtAt: string
  status: string
}

const images = ref<Image[]>([])
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/images`)
    if (resp.ok) images.value = (await resp.json()).data ?? []
  } finally {
    loading.value = false
  }
}

onMounted(load)
watch(() => props.appId, load)

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
    <el-table :data="images" v-loading="loading" size="small" empty-text="尚无镜像，先在「构建」tab 触发构建">
      <el-table-column label="镜像" min-width="260">
        <template #default="{ row }">
          <div class="img-cell">
            <span class="mono">{{ row.registry }}:{{ row.tag }}</span>
            <span class="digest mono">{{ shortDigest(row.digest) }}</span>
          </div>
        </template>
      </el-table-column>
      <el-table-column prop="branch" label="分支" width="90" />
      <el-table-column label="来源 commit" width="120">
        <template #default="{ row }"><span class="mono">{{ (row.source || '').slice(0, 8) }}</span></template>
      </el-table-column>
      <el-table-column label="构建时间" width="160">
        <template #default="{ row }">{{ new Date(row.builtAt).toLocaleString() }}</template>
      </el-table-column>
      <el-table-column label="操作" width="80">
        <template #default="{ row }">
          <el-button size="small" type="primary" @click="emit('pick', row)">发布</el-button>
        </template>
      </el-table-column>
    </el-table>
  </div>
</template>
