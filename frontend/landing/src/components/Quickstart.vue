<script setup lang="ts">
import { ref } from 'vue'

// 快速开始：三种真实路径（Docker / K8s / 本地），与 README 对齐。
// tab 切换 + 一键复制。
type Mode = 'docker' | 'k8s' | 'local'

const mode = ref<Mode>('docker')

const modes: { key: Mode; label: string; hint: string }[] = [
  { key: 'docker', label: 'Docker 一键', hint: '最快体验' },
  { key: 'k8s', label: 'K8s 部署', hint: '生产单镜像' },
  { key: 'local', label: '本地开发', hint: '前后端联调' },
]

const scripts: Record<Mode, { cmd: string; com: string }[]> = {
  docker: [
    { cmd: 'docker compose up --build', com: '构建并启动 Platform Core（:8080）' },
    {
      cmd: 'curl -H "Authorization: Bearer sk-acme-admin" http://localhost:8080/livez',
      com: '探针验证（三预设演示 Key）',
    },
  ],
  k8s: [
    { cmd: './scripts/deploy-k8s.sh', com: '前端 embed 单镜像 → push registry → helm upgrade' },
    { cmd: 'kubectl -n paas get pods', com: '数据面 CRD + Reconciler in-cluster 自动启用' },
  ],
  local: [
    { cmd: 'make build && ./bin/core', com: 'Go 控制面 :8080（默认 Key sk-acme-admin）' },
    { cmd: 'cd frontend && pnpm install && pnpm dev:user', com: '用户控制台 :5174' },
  ],
}

// 复制反馈按命令行索引独立维护：之前用单 ref，点行 1 复制会让所有行同时显示 ✓。
const copiedIdx = ref<number | null>(null)
let copyTimer: number | undefined
async function copy(line: string, idx: number) {
  try {
    await navigator.clipboard.writeText(line)
    copiedIdx.value = idx
    if (copyTimer) window.clearTimeout(copyTimer)
    copyTimer = window.setTimeout(() => (copiedIdx.value = null), 1400)
  } catch {
    /* 剪贴板不可用（非 HTTPS）—— 静默 */
  }
}
</script>

<template>
  <section id="quickstart" class="section">
    <div class="container">
      <div class="section-head center">
        <span class="eyebrow">快速开始</span>
        <h2 class="section-title">三分钟跑起来</h2>
        <p class="section-sub">选一条路径，点行末复制。三预设演示 Key 开箱可用。</p>
      </div>

      <div class="qs">
        <div class="tabs">
          <button
            v-for="m in modes"
            :key="m.key"
            class="tab"
            :class="{ on: mode === m.key }"
            @click="mode = m.key"
          >
            <span class="t-label">{{ m.label }}</span>
            <span class="t-hint">{{ m.hint }}</span>
          </button>
        </div>

        <div class="terminal">
          <div class="term-bar">
            <span class="dot r" /><span class="dot y" /><span class="dot g" />
            <span class="term-title mono">bash — paas</span>
          </div>
          <div class="term-body">
            <div v-for="(line, i) in scripts[mode]" :key="i" class="term-line">
              <span class="prompt mono">$</span>
              <code class="mono cmd">{{ line.cmd }}</code>
              <button class="copy" :title="'复制'" @click="copy(line.cmd, i)">
                {{ copiedIdx === i ? '✓' : '⧉' }}
              </button>
              <div class="com">{{ line.com }}</div>
            </div>
          </div>
        </div>

        <p class="api-note">
          <span class="key mono">sk-acme-admin</span> Acme 管理员 ·
          <span class="key mono">sk-globex-admin</span> Globex 管理员 ·
          <span class="key mono">sk-acme-dev</span> Acme 开发者（生产只读）
        </p>
      </div>
    </div>
  </section>
</template>

<style scoped>
.qs {
  max-width: 820px;
  margin: 0 auto;
}
.tabs {
  display: flex;
  gap: 8px;
  margin-bottom: 16px;
  flex-wrap: wrap;
}
.tab {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
  padding: 10px 16px;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--surface);
  cursor: pointer;
  transition: all 0.15s;
  font-family: inherit;
}
.tab:hover {
  border-color: var(--border-strong);
}
.tab.on {
  border-color: var(--brand);
  background: var(--brand-soft);
}
.t-label {
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
}
.t-hint {
  font-size: 11.5px;
  color: var(--text-faint);
}

.terminal {
  background: #060912;
  border: 1px solid var(--border-strong);
  border-radius: var(--radius);
  overflow: hidden;
  box-shadow: var(--shadow);
}
.term-bar {
  display: flex;
  align-items: center;
  gap: 7px;
  padding: 11px 14px;
  background: #0d1220;
  border-bottom: 1px solid var(--border);
}
.dot {
  width: 11px;
  height: 11px;
  border-radius: 50%;
}
.dot.r {
  background: #ff5f57;
}
.dot.y {
  background: #febc2e;
}
.dot.g {
  background: #28c840;
}
.term-title {
  margin-left: 8px;
  font-size: 12px;
  color: var(--text-faint);
}
.term-body {
  padding: 20px;
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.term-line {
  position: relative;
}
.prompt {
  color: var(--accent);
  margin-right: 10px;
  user-select: none;
}
.cmd {
  font-size: 14px;
  color: #e6e9f2;
  word-break: break-all;
}
.copy {
  position: absolute;
  top: -2px;
  right: 0;
  border: 1px solid var(--border);
  border-radius: 6px;
  background: var(--surface);
  color: var(--text-dim);
  padding: 3px 8px;
  font-size: 13px;
  cursor: pointer;
  opacity: 0;
  transition: opacity 0.15s;
}
.term-line:hover .copy {
  opacity: 1;
}
.copy:hover {
  border-color: var(--accent);
  color: var(--accent);
}
.com {
  margin-top: 6px;
  font-size: 12.5px;
  color: var(--text-faint);
}

.api-note {
  margin-top: 22px;
  text-align: center;
  font-size: 13px;
  color: var(--text-dim);
}
.key {
  display: inline-block;
  padding: 2px 8px;
  margin: 0 2px;
  border-radius: 5px;
  background: var(--surface-2);
  border: 1px solid var(--border);
  color: var(--accent);
  font-size: 12px;
}

@media (max-width: 560px) {
  .copy {
    opacity: 1;
  }
}
</style>
