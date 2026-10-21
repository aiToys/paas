<script setup lang="ts">
// 首屏用任务旅程说明平台价值；架构和启用条件在下方展开。
const stats = [
  { v: '交付', k: '变更 · 构建 · 发布' },
  { v: '运行', k: '实例 · 日志 · 链路' },
  { v: 'AI', k: '模型 · 智能体 · 工作流' },
  { v: '双模', k: 'SaaS + 私有化离线交付' },
]
</script>

<template>
  <section id="top" class="hero">
    <div class="container hero-grid">
      <div class="copy">
        <span class="eyebrow">Apache 2.0 · 开源</span>
        <h1>
          从代码交付到服务运行
          <span class="grad">统一在一个平台完成</span>
        </h1>
        <p class="lead">
          管理应用与环境，编排构建发布和泳道验证，关联指标、日志与链路，
          并接入模型和智能体能力。一个控制面，贯穿开发、交付与运维。
        </p>
        <div class="cta">
          <a class="btn primary" href="/console/">
            进入经典控制台
            <span class="arrow">→</span>
          </a>
          <a class="btn ghost" href="/next/">
            新版控制台（试验版）
          </a>
          <a class="btn ghost" href="#quickstart">
            快速开始
          </a>
          <a class="btn ghost" href="https://github.com/aitoys/paas">
            <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
              <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0016 8c0-4.42-3.58-8-8-8z" />
            </svg>
            查看源码
          </a>
        </div>

        <dl class="stats">
          <div v-for="s in stats" :key="s.k" class="stat">
            <dt class="v mono">{{ s.v }}</dt>
            <dd class="k">{{ s.k }}</dd>
          </div>
        </dl>
      </div>

      <!-- 三条任务旅程；具体操作入口见下方使用路径。 -->
      <div class="diagram" role="img" aria-label="交付、运行和 AI 三条使用路径，由统一控制面组织">
        <div class="layer l1">
          <div class="layer-tag">01 · 交付应用</div>
          <div class="layer-body">
            <span class="chip">代码与变更</span>
            <span class="chip">预演与审批</span>
            <span class="chip">构建与发布</span>
          </div>
        </div>

        <div class="flow" data-label="观察发布结果" />

        <div class="layer l2 primary">
          <div class="layer-tag">02 · 运行与排障</div>
          <div class="layer-body core">
            <div class="core-title">从运行状态找到问题</div>
            <div class="plugins">
              <span class="plugin">实例</span>
              <span class="plugin">指标</span>
              <span class="plugin">日志</span>
              <span class="plugin">链路</span>
              <span class="plugin">操作记录</span>
            </div>
            <div class="meta mono">应用 · 环境 · 泳道，同一上下文下钻</div>
          </div>
        </div>

        <div class="flow" data-label="接入模型与自动化" />

        <div class="layer l3">
          <div class="layer-tag">03 · 接入 AI</div>
          <div class="layer-body">
            <span class="chip">模型与通道</span>
            <span class="chip">智能体与工作流</span>
            <span class="chip">智能研发</span>
          </div>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.hero {
  padding: 88px 0 64px;
}
.hero-grid {
  display: grid;
  grid-template-columns: 1fr;
  gap: 64px;
  align-items: center;
}
.copy {
  max-width: 580px;
}
h1 {
  font-size: clamp(32px, 3.6vw, 48px);
  font-weight: 800;
  margin: 18px 0 20px;
  letter-spacing: -0.03em;
}
.grad {
  display: block;
  background: linear-gradient(120deg, var(--brand) 0%, var(--brand-2) 50%, var(--accent) 110%);
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
}
.lead {
  font-size: 18px;
  line-height: 1.7;
  color: var(--text-dim);
  max-width: 540px;
  margin-bottom: 32px;
}
.cta {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
}
.arrow {
  transition: transform 0.15s;
}
.btn.primary:hover .arrow {
  transform: translateX(3px);
}

.stats {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 22px 32px;
  margin: 44px 0 0;
}
.stat .v {
  font-size: 28px;
  font-weight: 800;
  /* 渐变起点随主题（暗=白 / 亮=深字色），终点 accent 双主题通用 */
  background: linear-gradient(120deg, var(--stat-grad-from) 0%, var(--accent) 130%);
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
}
.stat .k {
  margin: 2px 0 0;
  font-size: 13px;
  color: var(--text-faint);
}

/* —— 三层架构示意 —— */
.diagram {
  display: flex;
  flex-direction: column;
  gap: 0;
  padding: 24px;
  background: var(--bg-elev);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow);
}
.layer {
  border: 1px solid var(--border);
  border-radius: 12px;
  padding: 14px 16px;
  background: var(--surface);
}
.layer.primary {
  border-color: rgba(99, 102, 241, 0.5);
  background: linear-gradient(180deg, var(--brand-soft), var(--surface));
  box-shadow: 0 0 0 1px rgba(99, 102, 241, 0.2) inset;
}
.layer-tag {
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.06em;
  text-transform: uppercase;
  color: var(--text-faint);
  margin-bottom: 10px;
}
.layer.primary .layer-tag {
  color: var(--brand-2);
}
.layer-body {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.chip {
  font-size: 12px;
  padding: 5px 10px;
  border-radius: 6px;
  background: var(--surface-2);
  border: 1px solid var(--border);
  color: var(--text-dim);
}
.core {
  flex-direction: column;
  gap: 10px;
  align-items: stretch;
}
.core-title {
  font-size: 13.5px;
  font-weight: 600;
  color: var(--text);
}
.plugins {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.plugin {
  font-size: 12px;
  font-weight: 600;
  padding: 5px 11px;
  border-radius: 6px;
  /* 背景/文字走 accent 变量（亮色自动切深青保证对比），描边固定青色调双主题可见 */
  background: var(--accent-soft);
  border: 1px solid rgba(34, 211, 238, 0.3);
  color: var(--accent);
}
.meta {
  font-size: 11px;
  color: var(--text-faint);
}

/* 旅程之间的连接线 */
.flow {
  position: relative;
  height: 34px;
  display: grid;
  place-items: center;
}
.flow::before {
  content: '';
  position: absolute;
  left: 50%;
  top: 0;
  bottom: 0;
  width: 2px;
  background: linear-gradient(180deg, transparent, var(--accent), transparent);
  transform: translateX(-50%);
  opacity: 0.6;
}
.flow::after {
  content: '';
  position: absolute;
  left: 50%;
  top: 0;
  width: 2px;
  height: 14px;
  background: var(--accent);
  transform: translateX(-50%);
  box-shadow: 0 0 8px var(--accent);
  animation: flowDown 1.8s linear infinite;
}
@keyframes flowDown {
  0% {
    top: -2px;
    opacity: 0;
  }
  25% {
    opacity: 1;
  }
  100% {
    top: 100%;
    opacity: 0;
  }
}
.flow[data-label] {
  /* 标签可选，保留 attr 供未来 */
}

@media (min-width: 980px) {
  .hero-grid {
    grid-template-columns: 1.05fr 0.95fr;
  }
}
@media (max-width: 560px) {
  h1 {
    font-size: 36px;
  }
  .stats {
    grid-template-columns: 1fr 1fr;
    gap: 16px;
  }
}

@media (prefers-reduced-motion: reduce) {
  .flow::after {
    animation: none;
    opacity: 0.4;
  }
}
</style>
