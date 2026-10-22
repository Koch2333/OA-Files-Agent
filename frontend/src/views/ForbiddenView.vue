<script setup lang="ts">
import { ROLE_LABEL } from '../utils/authz'
import { useAuth } from '../stores/auth'
import StateView from '../components/StateView.vue'
const auth = useAuth()
</script>
<template>
  <StateView :empty="true" empty-icon="lock" empty-title="无权访问此页面" :empty-hint="`你当前的角色是“${auth.role ? ROLE_LABEL[auth.role] : '未知'}”，没有权限使用该功能。如需开通请联系管理员。`">
    <template #empty-action><RouterLink to="/" class="btn tonal">返回工作台</RouterLink></template>
  </StateView>
</template>
