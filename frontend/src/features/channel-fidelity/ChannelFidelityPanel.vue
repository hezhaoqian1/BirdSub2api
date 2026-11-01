<template>
  <div class="space-y-5">
    <div v-if="error" role="alert" class="rounded-xl bg-red-50 p-4 text-sm text-red-600 dark:bg-red-500/10 dark:text-red-300">{{ error }}</div>
    <div v-if="notice" role="status" class="rounded-xl bg-emerald-50 p-4 text-sm text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300">{{ notice }}</div>

    <div class="grid grid-cols-2 gap-4 xl:grid-cols-4">
      <div
        v-for="metric in summary"
        :key="metric.label"
        class="rounded-2xl border border-gray-200/80 bg-white p-5 dark:border-dark-700 dark:bg-dark-800"
      >
        <p class="text-xs text-gray-500">{{ metric.label }}</p>
        <div class="mt-3 flex items-end justify-between">
          <strong class="font-mono text-3xl font-semibold" :class="metric.color">{{ metric.value }}</strong>
          <span class="text-[11px] text-gray-400">{{ metric.hint }}</span>
        </div>
      </div>
    </div>

    <section class="rounded-2xl border border-gray-200/80 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
      <div class="flex flex-wrap items-start justify-between gap-4">
        <div class="max-w-2xl">
          <h2 class="text-sm font-semibold text-gray-900 dark:text-white">保真体检</h2>
          <p class="mt-1 text-xs leading-5 text-gray-500">
            定时用每个 DeepSeek 账号自己的 key 直接发一个小请求：system 里放暗号、关闭思考、要求 JSON 输出、做一道加法，
            检查上游有没有偷偷丢掉提示词、关不掉思考或注入隐藏提示词。不通过时隔一段时间复测，复测仍不通过才推送企业微信。
          </p>
          <p class="mt-2 text-xs text-gray-500">
            企业微信推送：
            <span :class="status?.notify_enabled ? 'text-emerald-600' : 'text-amber-600'">{{ status?.notify_enabled ? '已开启' : '已关闭' }}</span>
            <span class="text-gray-400">（在「系统设置 → 安全 → 告警」里切换）</span>
            · 最近一轮：{{ status?.last_sweep_at ? formatTime(status.last_sweep_at) : '还没有' }}
          </p>
        </div>
        <div class="flex flex-wrap items-center gap-3">
          <button class="btn btn-secondary text-xs" :disabled="busy" data-test="fidelity-refresh" @click="refresh()">刷新</button>
          <button class="btn btn-primary text-xs" :disabled="busy || running" data-test="fidelity-run-all" @click="runAllNow">
            {{ running ? '体检中…' : '立即全部体检' }}
          </button>
        </div>
      </div>

      <form v-if="form" class="mt-5 grid grid-cols-1 gap-4 border-t border-gray-100 pt-5 dark:border-dark-700 sm:grid-cols-2 xl:grid-cols-5" @submit.prevent="saveConfig">
        <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300 xl:col-span-5">
          <input v-model="form.enabled" type="checkbox" data-test="fidelity-enabled" />
          定时体检（关闭后仍可手动体检）
        </label>
        <label class="fidelity-label">
          间隔（分钟）
          <input v-model.number="form.interval_minutes" class="input mt-2" type="number" min="5" max="1440" required data-test="fidelity-interval" />
        </label>
        <label class="fidelity-label">
          复测等待（秒，0 = 不复测）
          <input v-model.number="form.recheck_seconds" class="input mt-2" type="number" min="0" max="600" required />
        </label>
        <label class="fidelity-label">
          prompt_tokens 允许偏差
          <input v-model.number="form.prompt_token_tolerance" class="input mt-2" type="number" min="0" max="1000" required />
        </label>
        <label class="fidelity-label sm:col-span-2">
          体检模型（逗号分隔，按账号映射换算成上游模型）
          <input v-model="modelsText" class="input mt-2" required data-test="fidelity-models" />
        </label>
        <div class="flex justify-end xl:col-span-5">
          <button class="btn btn-primary text-xs" :disabled="busy" data-test="fidelity-save">保存设置</button>
        </div>
      </form>
    </section>

    <section class="overflow-hidden rounded-2xl border border-gray-200/80 bg-white dark:border-dark-700 dark:bg-dark-800">
      <div class="flex flex-wrap items-center justify-between gap-2 border-b border-gray-100 px-5 py-4 dark:border-dark-700">
        <h2 class="text-sm font-semibold text-gray-900 dark:text-white">各账号最新结果</h2>
        <span class="text-xs text-gray-400">✓ 通过 · ✗ 不通过 · – 无法判断 · 小圆点为近 24 小时每次体检</span>
      </div>
      <div class="overflow-x-auto">
        <table class="w-full whitespace-nowrap text-left text-xs">
          <thead class="bg-gray-50 text-gray-500 dark:bg-dark-900/50">
            <tr>
              <th class="px-4 py-3 font-medium">账号</th>
              <th class="px-4 py-3 font-medium">模型</th>
              <th class="px-3 py-3 text-center font-medium">system</th>
              <th class="px-3 py-3 text-center font-medium">关思考</th>
              <th class="px-3 py-3 text-center font-medium">JSON</th>
              <th class="px-3 py-3 text-center font-medium">算术</th>
              <th class="px-3 py-3 font-medium">prompt / 基线</th>
              <th class="px-4 py-3 font-medium">最近体检</th>
              <th class="px-4 py-3 font-medium">近 24 小时</th>
              <th class="px-4 py-3 font-medium">操作</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-for="row in rows" :key="rowKey(row)" :class="row.schedulable ? '' : 'opacity-60'" :data-test="`fidelity-row-${rowKey(row)}`">
              <td class="px-4 py-3">
                <div class="font-medium text-gray-900 dark:text-white">{{ row.account_name }} <span class="text-gray-400">#{{ row.account_id }}</span></div>
                <div class="mt-1 flex gap-1">
                  <span v-if="!row.schedulable" class="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] text-gray-500 dark:bg-dark-700">已停用 · 不告警</span>
                  <span v-if="row.alerting" class="rounded bg-red-50 px-1.5 py-0.5 text-[10px] text-red-600 dark:bg-red-500/10">告警中</span>
                </div>
              </td>
              <td class="px-4 py-3 text-gray-700 dark:text-gray-300">
                {{ row.model }}
                <div v-if="row.upstream_model !== row.model" class="text-[10px] text-gray-400">上游 {{ row.upstream_model }}</div>
              </td>
              <template v-if="row.latest && row.latest.status !== 'error'">
                <td v-for="key in checkKeys" :key="key" class="px-3 py-3 text-center text-sm font-semibold" :class="checkClass(row.latest[key])">
                  {{ checkMark(row.latest[key]) }}
                </td>
              </template>
              <td v-else colspan="4" class="px-3 py-3 text-center text-gray-400">
                {{ row.latest ? '请求出错' : '还没有体检' }}
              </td>
              <td class="px-3 py-3 font-mono" :class="row.latest?.prompt_check === 'fail' ? 'text-red-500' : 'text-gray-500'">
                {{ row.latest?.prompt_tokens ?? '–' }} / {{ row.baseline_prompt_tokens ?? '待记录' }}
              </td>
              <td class="px-4 py-3">
                <template v-if="row.latest">
                  <span :class="statusClass(row.latest.status)">{{ statusLabel(row.latest.status) }}</span>
                  <div class="text-[10px] text-gray-400">{{ formatTime(row.latest.checked_at) }}<template v-if="row.latest.latency_ms != null"> · {{ (row.latest.latency_ms / 1000).toFixed(1) }}s</template></div>
                  <div v-if="row.latest.message" class="max-w-xs truncate text-[10px] text-gray-500" :title="row.latest.message">{{ row.latest.message }}</div>
                </template>
                <span v-else class="text-gray-400">–</span>
              </td>
              <td class="px-4 py-3">
                <div class="flex max-w-[16rem] flex-wrap gap-0.5">
                  <span
                    v-for="(point, i) in row.history"
                    :key="i"
                    class="inline-block rounded-full"
                    :class="[dotClass(point.status), point.is_recheck ? 'h-1.5 w-1.5' : 'h-2 w-2']"
                    :title="`${formatTime(point.checked_at)} ${point.is_recheck ? '复测' : '体检'}：${statusLabel(point.status)}${point.message ? '，' + point.message : ''}`"
                  />
                  <span v-if="!row.history.length" class="text-gray-400">–</span>
                </div>
              </td>
              <td class="px-4 py-3">
                <div class="flex gap-2">
                  <button class="text-blue-600 hover:underline disabled:opacity-40" :disabled="!!checking" :data-test="`fidelity-check-${rowKey(row)}`" @click="checkOne(row)">
                    {{ checking === rowKey(row) ? '体检中…' : '体检' }}
                  </button>
                  <button class="text-blue-600 hover:underline disabled:opacity-40" :disabled="!row.latest" @click="detail = row">详情</button>
                  <button class="text-gray-500 hover:underline disabled:opacity-40" :disabled="row.baseline_prompt_tokens == null || busy" @click="resetRowBaseline(row)">重置基线</button>
                </div>
              </td>
            </tr>
            <tr v-if="!rows.length">
              <td colspan="10" class="px-5 py-10 text-center text-gray-400">
                {{ status && !status.available ? '体检服务不可用（上游余额查询未初始化）' : busy ? '加载中…' : '没有可体检的 DeepSeek API Key 账号' }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <BaseDialog :show="!!detail" :title="detail ? `${detail.account_name} #${detail.account_id} · ${detail.model}` : ''" width="wide" @close="detail = null">
      <div v-if="detail && detail.latest" class="space-y-4 text-xs text-gray-600 dark:text-gray-300">
        <div class="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <div v-for="item in detailItems(detail.latest)" :key="item.label" class="rounded-xl bg-gray-50 p-3 dark:bg-dark-900">
            <div class="text-gray-400">{{ item.label }}</div>
            <div class="mt-1 font-mono text-sm" :class="item.class">{{ item.value }}</div>
          </div>
        </div>
        <p v-if="detail.latest.message" class="rounded-xl bg-amber-50 p-3 text-amber-800 dark:bg-amber-500/10 dark:text-amber-300">{{ detail.latest.message }}</p>
        <div>
          <div class="mb-1 text-gray-400">上游返回（正文与思考节选）</div>
          <pre class="max-h-72 overflow-auto whitespace-pre-wrap break-all rounded-xl bg-gray-900 p-3 font-mono text-[11px] text-gray-100">{{ detail.latest.response_excerpt || '（空）' }}</pre>
        </div>
        <p class="text-gray-400">
          请求：system 暗号 + thinking.type=disabled + reasoning_effort=none + response_format=json_object + 一道三位数加法，max_tokens 300。
          上游模型 {{ detail.upstream_model }}，检测于 {{ formatTime(detail.latest.checked_at) }}{{ detail.latest.is_recheck ? '（复测）' : '' }}。
        </p>
      </div>
    </BaseDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { extractApiErrorMessage } from '@/utils/apiError'
import {
  getStatus,
  resetBaseline,
  runAll,
  runOne,
  updateConfig,
  type ChannelFidelityCheck,
  type ChannelFidelityConfig,
  type ChannelFidelityRow,
  type ChannelFidelityStatusView,
  type FidelityCheckState,
  type FidelityStatus,
} from '@/api/admin/channelFidelity'

type CheckKey = 'system_check' | 'thinking_check' | 'json_check' | 'answer_check'
const checkKeys: CheckKey[] = ['system_check', 'thinking_check', 'json_check', 'answer_check']

const status = ref<ChannelFidelityStatusView | null>(null)
const form = ref<ChannelFidelityConfig | null>(null)
const modelsText = ref('')
const busy = ref(false)
const runningLocal = ref(false)
const checking = ref('')
const error = ref('')
const notice = ref('')
const detail = ref<ChannelFidelityRow | null>(null)

const rows = computed(() => status.value?.rows ?? [])
const running = computed(() => runningLocal.value || !!status.value?.running)
const summary = computed(() => {
  const list = rows.value
  const failing = list.filter(r => r.alerting || r.latest?.status === 'fail').length
  return [
    { label: '体检目标', value: list.length, hint: '账号 × 上游模型', color: 'text-gray-900 dark:text-white' },
    { label: '全部通过', value: list.filter(r => r.latest?.status === 'pass').length, hint: '最近一次', color: 'text-emerald-600' },
    { label: '不通过', value: failing, hint: '含告警中', color: 'text-red-500' },
    { label: '请求出错', value: list.filter(r => r.latest?.status === 'error').length, hint: '不算保真问题', color: 'text-amber-600' },
  ]
})

function rowKey(row: ChannelFidelityRow) {
  return `${row.account_id}-${row.upstream_model}`
}
function checkMark(state: FidelityCheckState) {
  return state === 'pass' ? '✓' : state === 'fail' ? '✗' : '–'
}
function checkClass(state: FidelityCheckState) {
  return state === 'pass' ? 'text-emerald-600' : state === 'fail' ? 'text-red-500' : 'text-gray-400'
}
function statusLabel(s: FidelityStatus) {
  return s === 'pass' ? '全部通过' : s === 'fail' ? '不通过' : '请求出错'
}
function statusClass(s: FidelityStatus) {
  return s === 'pass' ? 'text-emerald-600' : s === 'fail' ? 'font-medium text-red-500' : 'text-amber-600'
}
function dotClass(s: FidelityStatus) {
  return s === 'pass' ? 'bg-emerald-500' : s === 'fail' ? 'bg-red-500' : 'bg-amber-400'
}
function formatTime(value: string) {
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return value
  return d.toLocaleString('zh-CN', { hour12: false, month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit' })
}
function detailItems(c: ChannelFidelityCheck) {
  const mark = (s: FidelityCheckState) => ({ value: s === 'pass' ? '通过' : s === 'fail' ? '不通过' : '无法判断', class: checkClass(s) })
  return [
    { label: '结论', value: statusLabel(c.status), class: statusClass(c.status) },
    { label: 'system 暗号', ...mark(c.system_check) },
    { label: '关闭思考', ...mark(c.thinking_check) },
    { label: 'JSON 模式', ...mark(c.json_check) },
    { label: '算术题', ...mark(c.answer_check) },
    { label: 'prompt / 基线', value: `${c.prompt_tokens ?? '–'} / ${c.baseline_prompt_tokens ?? '待记录'}`, class: checkClass(c.prompt_check) },
    { label: '思考字数 / token', value: `${c.reasoning_chars} / ${c.reasoning_tokens ?? '–'}`, class: c.thinking_check === 'fail' ? 'text-red-500' : '' },
    { label: 'HTTP / 耗时', value: `${c.http_status ?? '–'} / ${c.latency_ms != null ? (c.latency_ms / 1000).toFixed(1) + 's' : '–'}`, class: '' },
  ]
}
function report(cause: unknown) {
  error.value = extractApiErrorMessage(cause, '操作失败，请稍后重试')
}

async function refresh(updateForm = true) {
  busy.value = true
  try {
    const data = await getStatus()
    status.value = data
    if (updateForm || !form.value) {
      form.value = { ...data.config, models: [...data.config.models] }
      modelsText.value = data.config.models.join(', ')
    }
    if (detail.value) detail.value = data.rows.find(r => rowKey(r) === rowKey(detail.value!)) ?? null
    error.value = ''
  } catch (cause) {
    report(cause)
  } finally {
    busy.value = false
  }
}

async function saveConfig() {
  if (!form.value) return
  notice.value = ''
  busy.value = true
  try {
    const models = modelsText.value.split(/[,，\s]+/).map(m => m.trim()).filter(Boolean)
    const saved = await updateConfig({ ...form.value, models })
    form.value = { ...saved, models: [...saved.models] }
    modelsText.value = saved.models.join(', ')
    notice.value = '体检设置已保存'
    error.value = ''
  } catch (cause) {
    report(cause)
  } finally {
    busy.value = false
  }
  await refresh(false)
}

let pollUntil = 0
async function runAllNow() {
  notice.value = ''
  try {
    const res = await runAll()
    notice.value = res.started ? '已开始体检，结果会陆续刷新（不通过的目标会在复测后更新）' : '已有一轮体检在进行中'
    runningLocal.value = true
    pollUntil = Date.now() + 3 * 60 * 1000
    error.value = ''
  } catch (cause) {
    report(cause)
  }
}

async function checkOne(row: ChannelFidelityRow) {
  checking.value = rowKey(row)
  notice.value = ''
  try {
    const res = await runOne(row.account_id, row.model)
    notice.value = `${row.account_name} · ${row.model}：${statusLabel(res.status)}${res.message ? '（' + res.message + '）' : ''}`
    error.value = ''
  } catch (cause) {
    report(cause)
  } finally {
    checking.value = ''
  }
  await refresh(false)
}

async function resetRowBaseline(row: ChannelFidelityRow) {
  if (!window.confirm(`重置 ${row.account_name} · ${row.upstream_model} 的 prompt_tokens 基线？下次全部通过时会重新记录。`)) return
  try {
    await resetBaseline(row.account_id, row.upstream_model)
    notice.value = '基线已重置'
    error.value = ''
  } catch (cause) {
    report(cause)
  }
  await refresh(false)
}

let timer: ReturnType<typeof setInterval> | undefined
let nextRefresh = 0
onMounted(async () => {
  await refresh()
  nextRefresh = Date.now() + 30000
  timer = setInterval(() => {
    if (document.hidden || busy.value || checking.value) return
    const now = Date.now()
    const polling = now < pollUntil
    if (!polling) runningLocal.value = false
    // 手动全部体检后 3 分钟内每 5 秒刷新一次，平时 30 秒一次。
    if (now >= nextRefresh || (polling && now >= nextRefresh - 25000)) {
      nextRefresh = now + 30000
      void refresh(false)
    }
  }, 1000)
})
onBeforeUnmount(() => clearInterval(timer))
</script>

<style scoped>
.fidelity-label {
  @apply text-xs font-medium text-gray-600 dark:text-gray-300;
}
</style>
