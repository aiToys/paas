<script setup lang="ts">
import { formatDateTime } from '@/utils/format'
// 应用详情 - 配置 tab：工作负载级 env/Secret 键值（静态，改了重启注入）。
// 与服务治理的「配置中心」（运行时动态、跨实例、版本灰度）严格区分。
// 依赖顶栏 scope（当前环境）：列表按当前环境过滤，增删改作用于该环境。
// Secret 后端明文存储，API 返回固定掩码 ••••••（不泄漏长度/内容）；
// 编辑 secret 不回填值，需重新输入。生产删除走 confirmDangerous（输入名称确认）。
import { computed, onMounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'
import { fetchAuth } from '@/api'
import { useEnvStore } from '@/stores/env'
import { confirmDangerous } from '@/composables/useDangerConfirm'

const props = defineProps<{ appId: string }>()
const envStore = useEnvStore()

interface ConfigItem {
  id: string
  envId: string
  key: string
  value: string
  type: 'env' | 'secret'
  updatedAt: string
}

const TYPE_ENV = 'env'
const TYPE_SECRET = 'secret'

const items = ref<ConfigItem[]>([])
const loading = ref(false)
const showEdit = ref(false)
const submitting = ref(false)

// 按 type 分两组：环境变量（明文）+ 凭证/密钥（掩码）。
// 凭证组即「应用引用的密钥」——appconfig(type=secret) 是工作负载启动注入的真实敏感凭证。
const envItems = computed(() => items.value.filter((i) => i.type === TYPE_ENV))
const secretItems = computed(() => items.value.filter((i) => i.type === TYPE_SECRET))
// 编辑表单：editingId 非空 = 编辑（同 key upsert）；空 = 新增
const form = ref<{ id: string; key: string; value: string; type: string }>({ id: '', key: '', value: '', type: TYPE_ENV })

const envId = computed(() => envStore.currentEnvId)
const envName = computed(() => envStore.currentEnv?.name ?? '')
const hasEnv = computed(() => !!envId.value)

// secret 编辑时：值框是否禁用回填。编辑 secret 不回填（掩码非真值）。
const isEditSecret = computed(() => !!form.value.id && form.value.type === TYPE_SECRET)

// 自增请求 ID：快速切环境时，旧请求返回若非最新则丢弃，防数据错乱（A 慢于 B 覆盖 B 结果）。
let loadReqId = 0

async function load() {
  const reqId = ++loadReqId
  if (!hasEnv.value) {
    if (reqId === loadReqId) items.value = []
    return
  }
  loading.value = true
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/configs?envId=${envId.value}`)
    if (reqId !== loadReqId) return // 已有更新请求，丢弃本次结果
    if (resp.ok) items.value = (await resp.json()).data ?? []
  } finally {
    if (reqId === loadReqId) loading.value = false
  }
}

function openAdd() {
  form.value = { id: '', key: '', value: '', type: TYPE_ENV }
  showEdit.value = true
}

function openEdit(row: ConfigItem) {
  // secret 不回填值（掩码非真值），编辑需重新输入
  form.value = {
    id: row.id,
    key: row.key,
    value: row.type === TYPE_SECRET ? '' : row.value,
    type: row.type,
  }
  showEdit.value = true
}

async function submit() {
  if (!hasEnv.value) return
  if (!form.value.key.trim()) {
    ElMessage.warning('请填写 Key')
    return
  }
  if (!form.value.value) {
    ElMessage.warning(isEditSecret.value ? '编辑 Secret 需重新输入值' : '请填写 Value')
    return
  }
  submitting.value = true
  try {
    const resp = await fetchAuth(`/api/applications/${props.appId}/configs`, {
      method: 'POST',
      body: JSON.stringify({
        envId: envId.value,
        key: form.value.key.trim(),
        value: form.value.value,
        type: form.value.type,
      }),
    })
    if (resp.ok) {
      ElMessage.success(form.value.id ? '已更新' : '已新增')
      showEdit.value = false
      load()
    } else {
      const err = await resp.json().catch(() => ({}))
      ElMessage.error(err.error || '保存失败')
    }
  } finally {
    submitting.value = false
  }
}

async function remove(row: ConfigItem) {
  // 生产删除高危：要求输入 Key 确认；测试普通确认
  const ok = await confirmDangerous({
    action: '删除',
    target: row.key,
    requireNameConfirm: envStore.isProd,
  })
  if (!ok) return
  const resp = await fetchAuth(`/api/applications/${props.appId}/configs/${row.id}`, { method: 'DELETE' })
  if (resp.ok) {
    ElMessage.success('已删除')
    load()
  } else {
    const err = await resp.json().catch(() => ({}))
    ElMessage.error(err.error || '删除失败')
  }
}

onMounted(load)
watch(() => envStore.currentEnvId, load)
watch(() => props.appId, load)
</script>

<template>
  <div class="devops-tab">
    <div class="tab-head">
      <span class="tab-title">应用配置</span>
      <span class="tab-hint">工作负载级 env/Secret，改后重启注入 · 当前环境：{{ envName || '未选择' }}</span>
      <el-button v-if="hasEnv" type="primary" size="small" style="margin-left: auto" @click="openAdd">+ 新增配置</el-button>
    </div>

    <div v-if="!hasEnv" class="cfg-empty">
      <p>请在顶栏选择一个环境，以管理该环境的应用配置。</p>
    </div>

    <!-- 环境变量 -->
    <section v-if="hasEnv" class="cfg-group">
      <div class="group-title">环境变量（明文）<span class="group-cnt mono">{{ envItems.length }}</span></div>
      <el-table :data="envItems" v-loading="loading" size="small" empty-text="尚无环境变量">
        <el-table-column prop="key" label="Key" min-width="180">
          <template #default="{ row }"><span class="mono">{{ row.key }}</span></template>
        </el-table-column>
        <el-table-column label="值" min-width="220">
          <template #default="{ row }"><span class="mono">{{ row.value }}</span></template>
        </el-table-column>
        <el-table-column label="更新时间" width="160">
          <template #default="{ row }">{{ formatDateTime(row.updatedAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="120">
          <template #default="{ row }">
            <el-button text type="primary" size="small" @click="openEdit(row)">编辑</el-button>
            <el-button text type="danger" size="small" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 凭证 / 密钥 -->
    <section v-if="hasEnv" class="cfg-group">
      <div class="group-title">
        凭证 / 密钥<span class="group-cnt mono">{{ secretItems.length }}</span>
      </div>
      <div class="secret-tip">应用工作负载启动时注入的敏感凭证。解绑数据服务会同步清除注入的连接凭证。</div>
      <el-table :data="secretItems" size="small" empty-text="尚无凭证">
        <el-table-column prop="key" label="Key" min-width="200">
          <template #default="{ row }"><span class="mono">{{ row.key }}</span></template>
        </el-table-column>
        <el-table-column label="值（掩码）" min-width="200">
          <template #default="{ row }"><span class="mono masked">{{ row.value }}</span></template>
        </el-table-column>
        <el-table-column label="更新时间" width="160">
          <template #default="{ row }">{{ formatDateTime(row.updatedAt) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="120">
          <template #default="{ row }">
            <el-button text type="primary" size="small" @click="openEdit(row)">编辑</el-button>
            <el-button text type="danger" size="small" @click="remove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </section>

    <!-- 空态（两组皆空） -->
    <div v-if="hasEnv && !items.length && !loading" class="cfg-empty">
      <p>该环境尚无配置项，点右上角「+ 新增配置」添加。</p>
    </div>

    <el-dialog v-model="showEdit" :title="form.id ? '编辑配置' : '新增配置'" width="460px">
      <el-form label-width="70px">
        <el-form-item label="Key">
          <el-input v-model="form.key" :disabled="!!form.id" placeholder="如 LOG_LEVEL" />
        </el-form-item>
        <el-form-item label="类型">
          <el-radio-group v-model="form.type">
            <el-radio :value="TYPE_ENV">Env（明文）</el-radio>
            <el-radio :value="TYPE_SECRET">Secret（掩码）</el-radio>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="Value">
          <el-input
            v-if="form.type === TYPE_SECRET"
            v-model="form.value"
            type="password"
            show-password
            :placeholder="isEditSecret ? '编辑 Secret 需重新输入完整值' : '输入敏感值，保存后以掩码展示'"
          />
          <el-input v-else v-model="form.value" placeholder="如 info" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="showEdit = false">取消</el-button>
        <el-button type="primary" :disabled="submitting" @click="submit">
          {{ submitting ? '保存中…' : '保存' }}
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.cfg-empty {
  padding: 48px 0;
  text-align: center;
  color: var(--text-faint);
  font-size: 13px;
}
.masked {
  color: var(--text-faint);
  letter-spacing: 2px;
}
.cfg-group { margin-bottom: 22px; }
.group-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13px;
  font-weight: 600;
  color: var(--text-dim);
  margin-bottom: 8px;
}
.group-cnt {
  font-size: 11px;
  color: var(--text-faint);
  padding: 1px 7px;
  background: var(--surface-2, transparent);
  border-radius: 8px;
}
.secret-tip {
  margin-bottom: 8px;
  padding: 8px 10px;
  font-size: 12px;
  color: var(--text-dim);
  background: var(--surface-2);
  border: 1px solid var(--border);
  border-radius: var(--radius);
}
</style>
