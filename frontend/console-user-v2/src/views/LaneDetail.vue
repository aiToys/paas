<script setup lang="ts">
import { formatDateTime } from '@/utils/format'
import { apiError } from '@/api'
// 泳道详情页（一等实体）：单泳道的深度视图。
// 服务部署表（就绪比/镜像）+ 最近 run（跳运行详情）+ trace 入口（带 lane 过滤跳可观测）+ 关闭泳道。
// 关闭 = 标记 closed + 后端同步回收该泳道全部工作负载（confirmDangerous 二次确认）。
import { computed, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { getLane, getLaneAccess, closeLane, type Lane, type LaneDetail, type AccessEndpoint } from '@/api/lane'
import { confirmDangerous } from '@/composables/useDangerConfirm'
import { useEnvStore } from '@/stores/env'
import DetailShell from '@/components/DetailShell.vue'

const route = useRoute()
const router = useRouter()
const envStore = useEnvStore()

const detail = ref<LaneDetail | null>(null)
const access = ref<AccessEndpoint[]>([])
const loading = ref(true)
const closing = ref(false)

// 访问地址分区（spec 2026-10-17 §7.1 按访问意图分类；主入口=首条对外地址）。
const KIND_META: Record<string, { label: string; hint: string }> = {
  business: { label: '业务入口', hint: '本环境基线的对外域名' },
  preview: { label: '泳道联调入口', hint: '泳道版本对外域名；浏览器直达不带染色，全链路依赖 SDK' },
  internal: { label: '集群内部地址', hint: '仅集群内可达' },
}
const accessByKind = computed(() => {
  const m: Record<string, AccessEndpoint[]> = { business: [], preview: [], internal: [] }
  for (const ep of access.value) {
    ;(m[ep.kind] ??= []).push(ep)
  }
  return m
})

const lane = computed(() => detail.value?.lane as Lane | undefined)
// 泳道在生产环境不可关（联调只在测试环境；此处仅显示态，后端 allowProd 兜底）。
const isProd = computed(() => envStore.envs.find((e) => e.id === lane.value?.envId)?.type === 'prod')

const modeMeta: Record<string, { label: string; type: 'warning' | 'success' }> = {
  permanent: { label: '常驻（GC 不回收）', type: 'warning' },
  standard: { label: '常规（闲置可回收）', type: 'success' },
}

async function load() {
  loading.value = true
  try {
    detail.value = await getLane(route.params.id as string)
  } catch (e) {
    ElMessage.error(apiError(e))
  } finally {
    loading.value = false
  }
  // 地址分类独立加载（失败降级空，不阻断详情）。
  try {
    access.value = (await getLaneAccess(route.params.id as string)).endpoints ?? []
  } catch { access.value = [] }
}
onMounted(async () => { await Promise.all([load(), envStore.loadEnvs()]) })

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
    const res = await closeLane(lane.value.id)
    if (res.failedIds?.length) {
      // 部分回收失败（spec 2026-10-17 B）：不假报成功——留在页面呈现遗留，可重试关闭。
      ElMessage.warning(`已关闭，回收 ${res.reclaimed} 个；${res.failedIds.length} 个失败遗留（GC 兜底清扫，或重试关闭）`)
      await load()
    } else {
      ElMessage.success(`泳道已关闭，回收 ${res.reclaimed} 个工作负载`)
      router.back()
    }
  } catch (e) {
    ElMessage.error(apiError(e))
  } finally {
    closing.value = false
  }
}

const fmtTime = (t?: string) => {
  if (!t) return '-'
  const d = new Date(t)
  return Number.isNaN(d.getTime()) ? '-' : formatDateTime(d)
}

async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success('已复制')
  } catch {
    ElMessage.warning('复制失败，请手动选择复制')
  }
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
          @click="router.push(`/observability?app=&lane=${lane.name}`)"
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
              <router-link :to="`/products/${row.appId}`" class="link">{{ row.appId }}</router-link>
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

      <!-- 访问地址分区（spec 2026-10-17 §7）：主入口优先展示对外域名，集群内地址折叠说明 -->
      <div v-if="access.length" class="card">
        <h3 class="card-title">访问入口（{{ access.length }}）</h3>
        <div v-for="(eps, kind) in accessByKind" :key="kind">
          <div v-if="eps.length" class="access-group">
            <div class="access-kind">
              <span :class="['access-badge', `kind-${kind}`]">{{ KIND_META[kind]?.label ?? kind }}</span>
              <span class="access-hint">{{ KIND_META[kind]?.hint }}</span>
            </div>
            <div v-for="ep in eps" :key="ep.target + ep.url" class="access-row">
              <code class="access-url" :title="ep.url">{{ ep.url }}</code>
              <span class="access-target">目标 {{ ep.name || ep.target }} · 端口 {{ ep.port || 80 }}</span>
              <a v-if="kind !== 'internal'" class="access-open" :href="ep.url" target="_blank" rel="noopener">打开</a>
              <el-button size="small" link @click="copyText(ep.url)">复制</el-button>
              <div v-if="ep.note" class="access-note">{{ ep.note }}</div>
            </div>
          </div>
        </div>
      </div>

      <div class="card">
        <h3 class="card-title">最近运行（按真实部署目标关联）</h3>
        <el-table :data="detail?.recentRuns ?? []" size="small">
          <el-table-column label="运行" min-width="150">
            <template #default="{ row }">
              <router-link :to="`/runs/${row.id}`" class="link">{{ row.id }}</router-link>
            </template>
          </el-table-column>
          <el-table-column label="应用" min-width="120">
            <template #default="{ row }">
              <router-link :to="`/products/${row.appId}`" class="link">{{ row.appId }}</router-link>
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
.access-group { margin-bottom: 14px; }
.access-group:last-child { margin-bottom: 0; }
.access-kind { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
.access-badge { padding: 2px 10px; border-radius: 999px; font-size: 12px; font-weight: 500; }
.access-badge.kind-business { background: rgba(63, 185, 80, 0.12); color: #3fb950; }
.access-badge.kind-preview { background: rgba(79, 124, 255, 0.12); color: #4f7cff; }
.access-badge.kind-internal { background: rgba(139, 148, 158, 0.12); color: #8b949e; }
.access-hint { font-size: 12px; color: var(--el-text-color-secondary); }
.access-row { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; padding: 6px 0; border-top: 1px dashed var(--paas-border, #e5e7eb); }
.access-url { font-size: 13px; color: var(--el-text-color-primary); word-break: break-all; }
.access-target { font-size: 12px; color: var(--el-text-color-secondary); }
.access-open { font-size: 12px; color: var(--el-color-primary); text-decoration: none; }
.access-open:hover { text-decoration: underline; }
.access-note { flex-basis: 100%; font-size: 12px; color: var(--el-text-color-secondary); }
</style>
