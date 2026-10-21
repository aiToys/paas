<script setup lang="ts">
import { computed } from 'vue'
import { platform, platformError, consoleLink } from '../platform'

const journeys = computed(() => [
  { title: '泳道对比', action: '选择泳道，再搜索或下单；对比选路结果与 Trace。', path: platform.value?.envId ? `environments/${encodeURIComponent(platform.value.envId)}` : 'environments', cta: '管理当前环境泳道' },
  { title: '配置热更新', action: '在配置中心发布分页或推荐参数，再刷新业务请求确认变化。', path: 'config-center', cta: '打开配置中心' },
  { title: 'AI 客服与审批', action: '向客服查询商品；运营视图发起测试订单退款，再到工作流审批。', path: 'ai/workflows', cta: '查看工作流' },
  { title: '事件与定时任务', action: '下单后查看通知；等待超时关单，再核对任务日志。消息和经营统计为环境汇总。', path: 'workloads', cta: '查看任务与日志' },
  { title: '发布与回滚', action: '在交付中心运行流水线，对比测试与生产入口的服务版本。', path: 'delivery', cta: '打开交付中心' },
  { title: '数据服务与恢复', action: '查看数据库、缓存、消息等绑定资源；备份恢复需在平台执行并核验数据。', path: 'dataservices', cta: '查看数据服务' },
])
</script>

<template>
  <section class="experience" aria-labelledby="experience-title">
    <div class="experience-heading">
      <div><h2 id="experience-title">从一次业务操作，看到平台如何工作</h2><p>每条路径都需要真实操作和平台证据；入口可用不等于能力已验收。</p></div>
      <a v-if="consoleLink(platform?.appId ? `products/${encodeURIComponent(platform.appId)}` : 'products')" :href="consoleLink(platform?.appId ? `products/${encodeURIComponent(platform.appId)}` : 'products')" target="_blank" rel="noopener noreferrer">打开平台控制台 ↗</a>
    </div>
    <p class="connection-status" role="status">{{ platformError || (platform ? `平台目录已读取 · ${platform.discovery ? '数据面发现已配置，实际选路以请求结果为准' : '数据面发现未配置'}` : '平台目录尚未读取') }}</p>
    <div class="journey-grid">
      <article v-for="(journey, index) in journeys" :key="journey.title">
        <h3><span>{{ String(index + 1).padStart(2, '0') }}</span> {{ journey.title }}</h3>
        <p>{{ journey.action }}</p>
        <a v-if="consoleLink(journey.path)" :href="consoleLink(journey.path)" target="_blank" rel="noopener noreferrer">{{ journey.cta }} ↗</a>
        <span v-else class="unconfigured">部署时配置 PAAS_CONSOLE_URL 后可直达</span>
      </article>
    </div>
    <p class="coverage-note">PaasShop 聚焦应用交付、数据服务、治理、可观测和 AI 的业务体验。多租户权限、配额计费、离线交付等平台管理场景仍需单独验证；HTTP 泳道验证也不能代表 MQ 与定时任务已隔离。</p>
  </section>
</template>

<style scoped>
.experience { margin: 20px 32px; padding: 24px; background: #fff; border: 1px solid #dce1ea; border-radius: 12px; }
.experience-heading { display: flex; flex-wrap: wrap; align-items: start; justify-content: space-between; gap: 16px; }
h2 { font-size: 19px; color: #24304b; margin-bottom: 8px; }
p { font-size: 13px; color: #526174; line-height: 1.7; }
.connection-status { margin: 20px 0; padding: 10px 14px; background: #f1f4f9; border-radius: 6px; }
.journey-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 24px; }
article { display: flex; flex-direction: column; gap: 10px; }
article h3 { font-size: 15px; color: #24304b; }
article h3 span { color: #526174; font-size: 12px; margin-right: 6px; }
article p { flex: 1; }
a { font-size: 13px; }
.unconfigured { font-size: 12px; color: #526174; }
.coverage-note { margin-top: 24px; padding-top: 16px; border-top: 1px solid #dce1ea; }
@media (max-width: 900px) { .journey-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
@media (max-width: 600px) { .experience { margin: 16px; padding: 20px; } .journey-grid { grid-template-columns: 1fr; } }
</style>
