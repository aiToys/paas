<script setup lang="ts">
// AI 编排广场：跨租户共享能力市场（对标 Dify Explore / Coze 商店）。
// 浏览（类型 tab + 分类 pill + 搜索）→ 卡片网格 → 详情抽屉（snapshot 预览）→ 安装 fork 到本租户。
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'
import { listMarket, getMarketItem, installFromMarket, CATEGORIES, ENTITY_TYPES, catLabel, type MarketItem } from '@/api/marketplace'

const router = useRouter()
const items = ref<MarketItem[]>([])
const loading = ref(false)
const entityType = ref('')
const category = ref('')
const q = ref('')

// 详情抽屉
const detail = ref<MarketItem | null>(null)
const detailLoading = ref(false)
const installing = ref(false)

async function load() {
  loading.value = true
  try {
    items.value = await listMarket(entityType.value, category.value, q.value)
  } catch (e) {
    ElMessage.error('加载广场失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

function pickType(t: string) {
  entityType.value = t
  load()
}

function pickCategory(c: string) {
  category.value = category.value === c ? '' : c
  load()
}

async function openDetail(it: MarketItem) {
  detail.value = it
  detailLoading.value = true
  try {
    // 详情拉全量（含 snapshot 预览）
    detail.value = await getMarketItem(it.id)
  } catch {
    // 列表元信息兜底
  } finally {
    detailLoading.value = false
    if (detail.value) buildPreview(detail.value)
  }
}

// snapshot 预览：按 entityType 抽关键字段
const preview = ref<{ label: string; value: string }[]>([])
function buildPreview(it: MarketItem) {
  preview.value = []
  const snap = it.snapshot as Record<string, any> | undefined
  if (!snap) return
  const fields: [string, string][] = [
    ['指令内容', snap.instructions],
    ['模板', snap.template],
    ['工具类型', snap.type],
    ['模型', snap.model],
    ['系统提示', snap.systemPrompt],
  ]
  for (const [label, v] of fields) {
    if (v) preview.value.push({ label, value: String(v) })
  }
  // Agent 整包：组装结构
  if (it.entityType === 'agent' && snap.agent) {
    const a = snap.agent as Record<string, any>
    const parts: string[] = []
    if (a.model) parts.push(`模型 ${a.model}`)
    if (Array.isArray(snap.skills) && snap.skills.length) parts.push(`${snap.skills.length} 个 Skill`)
    if (snap.prompt) parts.push('Prompt 模板')
    if (Array.isArray(snap.tools) && snap.tools.length) parts.push(`${snap.tools.length} 个工具`)
    preview.value.push({ label: '组装结构', value: parts.join(' + ') || '仅系统提示' })
  }
}

// 安装落点路由（fork 完成后可直达）
const entityTypeRoute: Record<string, string> = {
  skill: '/ai/skills', prompt: '/ai/prompts', tool: '/ai/tools', agent: '/ai/agents',
}

async function install(it: MarketItem) {
  await ElMessageBox.confirm(
    `安装「${it.name}」到本租户？${it.entityType === 'tool' ? '工具凭证不会随安装带入，装完需自行补填。' : ''}安装后与源头独立演进。`,
    '安装确认', { type: 'info' },
  )
  installing.value = true
  try {
    const res = await installFromMarket(it.id)
    ElMessage.success(`已安装为「${res.name}」`)
    detail.value = null
    router.push(entityTypeRoute[res.entityType] ?? '/ai/explore')
  } catch (e) {
    ElMessage.error('安装失败：' + (e as Error).message)
  } finally {
    installing.value = false
  }
}

function fmtDate(s: string) {
  return s ? new Date(s).toLocaleDateString() : ''
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <div>
        <h2>广场</h2>
        <p class="sub">跨租户共享的能力市场——浏览/安装他人发布的 Skill、Prompt、工具与 Agent 整包</p>
      </div>
    </div>

    <!-- 维度过滤条（复用 Observability 多维度范式） -->
    <div class="filters">
      <div class="type-tabs">
        <button
          v-for="t in ENTITY_TYPES" :key="t.value"
          class="type-tab" :class="{ on: entityType === t.value }"
          @click="pickType(t.value)"
        >{{ t.label }}</button>
      </div>
      <div class="cat-row">
        <button
          v-for="c in CATEGORIES" :key="c.value"
          class="cat-pill" :class="{ on: category === c.value }"
          @click="pickCategory(c.value)"
        >{{ c.label }}</button>
        <input
          v-model="q" class="search" placeholder="搜索名称/说明…"
          @keyup.enter="load"
        >
      </div>
    </div>

    <div v-if="loading" v-loading="true" class="grid-loading" />
    <div v-else-if="!items.length" class="empty-wrap">
      <el-empty description="广场还没有内容——去发布第一个能力包吧" :image-size="80">
        <el-button type="primary" @click="router.push('/ai/skills')">去创建</el-button>
      </el-empty>
    </div>
    <div v-else class="grid">
      <div v-for="it in items" :key="it.id" class="card" @click="openDetail(it)">
        <div class="card-head">
          <span class="card-name">{{ it.name }}</span>
          <el-tag size="small" type="info">{{ catLabel(it.category) }}</el-tag>
        </div>
        <div class="card-type">
          <el-tag size="small" :type="it.entityType === 'agent' ? 'primary' : it.entityType === 'skill' ? 'success' : 'warning'">
            {{ it.entityType }}
          </el-tag>
        </div>
        <p class="card-desc">{{ it.description || '（无说明）' }}</p>
        <div class="card-foot">
          <span class="meta">📦 {{ it.installs }} 次安装</span>
          <span class="meta">{{ it.publisherName || it.publisherTenant }} · {{ fmtDate(it.createdAt) }}</span>
        </div>
        <div class="card-actions">
          <el-button size="small" type="primary" @click.stop="install(it)">安装</el-button>
        </div>
      </div>
    </div>

    <!-- 详情抽屉 -->
    <el-drawer v-model="detail" :title="detail?.name" size="520px">
      <template v-if="detail">
        <div v-loading="detailLoading">
          <el-descriptions :column="1" border size="small">
            <el-descriptions-item label="类型">{{ detail.entityType }}</el-descriptions-item>
            <el-descriptions-item label="分类">{{ catLabel(detail.category) }}</el-descriptions-item>
            <el-descriptions-item label="发布者">{{ detail.publisherName || detail.publisherTenant }}</el-descriptions-item>
            <el-descriptions-item label="安装量">{{ detail.installs }}</el-descriptions-item>
          </el-descriptions>
          <template v-if="preview.length">
            <h4 class="sec">内容预览</h4>
            <div v-for="p in preview" :key="p.label" class="preview-block">
              <div class="preview-label">{{ p.label }}</div>
              <pre class="preview-val">{{ p.value.length > 800 ? p.value.slice(0, 800) + '…' : p.value }}</pre>
            </div>
          </template>
          <el-alert
            v-if="detail.entityType === 'tool'" type="warning" :closable="false" class="hint"
            title="工具凭证不随安装带入，装完请到工具管理补填"
          />
          <div class="drawer-actions">
            <el-button type="primary" :loading="installing" @click="install(detail)">安装到本租户</el-button>
          </div>
        </div>
      </template>
    </el-drawer>
  </div>
</template>

<style scoped>
.page { padding: 20px; }
.page-header { margin-bottom: 16px; }
.page-header h2 { margin: 0; }
.sub { margin: 4px 0 0; color: var(--el-text-color-secondary); font-size: 13px; }

.filters { display: flex; flex-direction: column; gap: 10px; margin-bottom: 20px; }
.type-tabs { display: flex; gap: 4px; border-bottom: 1px solid var(--el-border-color-lighter); }
.type-tab {
  padding: 8px 16px; border: none; background: transparent; cursor: pointer;
  color: var(--el-text-color-secondary); font-size: 13.5px; font-family: inherit;
  border-bottom: 2px solid transparent; margin-bottom: -1px;
}
.type-tab.on { color: var(--el-color-primary); border-bottom-color: var(--el-color-primary); }
.cat-row { display: flex; gap: 8px; align-items: center; flex-wrap: wrap; }
.cat-pill {
  padding: 4px 12px; border-radius: 12px; border: 1px solid var(--el-border-color);
  background: transparent; cursor: pointer; font-size: 12.5px; color: var(--el-text-color-regular); font-family: inherit;
}
.cat-pill.on { border-color: var(--el-color-primary); color: var(--el-color-primary); }
.search {
  margin-left: auto; padding: 5px 12px; border: 1px solid var(--el-border-color); border-radius: 6px;
  width: 220px; font-size: 13px; outline: none;
}
.search:focus { border-color: var(--el-color-primary); }

.grid-loading { min-height: 200px; }
.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 14px; }
.card {
  border: 1px solid var(--el-border-color-lighter); border-radius: 8px; padding: 14px;
  cursor: pointer; transition: box-shadow 0.15s, border-color 0.15s; display: flex; flex-direction: column; gap: 8px;
}
.card:hover { box-shadow: var(--el-box-shadow-light); border-color: var(--el-color-primary-light-5); }
.card-head { display: flex; justify-content: space-between; align-items: center; gap: 8px; }
.card-name { font-weight: 600; font-size: 14px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.card-desc {
  margin: 0; color: var(--el-text-color-secondary); font-size: 12.5px; line-height: 1.5;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; min-height: 38px;
}
.card-foot { display: flex; justify-content: space-between; color: var(--el-text-color-placeholder); font-size: 12px; }
.card-actions { display: flex; justify-content: flex-end; }
.empty-wrap { padding: 40px 0; }

.sec { margin: 16px 0 8px; color: var(--el-text-color-secondary); font-size: 13px; }
.preview-block { margin-bottom: 10px; }
.preview-label { font-size: 12px; color: var(--el-text-color-secondary); margin-bottom: 4px; }
.preview-val {
  margin: 0; white-space: pre-wrap; word-break: break-word; font-size: 12px; line-height: 1.5;
  background: var(--el-fill-color-light); padding: 8px; border-radius: 6px; max-height: 200px; overflow: auto;
}
.hint { margin-top: 12px; }
.drawer-actions { margin-top: 16px; display: flex; justify-content: flex-end; }
</style>
