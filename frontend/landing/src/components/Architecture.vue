<script setup lang="ts">
// 架构哲学：三条不变量。把设计文档里的硬约束翻译成访客可读的产品语言。
const pillars = [
  {
    n: '01',
    title: 'Core 最小不可分',
    body: '只含所有子系统都依赖的元能力：租户、鉴权、资源纳管、编排、可观测、插件机制。业务领域逻辑绝不进 Core —— 判据："MaaS / 治理 / DevOps 都会用吗？"',
    foot: '元数据 PostgreSQL · 多租户身份与权限 · OpenAPI 契约',
  },
  {
    n: '02',
    title: '模块边界与插件契约',
    body: '插件契约提供拓扑排序、依赖检查与依赖注入。当前 MaaS 通过插件注册，其余领域在控制面内按模块装配，逐步对齐插件边界。',
    foot: '插件化是演进方向，模块能力以实际配置与接口为准',
  },
  {
    n: '03',
    title: '控制面 / 数据面解耦',
    body: '工作负载与数据服务由 CRD 驱动 Kubernetes 运行。控制面故障时，已落地负载继续由集群管理；平台 API、模型推理网关及实时发现仍依赖控制面。',
    foot: 'WorkloadReconciler / DataServiceReconciler watch CRD → CreateOrUpdate Deployment/StatefulSet',
  },
]
</script>

<template>
  <section id="architecture" class="section alt">
    <div class="container">
      <div class="section-head">
        <span class="eyebrow">架构</span>
        <h2 class="section-title">统一控制面，清晰的运行边界</h2>
        <p class="section-sub">
          Go 控制面组织元数据与期望状态，Kubernetes 承载工作负载，第三方通道提供模型推理。
        </p>
      </div>

      <div class="pillars">
        <article v-for="p in pillars" :key="p.n" class="pillar">
          <div class="num mono">{{ p.n }}</div>
          <h3>{{ p.title }}</h3>
          <p class="body">{{ p.body }}</p>
          <div class="foot mono">{{ p.foot }}</div>
        </article>
      </div>
    </div>
  </section>
</template>

<style scoped>
.alt {
  background: linear-gradient(180deg, transparent, rgba(99, 102, 241, 0.04), transparent);
  border-top: 1px solid var(--border);
  border-bottom: 1px solid var(--border);
}
.pillars {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 22px;
}
.pillar {
  padding: 30px 26px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  position: relative;
  overflow: hidden;
}
.pillar::before {
  content: '';
  position: absolute;
  top: 0;
  left: 0;
  right: 0;
  height: 3px;
  background: var(--grad-brand);
  opacity: 0.7;
}
.num {
  font-size: 13px;
  font-weight: 700;
  color: var(--accent);
  margin-bottom: 14px;
}
.pillar h3 {
  font-size: 19px;
  font-weight: 700;
  margin-bottom: 12px;
}
.body {
  font-size: 14.5px;
  color: var(--text-dim);
  line-height: 1.65;
  margin-bottom: 18px;
}
.foot {
  font-size: 12px;
  color: var(--text-faint);
  padding-top: 14px;
  border-top: 1px solid var(--border);
  line-height: 1.5;
}

@media (max-width: 880px) {
  .pillars {
    grid-template-columns: 1fr;
  }
}
</style>
