<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { desktop } from '../utils/desktop'
import Icon from './Icon.vue'

/** Desktop app started "as administrator": Windows (UIPI) silently blocks dragging files from Explorer into it. */
const elevated = ref(false)
onMounted(async () => {
  try { elevated.value = (await desktop()?.elevated?.()) === true } catch { elevated.value = false }
})
</script>
<template>
  <div v-if="elevated" class="banner" role="alert" data-testid="elevated-notice">
    <Icon name="warning" />
    <div>本程序正以“管理员身份”运行，Windows 会拦截从资源管理器拖入的文件，所以拖放没有反应。请关闭程序后用普通方式重新打开（不要右键“以管理员身份运行”，并取消 exe 属性 → 兼容性里的“以管理员身份运行此程序”）。现在仍可用“选择文件 / 选择文件夹”按钮或粘贴。</div>
  </div>
</template>
