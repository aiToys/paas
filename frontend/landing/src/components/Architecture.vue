<script setup lang="ts">
// 架构哲学：三条不变量。把设计文档里的硬约束翻译成访客可读的产品语言。
const pillars = [
  {
    n: '01',
    title: 'Core 最小不可分',
    body: '只含所有子系统都依赖的元能力：租户、鉴权、资源纳管、编排、可观测、插件机制。业务领域逻辑绝不进 Core —— 判据："MaaS / 治理 / DevOps 都会用吗？"',
    foot: '元数据 PostgreSQL · 事件 NATS（Core 自带，避免元设施鸡生蛋）',
  },
  {
    n: '02',
    title: '子系统是插件，不是微服务',
    body: '以插件形式注册进 Core，共享鉴权 / 存储 / 事件总线。插件契约（pkg/plugin）对外可见，拓扑排序 + 环检测 + 依赖倒置注入。',
    foot: '本期范围：Core 底座 + MaaS；治理 / 中间件 / DevOps 后续按插件接入',
  },
  {
    n: '03',
    title: '控制面 / 数据面解耦',
    body: '控制面只下发期望状态（CRD），数据面负责实际运行。控制面挂了，已部署的模型 / 服务继续跑 —— 数据面有自愈能力。',
    foot: 'WorkloadReconciler / DataServiceReconciler watch CRD → CreateOrUpdate Deployment/StatefulSet',
  },
]
</script>

<template>
  <section id="architecture" class="section alt">
    <div class="container">
      <div class="section-head">
        <span class="eyebrow">架构</span>
        <h2 class="section-title">三条不变量，撑起整个平台</h2>
        <p class="section-sub">
          设计阶段定稿的核心约束，不轻易推翻。这是 paas 区别于"堆砌功能"型平台的地方。
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
