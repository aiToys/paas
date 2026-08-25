<script setup lang="ts">
// 内置 Gitea 仓库浏览器：文件树 + 最近提交（一站式--不跳转 Gitea Web UI）。
// 调 PaaS API（后端代理 Gitea），仅 source=internal 仓库可用。
import { ref, onMounted, computed } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'

const props = defineProps<{ appId: string; repo: { id: string; giteaRepo?: string; branch?: string } }>()
defineEmits<{ (e: 'close'): void }>()

interface TreeNode { path: string; type: string; mode?: string; size?: number }
interface TreeItem { label: string; path: string; type: string; isFile: boolean; children?: TreeItem[] }
interface Commit { sha: string; message: string; author: string; date: string }

const loading = ref(false)
const error = ref('')
const flatNodes = ref<TreeNode[]>([])
const commits = ref<Commit[]>([])
// 顶部 tab：文件树 / 最近提交（避免单列纵向布局把提交时间线推到视口外）
const activeTab = ref<'tree' | 'commits'>('tree')

// 从扁平 path 列表构建 el-tree 嵌套结构（目录在前，文件在后）
const treeData = computed<TreeItem[]>(() => {
  const root: TreeItem = { label: '', path: '', type: 'tree', isFile: false, children: [] }
  for (const n of flatNodes.value) {
    if (!n.path) continue
    const parts = n.path.split('/')
    let cur = root
    for (let i = 0; i < parts.length; i++) {
      const path = parts.slice(0, i + 1).join('/')
      let child = cur.children!.find((c) => c.label === parts[i])
      if (!child) {
        child = {
          label: parts[i],
          path,
          type: i === parts.length - 1 ? n.type : 'tree',
          isFile: i === parts.length - 1 ? n.type === 'blob' : false,
          children: i === parts.length - 1 && n.type === 'blob' ? undefined : [],
        }
        cur.children!.push(child)
      }
      cur = child
    }
  }
  // 排序：目录在前，文件在后，各自字母序
  const sortRec = (items: TreeItem[]) => {
    items.sort((a, b) => (a.isFile === b.isFile ? a.label.localeCompare(b.label) : a.isFile ? 1 : -1))
    items.forEach((it) => it.children && sortRec(it.children))
  }
  sortRec(root.children!)
  return root.children!
})

const hasFiles = computed(() => flatNodes.value.length > 0)

async function loadTree() {
  const resp = await fetchAuth(`/api/applications/${props.appId}/repositories/${props.repo.id}/tree`)
  if (resp.ok) {
    flatNodes.value = (await resp.json()).data ?? []
  } else {
    const err = await resp.json().catch(() => ({}))
    error.value = err.error || '加载文件树失败'
  }
}

async function loadCommits() {
  const resp = await fetchAuth(`/api/applications/${props.appId}/repositories/${props.repo.id}/commits?limit=20`)
  if (resp.ok) {
    commits.value = (await resp.json()).data ?? []
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    await Promise.all([loadTree(), loadCommits()])
  } finally {
    loading.value = false
  }
}

function fmtDate(s: string) {
  if (!s) return ''
  try {
    return new Date(s).toLocaleString()
  } catch {
    return s
  }
}

function shortSha(s: string) {
  return s ? s.slice(0, 8) : ''
}

// 文件内容查看：点击文件树叶子节点加载内容（避免「文件打不开」）。
const filePath = ref('')
const fileContent = ref('')
const loadingFile = ref(false)

async function onNodeClick(data: TreeItem) {
  if (!data.isFile) return
  loadingFile.value = true
  filePath.value = data.path
  fileContent.value = ''
  try {
    const resp = await fetchAuth(
      `/api/applications/${props.appId}/repositories/${props.repo.id}/file?path=${encodeURIComponent(data.path)}`,
    )
    if (resp.ok) {
      const d = await resp.json()
      fileContent.value = d.data?.content ?? ''
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '加载文件内容失败')
      filePath.value = ''
    }
  } finally {
    loadingFile.value = false
  }
}

function closeFile() {
  filePath.value = ''
  fileContent.value = ''
}

onMounted(load)
</script>

<template>
  <el-drawer :model-value="true" :title="`仓库浏览 · ${repo.giteaRepo ?? ''}`" size="55%" @close="$emit('close')">
    <div v-loading="loading" class="drawer-body">
      <el-alert v-if="error" :title="error" type="error" :closable="false" show-icon style="margin-bottom: 12px" />

      <el-tabs v-model="activeTab" class="browser-tabs">
        <el-tab-pane label="文件浏览" name="tree">
          <div class="split">
            <!-- 左：文件树（固定宽，内部滚动） -->
            <div class="tree-pane">
              <el-empty v-if="!hasFiles && !loading && !error" description="空仓库（push 代码后刷新）" :image-size="48" />
              <el-tree
                v-else
                :data="treeData"
                :props="{ label: 'label', children: 'children' }"
                node-key="path"
                @node-click="onNodeClick"
              >
                <template #default="{ data }">
                  <span class="tree-node" :class="{ clickable: data.isFile }">
                    <span class="tree-icon">{{ data.isFile ? '📄' : '📁' }}</span>
                    <span class="tree-label">{{ data.label }}</span>
                  </span>
                </template>
              </el-tree>
            </div>
            <!-- 右：文件内容（占满剩余宽，点左侧文件即在此展示） -->
            <div class="content-pane">
              <template v-if="filePath">
                <div v-loading="loadingFile" class="file-view">
                  <div class="file-header">
                    <span class="mono file-path">📄 {{ filePath }}</span>
                    <el-button link size="small" @click="closeFile">关闭</el-button>
                  </div>
                  <pre class="file-content">{{ fileContent }}</pre>
                </div>
              </template>
              <el-empty v-else description="点击左侧文件查看内容" :image-size="64" />
            </div>
          </div>
        </el-tab-pane>

        <el-tab-pane :label="`最近提交${commits.length ? `（${commits.length}）` : ''}`" name="commits">
          <el-timeline v-if="commits.length" class="commit-timeline">
            <el-timeline-item v-for="c in commits" :key="c.sha" :timestamp="fmtDate(c.date)" placement="top">
              <div class="commit-msg">{{ c.message }}</div>
              <div class="commit-meta">
                <span>{{ c.author }}</span>
                <span class="mono">{{ shortSha(c.sha) }}</span>
              </div>
            </el-timeline-item>
          </el-timeline>
          <el-empty v-else description="暂无提交" :image-size="48" />
        </el-tab-pane>
      </el-tabs>
    </div>
  </el-drawer>
</template>

<style scoped>
.drawer-body { display: flex; flex-direction: column; height: 100%; min-height: 0; }
.browser-tabs { flex: 1; min-height: 0; display: flex; flex-direction: column; }
.browser-tabs :deep(.el-tabs__content) { flex: 1; min-height: 0; overflow: auto; }
/* 左树右内容分栏：树固定宽滚动，内容占满剩余并撑满高 */
.split { display: flex; gap: 12px; height: calc(88vh - 180px); min-height: 320px; }
.tree-pane { width: 280px; flex-shrink: 0; overflow: auto; border-right: 1px solid var(--el-border-color-lighter); padding-right: 8px; }
.content-pane { flex: 1; min-width: 0; overflow: auto; }
.tree-node { display: flex; align-items: center; gap: 6px; font-size: 13px; }
.tree-node.clickable { cursor: pointer; }
.tree-node.clickable:hover { color: var(--el-color-primary); }
.tree-icon { font-size: 14px; }
.tree-label { font-family: var(--mono, ui-monospace, monospace); }
.file-view { display: flex; flex-direction: column; height: 100%; background: var(--el-fill-color-light); border-radius: 6px; padding: 12px; box-sizing: border-box; }
.file-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.file-path { font-size: 13px; font-weight: 600; word-break: break-all; }
.file-content {
  margin: 0; padding: 12px; background: var(--el-bg-color-page); border-radius: 4px;
  font-family: var(--mono, ui-monospace, monospace); font-size: 12px; line-height: 1.6;
  white-space: pre-wrap; word-break: break-all; flex: 1; min-height: 0; overflow: auto;
}
.commit-timeline { padding: 8px 4px 0 4px; }
.commit-msg { font-size: 13px; margin-bottom: 4px; }
.commit-meta { display: flex; gap: 12px; font-size: 12px; color: var(--text-dim); }
.mono { font-family: var(--mono, ui-monospace, monospace); }
</style>
