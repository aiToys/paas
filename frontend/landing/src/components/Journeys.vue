<script setup lang="ts">
const journeys = [
  { n: '01', title: '交付一次应用变更', description: '在产品内组织服务与代码，预演目标环境和产物，再跟进审批、验证与发布结果。', steps: ['代码与变更', '交付预演', '构建部署', '验证与发布'], href: '/docs/features/devops.html', link: '查看交付指南' },
  { n: '02', title: '定位服务运行问题', description: '从工作负载进入实例，关联容器日志、事件和调用链，再通过操作记录跟进重启或重建。', steps: ['运行状态', '实例与日志', '指标与链路', '运维操作'], href: '/docs/features/runtime.html', link: '查看运行指南' },
  { n: '03', title: '接入模型与智能体', description: '配置模型通道，用 Playground 或 API 调用；组合工具与知识库构建智能体，也可接入智能研发执行域。', steps: ['模型通道', 'Playground / API', '智能体与工作流', '执行记录'], href: '/docs/features/ai.html', link: '查看 AI 指南' },
]
</script>

<template>
  <section id="journeys" class="section journeys">
    <div class="container">
      <div class="section-head">
        <span class="eyebrow">使用路径</span>
        <h2 class="section-title">从你要完成的任务开始</h2>
        <p class="section-sub">交付、排障和 AI 开发，都能找到明确的操作入口。</p>
      </div>
      <article v-for="j in journeys" :key="j.n" class="journey">
        <div class="intro">
          <span class="number mono">{{ j.n }}</span>
          <h3>{{ j.title }}</h3>
          <p>{{ j.description }}</p>
          <a :href="j.href">{{ j.link }} →</a>
        </div>
        <ol :aria-label="j.title + '的操作步骤'">
          <li v-for="(step, index) in j.steps" :key="step"><span class="mono">{{ index + 1 }}</span>{{ step }}</li>
        </ol>
      </article>
    </div>
  </section>
</template>

<style scoped>
.journeys { border-top: 1px solid var(--border); }
.journey { display: grid; grid-template-columns: 1fr 1fr; gap: 64px; padding: 40px 0; border-top: 1px solid var(--border); align-items: center; }
.number { color: var(--accent); font-size: 13px; }
h3 { font-size: 25px; margin: 12px 0 16px; }
p { color: var(--text-dim); margin-bottom: 20px; max-width: 480px; }
a { color: var(--accent); font-weight: 600; }
ol { list-style: none; padding: 0; margin: 0; display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
li { display: flex; align-items: center; gap: 12px; padding: 20px 16px; background: var(--bg-elev); border: 1px solid var(--border); border-radius: var(--radius); font-size: 14px; }
li span { color: var(--accent); }
@media (max-width: 768px) { .journey { grid-template-columns: 1fr; gap: 24px; } }
@media (max-width: 380px) { ol { grid-template-columns: 1fr; } }
</style>
