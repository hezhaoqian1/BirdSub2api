<template>
  <button
    type="button"
    class="inline-flex h-6 w-6 flex-shrink-0 items-center justify-center rounded text-blue-600 transition-colors hover:bg-blue-50 disabled:cursor-not-allowed disabled:opacity-50 dark:text-blue-400 dark:hover:bg-blue-900/30"
    :disabled="busy || disabled"
    :aria-label="label"
    :title="label"
    @click="$emit('click', $event)"
  >
    <Icon name="refresh" size="xs" :class="{ 'animate-spin motion-reduce:animate-none': busy }" aria-hidden="true" />
  </button>
</template>

<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'

defineProps<{ label: string; busy?: boolean; disabled?: boolean }>()
defineEmits<{ click: [event: MouseEvent] }>()
</script>
