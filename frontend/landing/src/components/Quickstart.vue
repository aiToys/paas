<script setup lang="ts">
import { ref } from 'vue'

// 快速开始：三种真实路径（Docker / K8s / 本地），与 README 对齐。
// tab 切换 + 一键复制。
type Mode = 'docker' | 'k8s' | 'local'

const mode = ref<Mode>('docker')

const modes: { key: Mode; label: string; hint: string }[] = [
  { key: 'docker', label: 'Docker 一键', hint: '最快体验' },
  { key: 'k8s', label: 'K8s 部署', hint: 'Helm 安装' },
  { key: 'local', label: '本地开发', hint: '前后端联调' },
]

const scripts: Record<Mode, { cmd: string; com: string }[]> = {
  docker: [
    { cmd: 'docker compose up --build', com: '构建并启动 Platform Core（:8080）' },
    {
      cmd: 'curl http://localhost:8080/livez',
      com: '验证控制面存活；浏览器访问 localhost:8080/console/ 或 /next/',
    },
  ],
  k8s: [
    { cmd: 'helm install paas deploy/charts/paas --namespace paas --create-namespace', com: '在仓库根目录执行；需镜像可拉取、默认 StorageClass 与集群权限' },
    { cmd: 'kubectl -n paas get pods', com: '检查控制面和数据库；访问方式及生产配置见部署指南' },
  ],
  local: [
    { cmd: 'make build && PAAS_K8S_ENABLED=false ./bin/core', com: 'Go 控制面 :8080；关闭自动连接本机 Kubernetes' },
    { cmd: 'cd frontend && pnpm install && pnpm --dir console-user-v2 dev', com: '在另一终端运行；新版控制台 http://localhost:5176/next/' },
  ],
}

// 复制反馈按命令行索引独立维护：之前用单 ref，点行 1 复制会让所有行同时显示 ✓。
const copiedIdx = ref<number | null>(null)
const copyError = ref('')
let copyTimer: number | undefined
async function copy(line: string, idx: number) {
  copyError.value = ''
  try {
    await navigator.clipboard.writeText(line)
    copiedIdx.value = idx
    if (copyTimer) window.clearTimeout(copyTimer)
    copyTimer = window.setTimeout(() => (copiedIdx.value = null), 1400)
  } catch {
    copyError.value = '无法自动复制，请手动选择命令。'
  }
}
</script>

<template>
  <section id="quickstart" class="section">
    <div class="container">
      <div class="section-head center">
        <span class="eyebrow">快速开始</span>
        <h2 class="section-title">选择适合你的启动方式</h2>
        <p class="section-sub">先克隆仓库并进入根目录。Docker 用于本地体验；集群与生产部署请按指南准备依赖。</p>
      </div>

      <div class="qs">
        <div class="tabs">
          <button
            v-for="m in modes"
            :key="m.key"
            class="tab"
            :class="{ on: mode === m.key }"
            :aria-pressed="mode === m.key"
            @click="mode = m.key"
          >
            <span class="t-label">{{ m.label }}</span>
            <span class="t-hint">{{ m.hint }}</span>
          </button>
        </div>

        <p v-if="copyError" class="api-note" role="status">{{ copyError }}</p>
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

        <p class="api-note">仅启用演示 seed 时，可用 <span class="key mono">acme-admin / 123456</span> 登录。生产须关闭演示凭证并配置安全基线。</p>
        <p class="api-note">真实构建需配置 builder 与镜像库；推理需可用模型通道与凭证；监控需接入采集后端。<a href="/docs/guide/quickstart.html">完整上手指南 →</a> · <a href="/docs/deploy/">部署配置 →</a></p>
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

/* 终端窗口刻意保持深色（亮色模式下代码块/终端仍深色的惯例）——
   边框须固定浅色调：若用 --border 变量，亮色下变为深色系画在深底上不可见。 */
.terminal {
  background: #060912;
  border: 1px solid rgba(255, 255, 255, 0.14);
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
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
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
  color: #6b7390; /* 终端固定深色，文字不走主题变量（亮色下会变深色调不可读） */
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
  color: #22d3ee; /* 终端内固定配色，不随主题（理由同 term-title） */
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
.term-line:hover .copy,
.term-line:focus-within .copy {
  opacity: 1;
}
.copy:hover {
  border-color: var(--accent);
  color: var(--accent);
}
.com {
  margin-top: 6px;
  font-size: 12.5px;
  color: #9aa4bd; /* 终端内固定配色，不随主题 */
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
