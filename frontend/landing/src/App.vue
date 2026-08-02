<script setup lang="ts">
// 官网展示页组装：导航 / Hero / 能力 / 架构 / 快速开始 / CTA + Footer。
import SiteNav from './components/SiteNav.vue'
import Hero from './components/Hero.vue'
import Features from './components/Features.vue'
import Architecture from './components/Architecture.vue'
import Quickstart from './components/Quickstart.vue'
import SiteFooter from './components/SiteFooter.vue'
</script>

<template>
  <SiteNav />
  <main>
    <Hero />
    <Features />
    <Architecture />
    <Quickstart />
  </main>
  <SiteFooter />
</template>
