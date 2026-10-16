<script setup lang="ts">
import Icon from './Icon.vue'
withDefaults(defineProps<{ title: string; description?: string; error?: boolean; compact?: boolean }>(), { description: '', error: false, compact: false })
</script>
<template>
  <div class="state-panel" :class="{ 'is-error': error, compact }" :role="error ? 'alert' : 'status'">
    <Icon :name="error ? 'activity' : 'layers'" :size="26" />
    <h3>{{ title }}</h3>
    <p v-if="description">{{ description }}</p>
    <div v-if="$slots.default" class="state-actions"><slot /></div>
  </div>
</template>
