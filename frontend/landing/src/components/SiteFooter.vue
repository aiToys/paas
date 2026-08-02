<script setup lang="ts">
import LogoMark from './LogoMark.vue'

// CTA 收口 + Footer：链接组（产品/资源/社区）+ 法律。
const cols = [
  {
    title: '产品',
    links: [
      { label: '能力', href: '#capabilities' },
      { label: '架构', href: '#architecture' },
      { label: '快速开始', href: '#quickstart' },
      { label: '用户控制台', href: '/console/' },
      { label: '管理后台', href: '/admin/' },
    ],
  },
  {
    title: '资源',
    links: [
      { label: 'GitHub 仓库', href: 'https://github.com/aitoys/paas', ext: true },
      { label: 'API 契约', href: '/openapi.json' },
      { label: '交互文档', href: '/docs' },
      { label: '设计规格', href: 'https://github.com/aitoys/paas/tree/main/docs/superpowers/specs', ext: true },
    ],
  },
  {
    title: '社区',
    links: [
      { label: '贡献指南', href: 'https://github.com/aitoys/paas/blob/main/CONTRIBUTING.md', ext: true },
      { label: '行为准则', href: 'https://github.com/aitoys/paas/blob/main/CODE_OF_CONDUCT.md', ext: true },
      { label: '安全披露', href: 'https://github.com/aitoys/paas/blob/main/SECURITY.md', ext: true },
      { label: '提 Issue', href: 'https://github.com/aitoys/paas/issues', ext: true },
    ],
  },
]
const year = 2026
</script>

<template>
  <section class="cta-band">
    <div class="container cta-inner">
      <div>
        <h2>把你的基础设施，收敛成一个控制面</h2>
        <p>Apache 2.0 开源 · 三预设 Key 开箱可试 · 双模交付</p>
      </div>
      <div class="cta-btns">
        <a class="btn primary" href="#quickstart">开始使用 →</a>
        <a class="btn ghost" href="https://github.com/aitoys/paas">GitHub</a>
      </div>
    </div>
  </section>

  <footer class="footer">
    <div class="container foot-grid">
      <div class="brand-col">
        <LogoMark :size="32" />
        <p class="desc">一站式 PaaS 平台 —— 服务治理、资源中心、MaaS、DevOps 的基础设施统一平台。</p>
        <p class="legal mono">Apache License 2.0 · © {{ year }} aitoys</p>
      </div>
      <div v-for="c in cols" :key="c.title" class="col">
        <div class="col-title">{{ c.title }}</div>
        <a v-for="l in c.links" :key="l.href" :href="l.href" class="col-link">
          {{ l.label }}<span v-if="l.ext" class="ext">↗</span>
        </a>
      </div>
    </div>
  </footer>
</template>

<style scoped>
.cta-band {
  padding: 72px 0;
  border-top: 1px solid var(--border);
}
.cta-inner {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 28px;
  flex-wrap: wrap;
  padding: 44px 36px;
  background: linear-gradient(135deg, var(--brand-soft), rgba(34, 211, 238, 0.06));
  border: 1px solid rgba(99, 102, 241, 0.25);
  border-radius: var(--radius-lg);
}
.cta-inner h2 {
  font-size: 28px;
  font-weight: 800;
}
.cta-inner p {
  margin-top: 8px;
  color: var(--text-dim);
  font-size: 14.5px;
}
.cta-btns {
  display: flex;
  gap: 10px;
  flex-shrink: 0;
}

.footer {
  padding: 64px 0 40px;
  border-top: 1px solid var(--border);
  background: var(--bg);
}
.foot-grid {
  display: grid;
  grid-template-columns: 1.6fr 1fr 1fr 1fr;
  gap: 40px;
}
.brand-col .desc {
  margin: 16px 0 14px;
  font-size: 13.5px;
  color: var(--text-dim);
  line-height: 1.6;
  max-width: 320px;
}
.legal {
  font-size: 12px;
  color: var(--text-faint);
}
.col-title {
  font-size: 12.5px;
  font-weight: 700;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text-faint);
  margin-bottom: 14px;
}
.col-link {
  display: block;
  font-size: 14px;
  color: var(--text-dim);
  padding: 5px 0;
}
.col-link:hover {
  color: var(--text);
}
.ext {
  font-size: 10px;
  margin-left: 4px;
  opacity: 0.7;
}

@media (max-width: 880px) {
  .foot-grid {
    grid-template-columns: 1fr 1fr;
    gap: 32px;
  }
  .brand-col {
    grid-column: 1 / -1;
  }
  .cta-inner {
    flex-direction: column;
    align-items: flex-start;
    text-align: left;
  }
}
@media (max-width: 480px) {
  .foot-grid {
    grid-template-columns: 1fr;
  }
}
</style>
