<script setup lang="ts">
// 泳道详情页（一等实体）：单泳道的深度视图。
// 服务部署表（就绪比/镜像）+ 最近 run（跳运行详情）+ trace 入口（带 lane 过滤跳可观测）+ 关闭泳道。
// 关闭 = 标记 closed + 后端同步回收该泳道全部工作负载（confirmDangerous 二次确认）。
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getLane, closeLane, type Lane, type LaneDetail } from '@/api/lane'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import { useEnvStore } from '@/stores/env'
import DetailShell from '@/components/DetailShell.vue'

const route = useRoute()
const router = useRouter()
const envStore = useEnvStore()

const detail = ref<LaneDetail | null>(null)
const loading = ref(true)
const closing = ref(false)

const lane = computed(() => detail.value?.lane as Lane | undefined)
// 泳道在生产环境不可关（联调只在测试环境；此处仅显示态，后端 allowProd 兜底）。
const isProd = computed(() => envStore.isProd)

const modeMeta: Record<string, { label: string; type: 'warning' | 'success' }> = {
  permanent: { label: '常驻（GC 不回收）', type: 'warning' },
  standard: { label: '常规（闲置可回收）', type: 'success' },
}

async function load() {
  loading.value = true
  try {
    detail.value = await getLane(route.params.id as string)
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    loading.value = false
  }
}
onMounted(load)

async function onClose() {
  if (!lane.value) return
  const n = detail.value?.workloads.length ?? 0
  if (n > 0) {
    try {
      await ElMessageBox.confirm(
        `将同步回收该泳道全部 ${n} 个工作负载（Deployment/Service 一并删除），不可恢复。`,
        '关闭泳道', { type: 'warning', confirmButtonText: '继续' },
      )
    } catch { return }
  }
  const ok = await confirmDangerous({
    action: '关闭泳道',
    target: lane.value.name,
    requireNameConfirm: true,
    isProd: isProd.value,
  })
  if (!ok) return
  closing.value = true
  try {
    await closeLane(lane.value.id)
    ElMessage.success('泳道已关闭，工作负载已回收')
    router.back()
  } catch (e) {
    ElMessage.error((e as Error).message)
  } finally {
    closing.value = false
  }
}

function fmtTime(s?: string) {
  return s ? new Date(s).toLocaleString() : '-'
}
</script>

<template>
  <div class="page lane-detail">
    <DetailShell
      :crumbs="[{ label: '泳道', to: '/environments' }, { label: lane?.name ?? (route.params.id as string) }]"
      :tags="lane ? [
        { label: lane.status === 'active' ? '活跃' : '已关闭', type: lane.status === 'active' ? 'success' : 'info' },
        { label: modeMeta[lane.mode]?.label ?? lane.mode, type: modeMeta[lane.mode]?.type },
        { label: `环境 ${lane.envId}` },
      ] : []"
      :loading="loading"
    >
      <template #actions>
        <el-button
          v-if="lane && lane.status === 'active'"
          size="small"
          @click="router.push(`/platform/observability?app=&lane=${lane.name}`)"
        >
          在可观测中查看
        </el-button>
        <el-button
          v-if="lane && lane.status === 'active'"
          size="small" type="danger" :loading="closing"
          @click="onClose"
        >
          关闭泳道
        </el-button>
      </template>
    </DetailShell>

    <div v-if="loading" class="loading">加载中…</div>
    <template v-else-if="lane">
      <div class="card">
        <h3 class="card-title">服务部署（{{ detail?.workloads.length ?? 0 }}）</h3>
        <el-table :data="detail?.workloads ?? []" size="small">
          <el-table-column prop="appId" label="应用" min-width="120">
            <template #default="{ row }">
              <router-link :to="`/applications/${row.appId}`" class="link">{{ row.appId }}</router-link>
            </template>
          </el-table-column>
          <el-table-column prop="service" label="服务" min-width="110">
            <template #default="{ row }">{{ row.service || row.name }}</template>
          </el-table-column>
          <el-table-column prop="image" label="镜像" min-width="200" show-overflow-tooltip />
          <el-table-column label="副本" width="90">
            <template #default="{ row }">{{ row.ready }}/{{ row.replicas }}</template>
          </el-table-column>
          <el-table-column prop="status" label="状态" width="100" />
        </el-table>
        <el-empty v-if="!detail?.workloads.length" description="该泳道暂无部署" :image-size="60" />
      </div>

      <div class="card">
        <h3 class="card-title">最近运行（run.Branch = 泳道名）</h3>
        <el-table :data="detail?.recentRuns ?? []" size="small">
          <el-table-column label="运行" min-width="150">
            <template #default="{ row }">
              <router-link :to="`/devops/runs/${row.id}`" class="link">{{ row.id }}</router-link>
            </template>
          </el-table-column>
          <el-table-column label="应用" min-width="120">
            <template #default="{ row }">
              <router-link :to="`/applications/${row.appId}`" class="link">{{ row.appId }}</router-link>
            </template>
          </el-table-column>
          <el-table-column prop="branch" label="分支" min-width="140" />
          <el-table-column prop="status" label="状态" width="100">
            <template #default="{ row }">
              <el-tag size="small" :type="row.status === 'succeeded' ? 'success' : row.status === 'failed' ? 'danger' : 'info'">
                {{ row.status }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="开始时间" width="170">
            <template #default="{ row }">{{ fmtTime(row.createdAt) }}</template>
          </el-table-column>
        </el-table>
        <el-empty v-if="!detail?.recentRuns.length" description="暂无运行记录" :image-size="60" />
      </div>
    </template>
  </div>
</template>

<style scoped>
.lane-detail { display: flex; flex-direction: column; gap: 16px; }
.card { background: var(--el-bg-color-overlay, var(--paas-card-bg, #fff)); border: 1px solid var(--paas-border, #e5e7eb); border-radius: 10px; padding: 16px; }
.card-title { font-size: 14px; margin: 0 0 12px; }
.loading { color: var(--el-text-color-secondary); padding: 40px; text-align: center; }
.link { color: var(--el-color-primary); text-decoration: none; }
</style>
