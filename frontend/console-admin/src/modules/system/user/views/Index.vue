<template>
  <div class="user-auth-page">
    <el-tabs v-model="tab">
      <el-tab-pane
        label="用户"
        name="users"
        lazy
      >
        <UserList />
      </el-tab-pane>
      <el-tab-pane
        label="内置角色与权限"
        name="roles"
        lazy
      >
        <RoleList />
      </el-tab-pane>
    </el-tabs>
  </div>
</template>

<script lang="ts" setup>
// 用户与授权（spec 2026-10-19 §5 页内收敛）：用户 / 内置角色与权限两 tab，
// 复用既有 List 组件（读写能力不变）；?tab= 定位（/system/role 旧链接重定向至此）。
// 角色为内置三预设（tenant-admin/developer/viewer），无自定义角色 CRUD，故列表只读展示。
import { useRouteTab } from '@/app/composables'
import UserList from './List.vue'
import RoleList from '@/modules/system/role/views/List.vue'

const tab = useRouteTab('users', ['users', 'roles'])
</script>

<style scoped>
.user-auth-page {
  padding: 12px;
}
</style>
