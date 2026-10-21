<script setup lang="ts">
import { apiError } from '@/api'
import StatePanel from './StatePanel.vue'
// 交付·构建与镜像（v2 原生）：BuildRun 列表（状态/分支/触发）+ 镜像 digest（不可变真源）。
// 待办 build_failed 直达此处排查。
import { onMounted, ref, nextTick, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { listBuildRuns, listImages, type BuildRun, type Image } from '@/api/pipeline'
import { statusOf, tagTypeToCls, BUILD_STATUS } from '@/composables/useStatus'

const props = defineProps<{ appId: string }>()
const router = useRouter()
const route = useRoute()
async function focusImage() { await nextTick(); const id = route.query.image; if (typeof id === 'string') { const row = document.getElementById('image-'+id); row?.scrollIntoView({block:'center'}); row?.focus() } }
watch(() => route.query.image, focusImage)

const builds = ref<BuildRun[]>([])
const images = ref<Image[]>([])
const loading = ref(true)
const error = ref('')

async function load() {
  loading.value = true
  error.value = ''
  try {
    const [bs, is] = await Promise.all([
      listBuildRuns(props.appId),
      listImages(props.appId),
    ])
    builds.value = bs
    images.value = is
  } catch (e) {
    error.value = apiError(e, '无法加载交付配置')
  } finally {
    loading.value = false
    await focusImage()
  }
}
onMounted(load)

function fmtAgo(iso: string): string {
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ''
  const m = Math.floor(Math.max(0, Date.now() - t) / 60000)
  if (m < 1) return '刚刚'
  if (m < 60) return `${m} 分钟前`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h} 小时前`
  return `${Math.floor(h / 24)} 天前`
}
const shortDigest = (d: string) => (d ? d.replace('sha256:', '').slice(0, 12) : '-')
</script>

<template>
  <div class="card">
    <div class="card-head">
      <span class="card-title">构建与镜像</span>
      <span class="card-sub">构建产出不可变镜像（digest 寻址）</span>
    </div>
    <div v-if="loading" class="loading-block" v-loading="true"></div>
    <StatePanel v-else-if="error" title="加载失败" :description="error" error compact><button class="act" @click="load">重新加载</button></StatePanel>
    <template v-else>
      <div v-if="!builds.length && !images.length" class="empty-hint">
        暂无构建——流水线 build 阶段或手动触发构建后在此查看
      </div>
      <template v-if="builds.length">
        <div class="sec-title">构建记录（{{ builds.length }}）</div>
        <div class="table-scroll" data-table-scroll tabindex="0">
<table class="mini-table">
          <thead><tr><th>状态</th><th>分支</th><th>触发</th><th>说明</th><th>时间</th></tr></thead>
          <tbody>
            <tr v-for="b in builds" :key="b.id" class="clickable" @click="router.push(`/builds/${b.id}`)">
              <td>
                <span class="status" :class="tagTypeToCls(statusOf(BUILD_STATUS, b.status).type)">
                  {{ statusOf(BUILD_STATUS, b.status).label }}
                </span>
              </td>
              <td><span class="mono dim">{{ b.branch || '-' }}</span></td>
              <td class="dim">{{ b.trigger }}</td>
              <td class="t-msg">{{ b.message || '-' }}</td>
              <td class="dim">{{ fmtAgo(b.startedAt) }}</td>
            </tr>
          </tbody>
        </table>
</div>
      </template>
      <template v-if="images.length">
        <div class="sec-title">镜像（{{ images.length }}）</div>
        <div class="table-scroll" data-table-scroll tabindex="0">
<table class="mini-table">
          <thead><tr><th>Tag</th><th>Digest</th><th>来源分支</th></tr></thead>
          <tbody>
            <tr v-for="i in images" :key="i.id" :id="'image-'+i.id" tabindex="-1" :class="{'focus-row':route.query.image===i.id}">
              <td><span class="mono">{{ i.tag }}</span></td>
              <td><span class="mono dim" :title="i.digest">{{ shortDigest(i.digest) }}</span></td>
              <td><span class="mono dim">{{ i.branch || '-' }}</span></td>
            </tr>
          </tbody>
        </table>
</div>
      </template>
    </template>
  </div>
</template>

<style scoped>
@import '../views/list-page.css';
.loading-block { height: 120px; }
.dim { color: var(--text-faint); }
.mono { font-family: 'JetBrains Mono', monospace; }
</style>
