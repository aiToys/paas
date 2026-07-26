<script setup lang="ts">
// 官网展示页：Hero、能力矩阵、快速开始、Footer。
const capabilities = [
  { icon: '🧩', title: 'MaaS 推理平台', desc: 'vLLM 纳管、多模型路由、GPU 调度、OpenAI 兼容 API' },
  { icon: '🛡️', title: '服务治理', desc: '注册发现、配置中心、流量治理、链路追踪（规划中）' },
  { icon: '📦', title: '中间件管理', desc: 'MQ / 缓存 / DB 资源统一接入与生命周期（规划中）' },
  { icon: '🚀', title: 'DevOps', desc: 'CI/CD 流水线、发布编排、可观测性（规划中）' },
]

const quickstart = `# 编译并运行控制面
make build && ./bin/core

# 启动用户控制台
cd frontend && pnpm install && pnpm dev:user`
</script>

<template>
  <div>
    <!-- Hero -->
    <section class="hero">
      <div class="container">
        <h1>一站式 PaaS 平台</h1>
        <p class="lead">
          服务治理 · 中间件管理 · MaaS · DevOps —— 基础设施统一平台。
          云原生控制面（Go）+ 插件化子系统，SaaS 与私有化双模交付。
        </p>
        <div class="cta">
          <a class="btn primary" href="#quickstart">快速开始</a>
          <a class="btn" href="https://github.com/aitoys/paas">GitHub</a>
        </div>
      </div>
    </section>

    <!-- 能力矩阵 -->
    <section class="section">
      <div class="container">
        <h2 class="section-title">能力矩阵</h2>
        <div class="grid">
          <div v-for="c in capabilities" :key="c.title" class="card">
            <div class="icon">{{ c.icon }}</div>
            <h3>{{ c.title }}</h3>
            <p>{{ c.desc }}</p>
          </div>
        </div>
      </div>
    </section>

    <!-- 快速开始 -->
    <section id="quickstart" class="section alt">
      <div class="container">
        <h2 class="section-title">快速开始</h2>
        <pre class="code"><code>{{ quickstart }}</code></pre>
      </div>
    </section>

    <footer class="footer">
      <div class="container">
        Apache 2.0 · © 2026 aitoys ·
        <a href="https://github.com/aitoys/paas">GitHub</a>
      </div>
    </footer>
  </div>
</template>

<style scoped>
.container {
  max-width: 1080px;
  margin: 0 auto;
  padding: 0 24px;
}
.hero {
  background: linear-gradient(135deg, #0b1020 0%, #1a2b5c 100%);
  color: #fff;
  padding: 96px 0;
  text-align: center;
}
.hero h1 {
  font-size: 48px;
  margin: 0 0 16px;
}
.lead {
  font-size: 18px;
  opacity: 0.85;
  max-width: 720px;
  margin: 0 auto 32px;
}
.cta {
  display: flex;
  gap: 16px;
  justify-content: center;
}
.btn {
  display: inline-block;
  padding: 12px 28px;
  border-radius: 6px;
  border: 1px solid rgba(255, 255, 255, 0.4);
  color: #fff;
  font-weight: 600;
}
.btn.primary {
  background: var(--primary);
  border-color: var(--primary);
}
.section {
  padding: 80px 0;
}
.section.alt {
  background: #f7f8fa;
}
.section-title {
  text-align: center;
  font-size: 32px;
  margin: 0 0 48px;
}
.grid {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: 24px;
}
.card {
  background: var(--card);
  border: 1px solid #eee;
  border-radius: 12px;
  padding: 28px 24px;
  text-align: center;
}
.card .icon {
  font-size: 40px;
}
.card h3 {
  margin: 16px 0 8px;
}
.card p {
  color: var(--muted);
  font-size: 14px;
  line-height: 1.6;
}
.code {
  background: #0b1020;
  color: #e2e8f0;
  padding: 24px;
  border-radius: 8px;
  overflow-x: auto;
  font-size: 14px;
  text-align: left;
}
.footer {
  padding: 32px 0;
  text-align: center;
  color: var(--muted);
  border-top: 1px solid #eee;
}
@media (max-width: 768px) {
  .grid {
    grid-template-columns: repeat(2, 1fr);
  }
  .hero h1 {
    font-size: 32px;
  }
}
</style>
