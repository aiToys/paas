<template>
  <PageContainer title="智能诊断（只读根因分析）">
    <template #header>
      <span class="hint">LLM 工具循环跨全命名空间采集证据 · 同步返回约 1-3 分钟</span>
    </template>

    <el-alert
      type="info"
      :closable="false"
      show-icon
      title="全部操作只读：仅采集 K8s / Prometheus / Loki / Jaeger 证据，不写集群、不改数据。"
      description="报告是根因假设与证据链（每条假设至少 1 条证据），最终裁决由人执行；missing = 数据源不覆盖、empty = 查询成功无数据，二者均非「无异常」。"
    />

    <!-- 触发表单 -->
    <el-card shadow="never" class="gap" header="诊断目标">
      <el-form :model="form" label-width="90px" inline>
        <el-form-item label="命名空间" required>
          <el-input v-model="form.namespace" placeholder="如 paas / paas-t-acme" style="width: 180px" />
        </el-form-item>
        <el-form-item label="工作负载" required>
          <el-input v-model="form.workload" placeholder="如 paas-core / wl-xxx" style="width: 220px" />
        </el-form-item>
        <el-form-item label="Pod">
          <el-input v-model="form.pod" placeholder="选填，聚焦单 Pod" style="width: 240px" />
        </el-form-item>
        <el-form-item label="症状">
          <el-select v-model="form.symptom" style="width: 120px">
            <el-option v-for="s in symptoms" :key="s" :label="s" :value="s" />
          </el-select>
        </el-form-item>
        <el-form-item label="回看窗口">
          <el-input-number v-model="form.lookbackMinutes" :min="10" :max="720" :step="30" style="width: 120px" />
          <span class="unit">分钟</span>
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="loading" :disabled="!form.namespace || !form.workload" @click="run">
            {{ loading ? '诊断中（可耐心等待）…' : '开始诊断' }}
          </el-button>
        </el-form-item>
      </el-form>
    </el-card>

    <el-alert v-if="error" class="gap" type="error" :closable="false" show-icon :title="error" />

    <template v-if="report">
      <!-- 结论卡 -->
      <el-card shadow="never" class="gap">
        <template #header>
          <div class="verdict-head">
            <el-tag :type="severityType(report.verdict.severity)" size="large" effect="dark">
              {{ report.verdict.severity }}
            </el-tag>
            <el-tag v-if="report.verdict.category !== 'unknown'" type="info" effect="plain">
              {{ categoryLabel(report.verdict.category) }}
            </el-tag>
            <span v-if="degraded" class="degraded">输出校验未通过，以下为降级摘要（原文见页尾）</span>
            <span class="meta">{{ report.durationSeconds.toFixed(1) }}s · 约 {{ report.tokensUsed }} 字符</span>
          </div>
        </template>
        <div class="summary">{{ report.verdict.summary || '（无摘要）' }}</div>
        <div v-if="coverageKeys.length" class="coverage">
          <span class="coverage-label">数据源覆盖：</span>
          <el-tag
            v-for="k in coverageKeys"
            :key="k"
            size="small"
            :type="coverageType(report.dataSourceCoverage[k])"
            effect="plain"
          >
            {{ k }}:{{ report.dataSourceCoverage[k] }}
          </el-tag>
        </div>
      </el-card>

      <!-- 根因假设 -->
      <el-card v-if="report.rootCauseHypotheses.length" shadow="never" class="gap" header="根因假设（按置信度排序）">
        <div v-for="(h, i) in report.rootCauseHypotheses" :key="i" class="hyp">
          <div class="hyp-head">
            <span class="hyp-idx">H{{ i + 1 }}</span>
            <el-progress
              :percentage="Math.round(h.confidence * 100)"
              :stroke-width="14"
              :color="confColor(h.confidence)"
              class="hyp-conf"
            />
          </div>
          <div class="hyp-text">{{ h.hypothesis }}</div>
          <el-collapse v-if="h.evidence.length" class="hyp-evidence">
            <el-collapse-item :title="`证据 ${h.evidence.length} 条`">
              <div v-for="(e, j) in h.evidence" :key="j" class="evi">
                <el-tag size="small" :type="weightType(e.weight)" effect="plain">{{ e.source }}/{{ e.weight }}</el-tag>
                <span class="evi-detail">{{ e.detail }}</span>
              </div>
            </el-collapse-item>
          </el-collapse>
        </div>
      </el-card>

      <!-- 建议动作 -->
      <el-card v-if="report.recommendedActions.length" shadow="never" class="gap" header="建议动作（由人执行，平台不代操作）">
        <div v-for="(a, i) in report.recommendedActions" :key="i" class="action">
          <el-tag size="small" :type="a.type === 'code' ? 'danger' : 'primary'" effect="plain">{{ a.type }}</el-tag>
          <el-tag size="small" :type="riskType(a.risk)" effect="plain">风险 {{ a.risk }}</el-tag>
          <el-tag v-if="a.requiresApproval" size="small" type="warning" effect="plain">需审批</el-tag>
          <div class="action-text">{{ a.action }}</div>
        </div>
      </el-card>

      <!-- 置信说明 / 降级原文 -->
      <el-card v-if="report.confidenceNote" shadow="never" class="gap" header="置信度说明">
        <div class="note">{{ report.confidenceNote }}</div>
      </el-card>
      <el-card v-if="degraded && report.rawOutput" shadow="never" class="gap" header="LLM 原始输出（降级证据）">
        <pre class="raw">{{ report.rawOutput }}</pre>
      </el-card>
    </template>
  </PageContainer>
</template>

<script lang="ts" setup>
// 智能诊断（2026-10-27）：POST /api/admin/diagnose/run 的管理端入口。
// 与「平台诊断」页（/operations/diagnostics，internal/diagnostics 静态自检）完全不同：
// 本页是 LLM Agent 动态根因分析（internal/diagnose），跨全 ns 只读采集证据。
// 同步接口耗时 1-3 分钟，loading 态明示等待；报告渲染严格按后端四态语义
// （missing/empty 不渲染成「无异常」，coverage chips 原样透出）。
import { computed, reactive, ref } from 'vue'
import { PageContainer } from '@/app/components'
import { runDiagnose, type DiagnoseReport } from '@/modules/operations/api'

const symptoms = ['未知', 'CrashLoop', '延迟', '错误率']

const form = reactive({
  namespace: 'paas',
  workload: 'paas-core',
  pod: '',
  symptom: '未知',
  lookbackMinutes: 60,
})

const loading = ref(false)
const error = ref('')
const report = ref<DiagnoseReport | null>(null)

const degraded = computed(() => report.value?.verdict.category === 'unknown')

const coverageKeys = computed(() => Object.keys(report.value?.dataSourceCoverage ?? {}))

async function run() {
  loading.value = true
  error.value = ''
  report.value = null
  try {
    report.value = await runDiagnose({
      target: {
        namespace: form.namespace.trim(),
        workload: form.workload.trim(),
        ...(form.pod.trim() ? { pod: form.pod.trim() } : {}),
        symptom: form.symptom,
      },
      timeWindow: { lookbackMinutes: form.lookbackMinutes },
    })
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

const categoryLabels: Record<string, string> = {
  code_bug: '代码缺陷',
  config_error: '配置错误',
  resource_exhaustion: '资源耗尽',
  dependency_failure: '依赖故障',
  infra: '基础设施',
}
const categoryLabel = (c: string) => categoryLabels[c] ?? c

const severityType = (s: string) => (s === 'critical' ? 'danger' : s === 'warning' ? 'warning' : 'info')
const weightType = (w: string) => (w === 'high' ? 'danger' : w === 'medium' ? 'warning' : 'info')
const riskType = (r: string) => (r === 'high' ? 'danger' : r === 'medium' ? 'warning' : 'success')
const coverageType = (s: string) => (s === 'ok' ? 'success' : s === 'empty' ? 'info' : s === 'missing' ? 'warning' : 'danger')
const confColor = (c: number) => (c >= 0.8 ? '#67c23a' : c >= 0.6 ? '#e6a23c' : '#909399')
</script>

<style scoped>
.hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.gap {
  margin-top: 12px;
}
.verdict-head {
  display: flex;
  align-items: center;
  gap: 8px;
}
.degraded {
  color: var(--el-color-warning);
  font-size: 12px;
}
.meta {
  margin-left: auto;
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.summary {
  line-height: 1.7;
}
.coverage {
  margin-top: 10px;
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
}
.coverage-label {
  color: var(--el-text-color-secondary);
  font-size: 12px;
}
.hyp {
  padding: 10px 0;
  border-bottom: 1px solid var(--el-border-color-lighter);
}
.hyp:last-child {
  border-bottom: none;
}
.hyp-head {
  display: flex;
  align-items: center;
  gap: 10px;
}
.hyp-idx {
  font-weight: 600;
  color: var(--el-color-primary);
}
.hyp-conf {
  width: 180px;
}
.hyp-text {
  margin: 8px 0;
  line-height: 1.7;
}
.hyp-evidence :deep(.el-collapse-item__header) {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.evi {
  display: flex;
  gap: 8px;
  padding: 4px 0;
  align-items: flex-start;
}
.evi-detail {
  font-size: 13px;
  line-height: 1.6;
  color: var(--el-text-color-regular);
}
.action {
  padding: 8px 0;
  border-bottom: 1px solid var(--el-border-color-lighter);
  display: flex;
  gap: 6px;
  align-items: flex-start;
  flex-wrap: wrap;
}
.action:last-child {
  border-bottom: none;
}
.action-text {
  width: 100%;
  line-height: 1.7;
}
.note {
  line-height: 1.7;
  color: var(--el-text-color-regular);
  font-size: 13px;
}
.raw {
  white-space: pre-wrap;
  word-break: break-all;
  font-size: 12px;
  max-height: 400px;
  overflow: auto;
  background: var(--el-fill-color-light);
  padding: 10px;
  border-radius: 4px;
}
.unit {
  margin-left: 6px;
  color: var(--el-text-color-secondary);
  font-size: 13px;
}
</style>
