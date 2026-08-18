<template>
  <div class="login-wrap">
    <div class="login-card">
      <h2>PaaS 用户控制台</h2>
      <el-form :model="form" label-position="top" @submit.prevent="submit">
        <el-form-item label="用户名">
          <el-input v-model="form.username" placeholder="acme-admin" autocomplete="username" />
        </el-form-item>
        <el-form-item label="密码">
          <el-input
            v-model="form.password"
            type="password"
            show-password
            autocomplete="current-password"
            @keyup.enter="submit"
          />
        </el-form-item>
        <el-button type="primary" :loading="loading" style="width: 100%" @click="submit">
          登录
        </el-button>
      </el-form>
      <div class="demo-accounts">
        <p class="demo-title">演示账号快切</p>
        <el-button
          v-for="d in session.DEMO_ACCOUNTS"
          :key="d.username"
          size="small"
          :disabled="loading"
          @click="quickLogin(d)"
        >
          {{ d.label }}
        </el-button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useSessionStore, type DemoAccount } from '@/stores/session'

const session = useSessionStore()
const router = useRouter()
const route = useRoute()
const loading = ref(false)

// dev 环境预填演示账号（acme-admin/123456）；生产留空。
const form = reactive({
  username: import.meta.env.DEV ? 'acme-admin' : '',
  password: import.meta.env.DEV ? '123456' : '',
})

async function submit() {
  if (!form.username || !form.password) {
    ElMessage.warning('请输入用户名和密码')
    return
  }
  loading.value = true
  try {
    await session.login(form.username, form.password)
    router.push((route.query.redirect as string) || '/applications')
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '登录失败')
  } finally {
    loading.value = false
  }
}

async function quickLogin(d: DemoAccount) {
  loading.value = true
  try {
    await session.login(d.username, d.password)
    // 与 submit 同款：优先回跳 redirect（含 ?env= 等 query），保持登录前上下文
    router.push((route.query.redirect as string) || '/applications')
  } catch (e) {
    ElMessage.error(e instanceof Error ? e.message : '登录失败')
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.login-wrap {
  position: fixed;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--el-bg-color-page);
}
.login-card {
  width: 380px;
  padding: 32px;
  background: var(--el-bg-color);
  border-radius: 12px;
  box-shadow: var(--el-box-shadow);
}
.login-card h2 {
  margin: 0 0 24px;
  text-align: center;
}
.demo-accounts {
  margin-top: 24px;
  border-top: 1px solid var(--el-border-color);
  padding-top: 16px;
}
.demo-title {
  margin: 0 0 12px;
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.demo-accounts .el-button {
  margin: 0 8px 8px 0;
}
</style>
