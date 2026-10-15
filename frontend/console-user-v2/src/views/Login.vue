<script setup lang="ts">
// 登录（spec §9.1）：全屏无壳——不渲染控制台壳，失败统一「用户名或密码错误」。
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useSessionStore } from '@/stores/session'
import { useEnvStore } from '@/stores/env'

const route = useRoute()
const router = useRouter()
const session = useSessionStore()
const envStore = useEnvStore()

const form = ref({ username: '', password: '' })
const loading = ref(false)

async function submit() {
  if (!form.value.username || !form.value.password) {
    ElMessage.warning('请输入用户名和密码')
    return
  }
  loading.value = true
  try {
    await session.login(form.value.username, form.value.password)
    // 登录是会话边界：App.vue 壳挂载早于登录且不随路由卸载，退出态挂载时的
    // loadProfile/loadEnvs 已 401 失败——此处必须重拉，否则顶栏头像/环境下拉空到下次刷新。
    void session.loadProfile()
    void envStore.loadEnvs()
    router.push((route.query.redirect as string) || '/')
  } finally {
    loading.value = false
  }
}

// 演示账号快切（spec §9.1，仅 demo）：生产关闭演示 seed 后这些账号不存在，快切失败有提示。
async function demoLogin(u: string, p: string) {
  form.value = { username: u, password: p }
  await submit()
}
</script>

<template>
  <div class="login-page">
    <div class="login-card">
      <div class="brand-row">
        <span class="brand-mark">P</span>
        <div class="brand-text">
          <div class="brand-name">一站式 PaaS 平台</div>
          <div class="brand-sub">新版控制台（试验）</div>
        </div>
      </div>
      <el-form :model="form" label-position="top" @submit.prevent="submit">
        <el-form-item label="用户名">
          <el-input v-model="form.username" placeholder="用户名" autofocus @keyup.enter="submit" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input
            v-model="form.password"
            type="password"
            placeholder="密码"
            show-password
            @keyup.enter="submit"
          />
        </el-form-item>
        <el-button type="primary" :loading="loading" style="width: 100%" @click="submit">
          登录
        </el-button>
      </el-form>
      <div class="demo-accounts">
        <p class="demo-title">演示账号快切</p>
        <div class="demo-row">
          <el-button
            v-for="d in session.DEMO_ACCOUNTS" :key="d.username"
            size="small" :disabled="loading" @click="demoLogin(d.username, d.password)"
          >
{{ d.label }}
</el-button>
        </div>
      </div>
      <div class="alt-entry">
        <a href="/console/">继续使用经典版 →</a>
      </div>
    </div>
  </div>
</template>

<style scoped>
.login-page {
  min-height: 100vh;
  display: grid;
  place-items: center;
  background: var(--surface-2, #0f131c);
  padding: 20px;
}
.login-card {
  width: 360px;
  padding: 32px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
}
.brand-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-bottom: 24px;
}
.brand-mark {
  width: 40px;
  height: 40px;
  display: grid;
  place-items: center;
  border-radius: 10px;
  background: var(--brand);
  color: #fff;
  font-size: 18px;
  font-weight: 700;
}
.brand-name {
  font-size: 16px;
  font-weight: 700;
}
.brand-sub {
  font-size: 12px;
  color: var(--text-faint);
  margin-top: 2px;
}
.demo-accounts {
  margin-top: 18px;
  padding-top: 14px;
  border-top: 1px dashed var(--border);
}
.demo-title {
  margin: 0 0 8px;
  font-size: 11.5px;
  color: var(--text-faint);
}
.demo-row {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.alt-entry {
  margin-top: 16px;
  text-align: center;
  font-size: 12.5px;
}
.alt-entry a {
  color: var(--text-dim);
  text-decoration: none;
}
.alt-entry a:hover {
  color: var(--brand);
}
</style>
