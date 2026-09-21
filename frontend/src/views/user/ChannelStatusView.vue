<template>
  <ChannelStatusHybridView v-if="hybrid?.config.enabled" :initial="hybrid.items" @disabled="disableHybrid" />
  <ChannelStatusV1View v-else-if="checked && isV1" />
  <ChannelStatusV2View v-else-if="checked" />
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { getHybridSnapshot, type HybridSnapshot } from '@/api/channelMonitorHybrid'
import ChannelStatusHybridView from './ChannelStatusHybridView.vue'
import { isChannelMonitorV1Mode } from '@/utils/featureFlags'
import ChannelStatusV1View from './ChannelStatusV1View.vue'
import ChannelStatusV2View from './ChannelStatusV2View.vue'

const isV1 = computed(() => isChannelMonitorV1Mode())
const hybrid = ref<HybridSnapshot | null>(null)
const checked = ref(false)
function disableHybrid() { if (hybrid.value) hybrid.value.config.enabled = false }
onMounted(async () => { try { hybrid.value = await getHybridSnapshot() } catch { hybrid.value = null } finally { checked.value = true } })
</script>
