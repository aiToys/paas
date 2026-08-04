<script setup lang="ts">
import { ref } from 'vue'
import LogoMark from './LogoMark.vue'

// 顶部 sticky 导航：logo + 锚点菜单 + 主 CTA。移动端折叠为汉堡。
const open = ref(false)
const links = [
  { href: '#capabilities', label: '能力' },
  { href: '#architecture', label: '架构' },
  { href: '#quickstart', label: '快速开始' },
  { href: 'https://github.com/aitoys/paas', label: 'GitHub', ext: true },
]
</script>

<template>
  <header class="nav">
    <div class="container bar">
      <a class="home" href="#top" @click="open = false">
        <LogoMark :size="30" />
      </a>

      <nav class="links" :class="{ open }">
        <a v-for="l in links" :key="l.href" :href="l.href" @click="open = false">
          {{ l.label }}
          <span v-if="l.ext" class="ext">↗</span>
        </a>
        <a class="btn primary cta-mobile" href="/console/">立即体验</a>
      </nav>

      <div class="right">
        <a class="btn ghost console-link" href="/console/">控制台</a>
        <a class="btn ghost admin-link" href="/admin/">管理后台</a>
        <a class="btn ghost" href="https://github.com/aitoys/paas">
          <svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
            <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0016 8c0-4.42-3.58-8-8-8z" />
          </svg>
          Star
        </a>
        <a class="btn primary cta-desktop" href="/console/">立即体验</a>
        <button class="burger" :aria-expanded="open" @click="open = !open" aria-label="菜单">
          <span /><span /><span />
        </button>
      </div>
    </div>
  </header>
</template>

<style scoped>
.nav {
  position: sticky;
  top: 0;
  z-index: 50;
  background: rgba(10, 14, 26, 0.72);
  backdrop-filter: saturate(140%) blur(14px);
  -webkit-backdrop-filter: saturate(140%) blur(14px);
  border-bottom: 1px solid var(--border);
}
.bar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 64px;
}
.home {
  display: inline-flex;
}
.links {
  display: none;
  align-items: center;
  gap: 28px;
}
.links a {
  font-size: 14.5px;
  font-weight: 500;
  color: var(--text-dim);
}
.links a:hover {
  color: var(--text);
}
.ext {
  font-size: 11px;
  opacity: 0.7;
}
.right {
  display: flex;
  align-items: center;
  gap: 10px;
}
.burger {
  display: inline-flex;
  flex-direction: column;
  gap: 4px;
  width: 38px;
  height: 38px;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--border);
  border-radius: 9px;
  background: var(--surface);
  cursor: pointer;
}
.burger span {
  width: 16px;
  height: 2px;
  background: var(--text);
  border-radius: 1px;
}
.cta-mobile {
  display: none;
}

@media (min-width: 880px) {
  .links {
    display: inline-flex;
  }
  .burger {
    display: none;
  }
  .cta-mobile {
    display: none !important;
  }
}

/* 移动端展开菜单 */
@media (max-width: 879px) {
  .links.open {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: 4px;
    position: absolute;
    top: 64px;
    left: 0;
    right: 0;
    padding: 12px 28px 20px;
    background: var(--bg-elev);
    border-bottom: 1px solid var(--border);
    box-shadow: var(--shadow);
  }
  .links.open a {
    padding: 12px 4px;
    border-bottom: 1px solid var(--border);
  }
  .links.open .cta-mobile {
    display: inline-flex;
    margin-top: 8px;
    justify-content: center;
  }
  .cta-desktop {
    display: none;
  }
}
</style>
