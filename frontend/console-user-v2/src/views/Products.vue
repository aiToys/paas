<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { fetchJSON, fetchList, apiError } from '@/api'
import { useUrlState } from '@/composables/useUrlState'
import PageHeader from '@/components/PageHeader.vue'
import StatePanel from '@/components/StatePanel.vue'
import TablePager from '@/components/TablePager.vue'
import Icon from '@/components/Icon.vue'
interface Product { id: string; name: string; desc?: string; bindings?: { type: string; name: string }[] }
const route = useRoute()
const router = useRouter()
const items = ref<Product[]>([])
const loading = ref(true)
const error = ref('')
const { value: q } = useUrlState<string>('q', '')
const page = ref(1)
const showCreate = ref(route.query.new === '1')
const creating = ref(false)
const form = ref({ name: '', desc: '' })
const filtered = computed(() => items.value.filter((p) => (p.name + p.id + (p.desc ?? '')).toLowerCase().includes(q.value.trim().toLowerCase())))
const rows = computed(() => filtered.value.slice((page.value - 1) * 20, page.value * 20))
watch(q, () => { page.value = 1 })
watch(() => route.query.new, (v) => { if (v === '1') showCreate.value = true })
async function load() {
  loading.value = true
  error.value = ''
  try { items.value = await fetchList<Product>('/api/applications') }
  catch (e) { error.value = apiError(e, '无法加载产品列表') }
  finally { loading.value = false }
}
onMounted(load)
async function create() {
  if (!form.value.name.trim()) { ElMessage.warning('请输入产品名称'); return }
  creating.value = true
  try {
    const product = await fetchJSON<Product>('/api/applications', { method: 'POST', body: JSON.stringify({ name: form.value.name.trim(), desc: form.value.desc.trim() }) })
    ElMessage.success('产品已创建，现在可以添加服务')
    showCreate.value = false
    await router.push('/products/' + product.id)
  } catch (e) { ElMessage.error(apiError(e, '创建失败')) }
  finally { creating.value = false }
}
</script>
<template>
  <div class="page">
    <PageHeader title="产品" description="按业务组织服务、代码与共享资源。产品不绑定环境，服务可以部署到多个环境。" eyebrow="应用交付">
      <button class="act primary" @click="showCreate = true"><span aria-hidden="true">＋</span> 新建产品</button>
    </PageHeader>
    <div class="filter-bar">
      <label class="search-field"><Icon name="search" :size="17" /><input v-model="q" aria-label="搜索产品" placeholder="搜索产品名称、描述或 ID" /></label>
      <button class="act" :disabled="loading" @click="load">刷新</button>
      <span class="result-count">{{ items.length }} 个产品</span>
    </div>
    <div v-if="loading" v-loading="true" class="loading-block" aria-label="正在加载产品"></div>
    <StatePanel v-else-if="error" title="产品加载失败" :description="error" error><button class="act" @click="load">重新加载</button></StatePanel>
    <StatePanel v-else-if="!items.length" title="从第一个产品开始" description="将一个业务的服务、代码仓库和数据资源放在一起，再部署到所需环境。"><button class="act primary" @click="showCreate = true">新建产品</button></StatePanel>
    <StatePanel v-else-if="!filtered.length" title="没有匹配的产品" description="尝试其他名称，或清除搜索条件。"><button class="act" @click="q = ''">清除搜索</button></StatePanel>
    <template v-else>
      <div class="table-scroll" data-table-scroll tabindex="0">
<table class="list-table product-table">
<thead><tr><th scope="col">产品</th><th scope="col">共享资源</th><th scope="col">快捷操作</th></tr></thead>
        <tbody>
<tr v-for="p in rows" :key="p.id">
          <td><RouterLink :to="'/products/' + p.id" class="product-name">{{ p.name }}</RouterLink><p class="product-desc">{{ p.desc || '暂无描述' }}</p></td>
          <td><span class="dim">{{ p.bindings?.length ?? 0 }} 项资源绑定</span></td>
          <td><div class="row-actions"><RouterLink :to="'/products/' + p.id" class="text-button">服务运行</RouterLink><RouterLink :to="'/products/' + p.id + '?tab=delivery'" class="text-button">交付记录</RouterLink></div></td>
        </tr>
</tbody>
      </table>
</div>
      <TablePager v-model="page" :total="filtered.length" />
    </template>
    <el-dialog v-model="showCreate" title="新建产品" width="480px">
      <el-form label-position="top" @submit.prevent="create">
        <el-form-item label="产品名称" required><el-input v-model="form.name" placeholder="例如：在线商城" maxlength="100" autofocus @keyup.enter="create" /></el-form-item>
        <el-form-item label="描述（可选）"><el-input v-model="form.desc" type="textarea" :rows="3" placeholder="说明这个产品的用途" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="showCreate = false">取消</el-button><el-button type="primary" :loading="creating" @click="create">创建产品</el-button></template>
    </el-dialog>
  </div>
</template>
<style scoped>
@import './list-page.css';
.product-name { color: var(--brand); font-weight: 600; font-size: 14px; text-decoration: none; }
.product-name:hover { text-decoration: underline; }
.product-desc { color: var(--text-faint); font-size: 12px; margin: 5px 0 0; max-width: 600px; }
.product-table td:first-child { width: 58%; }
.row-actions { display: flex; gap: 18px; white-space: nowrap; }
</style>
