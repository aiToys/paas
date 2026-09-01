<script setup lang="ts">
// 资源中心 → 知识库（RAG）：文档上传->切片->embedding->向量检索。
// KB 引用 dataservice vector(qdrant)+storage(minio) 实例（不自建），复用 MaaS embedding 模型。
// 租户私有；不绑物理环境（无 prod:write）。文档上传异步解析+索引，状态轮询 parsing->indexed/failed。
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { fetchJSON, fetchAuth } from '@/api'

interface KnowledgeBase {
  id: string; name: string
  vectorStoreRef: string; objectStoreRef: string
  embeddingModel: string; embeddingDim: number
  retrieverConfig?: { topK?: number; hybrid?: boolean; queryRewrite?: boolean; rerankerRef?: string }
  createdAt: string
}
interface Document {
  id: string; kbId: string; name: string; mime: string; status: string
  chunkCount: number; message?: string; createdAt: string
}
interface ChunkHit { chunk: { content: string; seq: number }; score: number }
interface DSItem { id: string; name: string; kind: string }
interface Agent { id: string; name: string; knowledgeBases: string[] | null }

const kbs = ref<KnowledgeBase[]>([])
const agents = ref<Agent[]>([])
const loading = ref(false)

// 被 Agent 引用计数（kb id -> 引用它的 Agent 名列表）
const usage = computed(() => {
  const m: Record<string, string[]> = {}
  for (const a of agents.value) {
    for (const kid of a.knowledgeBases || []) {
      ;(m[kid] ||= []).push(a.name)
    }
  }
  return m
})
const vectorDS = ref<DSItem[]>([])
const storageDS = ref<DSItem[]>([])

const DOC_STATUS_LABEL: Record<string, string> = { parsing: '解析中', indexed: '已索引', failed: '失败' }
const DOC_STATUS_TYPE: Record<string, string> = { parsing: 'warning', indexed: 'success', failed: 'danger' }

async function load() {
  loading.value = true
  try {
    const [k, a] = await Promise.all([
      fetchJSON<KnowledgeBase[]>('/api/knowledgebases'),
      fetchJSON<Agent[]>('/api/agents'),
    ])
    kbs.value = k
    agents.value = a
  } catch (e) {
    ElMessage.error('加载知识库失败：' + (e as Error).message)
  } finally {
    loading.value = false
  }
}

async function loadDS() {
  try {
    const [vs, ss] = await Promise.all([
      fetchJSON<DSItem[]>('/api/dataservices?kind=vector'),
      fetchJSON<DSItem[]>('/api/dataservices?kind=storage'),
    ])
    vectorDS.value = vs
    storageDS.value = ss
  } catch {
    // 数据服务未就绪不阻塞 KB 列表
  }
}

// 创建弹窗
const showCreate = ref(false)
const form = ref({
  name: '',
  vectorStoreRef: '',
  objectStoreRef: '',
  embeddingModel: 'text-embedding-v4',
  embeddingDim: 1024,
})
const submitting = ref(false)

function openCreate() {
  form.value = {
    name: '',
    vectorStoreRef: vectorDS.value[0]?.id ?? '',
    objectStoreRef: storageDS.value[0]?.id ?? '',
    embeddingModel: 'text-embedding-v4',
    embeddingDim: 1024,
  }
  showCreate.value = true
}

async function create() {
  if (!form.value.name.trim()) { ElMessage.warning('请填写名称'); return }
  if (!form.value.vectorStoreRef) { ElMessage.warning('请选择向量库（先建 dataservice vector 实例）'); return }
  if (!form.value.objectStoreRef) { ElMessage.warning('请选择对象存储（先建 dataservice storage 实例）'); return }
  submitting.value = true
  try {
    await fetchJSON('/api/knowledgebases', {
      method: 'POST',
      body: JSON.stringify(form.value),
    })
    ElMessage.success('知识库已创建')
    showCreate.value = false
    await load()
  } catch (e) {
    ElMessage.error('创建失败：' + (e as Error).message)
  } finally {
    submitting.value = false
  }
}

async function remove(kb: KnowledgeBase) {
  try {
    await ElMessageBox.confirm(`确认删除知识库「${kb.name}」？关联文档与向量将一并清除`, '删除确认', { type: 'warning' })
  } catch {
    return
  }
  try {
    await fetchJSON(`/api/knowledgebases/${kb.id}`, { method: 'DELETE' })
    ElMessage.success('已删除')
    await load()
  } catch (e) {
    ElMessage.error('删除失败：' + (e as Error).message)
  }
}

// 文档抽屉
const docDrawer = ref(false)
const currentKB = ref<KnowledgeBase | null>(null)
const docs = ref<Document[]>([])
let pollTimer: ReturnType<typeof setInterval> | null = null
let pollLoading = false // in-flight 防重入（慢请求不堆叠）
let pollErrShown = false // 连续失败只提示一次（防 3s 轮询错误刷屏），成功后复位

async function openDocs(kb: KnowledgeBase) {
  currentKB.value = kb
  docDrawer.value = true
  await loadDocs()
  startPoll()
}

async function loadDocs() {
  if (!currentKB.value || pollLoading) return
  pollLoading = true
  try {
    docs.value = await fetchJSON<Document[]>(`/api/knowledgebases/${currentKB.value.id}/documents`)
    pollErrShown = false
  } catch (e) {
    // 首次失败提示；轮询连续失败静默（防 3s 间隔错误 toast 刷屏），成功后复位
    if (!pollErrShown) {
      ElMessage.error('加载文档失败：' + (e as Error).message)
      pollErrShown = true
    }
  } finally {
    pollLoading = false
  }
}

function startPoll() {
  stopPoll()
  pollTimer = setInterval(async () => {
    // 全部终态（无 parsing）则停止轮询；页面不可见时跳过本次（后台 tab 不请求）
    if (document.hidden) return
    await loadDocs()
    if (!docs.value.some((d) => d.status === 'parsing')) stopPoll()
  }, 3000)
}
function stopPoll() {
  if (pollTimer) { clearInterval(pollTimer); pollTimer = null }
}

// 注意：onUpload 用 fetchAuth（非 fetchJSON）——multipart body 不能被 fetchAuth
// 强加 Content-Type:application/json（会破坏 boundary），故手动检查 resp.ok + 解 error。
async function onUpload(file: File) {
  if (!currentKB.value) return
  const fd = new FormData()
  fd.append('file', file)
  try {
    const resp = await fetchAuth(`/api/knowledgebases/${currentKB.value.id}/documents`, {
      method: 'POST',
      body: fd,
    })
    if (!resp.ok) {
      const j = await resp.json().catch(() => ({}))
      const msg = (j && typeof j === 'object' && 'error' in j ? j.error : null) || `HTTP ${resp.status}`
      // 503 = 集群未配 qdrant/minio 后端，给可操作提示（避免误以为上传成功）
      if (resp.status === 503) {
        ElMessage.error('知识库后端未就绪：' + msg + '（需管理员配置向量库/对象存储）')
      } else {
        ElMessage.error('上传失败：' + msg)
      }
      return
    }
    ElMessage.success('上传成功，正在解析+索引')
    await loadDocs()
    startPoll()
  } catch (e) {
    ElMessage.error('上传失败：' + (e as Error).message)
  }
}

async function removeDoc(doc: Document) {
  if (!currentKB.value) return
  try {
    await fetchJSON(`/api/knowledgebases/${currentKB.value.id}/documents/${doc.id}`, { method: 'DELETE' })
    await loadDocs()
  } catch (e) {
    ElMessage.error('删除文档失败：' + (e as Error).message)
  }
}

// 检索测试
const query = ref('')
const hits = ref<ChunkHit[]>([])
const searching = ref(false)
async function retrieve() {
  if (!currentKB.value || !query.value.trim()) return
  searching.value = true
  try {
    hits.value = await fetchJSON<ChunkHit[]>(`/api/knowledgebases/${currentKB.value.id}/retrieve`, {
      method: 'POST',
      body: JSON.stringify({ query: query.value }),
    })
  } catch (e) {
    ElMessage.error('检索失败：' + (e as Error).message)
    hits.value = []
  } finally {
    searching.value = false
  }
}

onMounted(() => { load(); loadDS() })
onUnmounted(stopPoll)
</script>

<template>
  <div class="page">
    <div class="header">
      <h2>知识库</h2>
      <el-button type="primary" @click="openCreate">新建知识库</el-button>
    </div>
    <el-table v-loading="loading" :data="kbs">
      <template #empty>
        <el-empty description="暂无知识库，上传文档构建 RAG 检索" :image-size="64">
          <el-button type="primary" @click="openCreate">新建知识库</el-button>
        </el-empty>
      </template>
      <el-table-column prop="name" label="名称" />
      <el-table-column label="被引用" min-width="140">
        <template #default="{ row }">
          <template v-if="usage[row.id]?.length">
            <el-tooltip :content="usage[row.id].join('、')">
              <el-tag size="small" type="primary">{{ usage[row.id].length }} 个 Agent</el-tag>
            </el-tooltip>
          </template>
          <span v-else style="color: var(--el-text-color-placeholder)">未引用</span>
        </template>
      </el-table-column>
      <el-table-column prop="embeddingModel" label="向量模型" width="180" />
      <el-table-column prop="embeddingDim" label="维度" width="90" />
      <el-table-column label="创建时间" width="180">
        <template #default="{ row }">{{ new Date(row.createdAt).toLocaleString() }}</template>
      </el-table-column>
      <el-table-column label="操作" width="220">
        <template #default="{ row }">
          <el-button size="small" @click="openDocs(row)">文档管理</el-button>
          <el-button size="small" type="danger" @click="remove(row)">删除</el-button>
        </template>
      </el-table-column>
    </el-table>

    <!-- 创建弹窗 -->
    <el-dialog v-model="showCreate" title="新建知识库" width="520px">
      <el-form label-width="110px">
        <el-form-item label="名称"><el-input v-model="form.name" placeholder="如 product-docs" /></el-form-item>
        <el-form-item label="向量库实例">
          <el-select v-model="form.vectorStoreRef" placeholder="选择 dataservice vector 实例">
            <el-option v-for="d in vectorDS" :key="d.id" :label="d.name" :value="d.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="对象存储实例">
          <el-select v-model="form.objectStoreRef" placeholder="选择 dataservice storage 实例">
            <el-option v-for="d in storageDS" :key="d.id" :label="d.name" :value="d.id" />
          </el-select>
        </el-form-item>
        <el-form-item label="向量模型"><el-input v-model="form.embeddingModel" /></el-form-item>
        <el-form-item label="维度"><el-input-number v-model="form.embeddingDim" :min="1" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showCreate = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="create">创建</el-button>
      </template>
    </el-dialog>

    <!-- 文档管理抽屉 -->
    <el-drawer v-model="docDrawer" :title="`文档管理 - ${currentKB?.name ?? ''}`" size="60%" @close="stopPoll">
      <el-upload
        :show-file-list="false"
        :before-upload="(f: File) => { onUpload(f); return false }"
        accept=".txt,.md,.html,.htm,text/plain,text/markdown,text/html"
      >
        <el-button type="primary">上传文档（txt/md/html）</el-button>
      </el-upload>
      <p style="color:var(--el-text-color-secondary);margin-top:8px;font-size:12px">
        文档解析+检索依赖平台向量库/对象存储后端（管理员配置）；上传后若长时间停留「解析中」或返回 503，请联系管理员。
      </p>
      <el-table :data="docs" style="margin-top:12px" empty-text="暂无文档">
        <el-table-column prop="name" label="文件名" />
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag :type="DOC_STATUS_TYPE[row.status]">{{ DOC_STATUS_LABEL[row.status] }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column prop="chunkCount" label="切片数" width="80" />
        <el-table-column label="说明" min-width="160">
          <template #default="{ row }">
            <span style="color:var(--el-text-color-secondary)">{{ row.message }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="80">
          <template #default="{ row }">
            <el-button size="small" type="danger" link @click="removeDoc(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <!-- 检索测试 -->
      <h4 style="margin-top:24px">检索测试</h4>
      <div style="display:flex;gap:8px">
        <el-input v-model="query" placeholder="输入查询文本" @keyup.enter="retrieve" />
        <el-button type="primary" :loading="searching" @click="retrieve">检索</el-button>
      </div>
      <div v-for="(h, i) in hits" :key="i" class="hit">
        <div class="hit-score">相似度 {{ h.score.toFixed(3) }} · 切片 {{ h.chunk.seq }}</div>
        <div class="hit-content">{{ h.chunk.content }}</div>
      </div>
    </el-drawer>
  </div>
</template>

<style scoped>
.page { padding: 16px 24px; }
.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 16px; }
.hit { margin-top: 12px; padding: 10px; background: var(--el-fill-color-light); border-radius: 4px; }
.hit-score { font-size: 12px; color: var(--el-text-color-secondary); margin-bottom: 4px; }
.hit-content { white-space: pre-wrap; font-size: 13px; line-height: 1.5; max-height: 160px; overflow: auto; }
</style>
