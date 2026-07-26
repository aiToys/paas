<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { ElContainer, ElHeader, ElAside, ElMain, ElMenu, ElMenuItem } from 'element-plus'

const route = useRoute()
const activeMenu = computed(() => route.path)

const menus = [
  { index: '/marketplace', label: '模型市场' },
  { index: '/deployments', label: '我的部署' },
  { index: '/playground', label: 'Playground' },
  { index: '/api-keys', label: 'API Key' },
  { index: '/usage', label: '用量与计费' },
]
</script>

<template>
  <el-container class="layout">
    <el-aside width="220px" class="aside">
      <div class="logo">PaaS 控制台</div>
      <el-menu :default-active="activeMenu" router>
        <el-menu-item v-for="m in menus" :key="m.index" :index="m.index">
          {{ m.label }}
        </el-menu-item>
      </el-menu>
    </el-aside>
    <el-container>
      <el-header class="header">{{ route.meta.title }}</el-header>
      <el-main>
        <router-view />
      </el-main>
    </el-container>
  </el-container>
</template>

<style scoped>
.layout {
  height: 100vh;
}
.aside {
  background: #001529;
  color: #fff;
}
.logo {
  height: 60px;
  line-height: 60px;
  text-align: center;
  font-weight: 600;
  color: #fff;
}
.header {
  background: #fff;
  border-bottom: 1px solid #eee;
  line-height: 60px;
  font-weight: 600;
}
:deep(.el-menu) {
  border-right: none;
}
</style>
