<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { History, RefreshCw, ScrollText } from '@lucide/vue'
import { NAlert, NButton, NInput, NSelect } from 'naive-ui'
import {
  api,
  ApiError,
  type ConnectionHistoryItem,
  type Operation,
} from '../api'
import { errorMessage, formatBytes, formatDate, formatDuration, operationKindLabel } from '../labels'

const tab = ref<'connections' | 'tasks'>('connections')
const history = ref<ConnectionHistoryItem[]>([])
const operations = ref<Operation[]>([])
const loading = ref(false)
const error = ref('')
const updatedAt = ref('')
const query = ref('')
const reason = ref('')
const reasonOptions = [
  { label: '全部原因', value: '' },
  { label: '客户端断开', value: 'client_disconnect' },
  { label: '额度用尽', value: 'quota_exhausted' },
  { label: '权限变更', value: 'policy_revoked' },
  { label: '证书更换', value: 'certificate_changed' },
  { label: '管理员断开', value: 'admin_disconnect' },
  { label: '配置到期', value: 'profile_expired' },
]

const filteredHistory = computed(() => {
  const needle = query.value.trim().toLowerCase()
  return history.value.filter((item) => {
    if (reason.value && item.endReason !== reason.value) return false
    if (!needle) return true
    return [item.profileName, item.userDisplayName, item.vpnIp, item.sourceIp, item.endReason]
      .join(' ')
      .toLowerCase()
      .includes(needle)
  })
})

async function load() {
  if (loading.value) return
  loading.value = true
  error.value = ''
  try {
    const [sessions, ops] = await Promise.all([
      api.connectionHistory('', 200),
      api.operations(),
    ])
    history.value = sessions.sessions ?? []
    updatedAt.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
    operations.value = ops.operations ?? []
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

function endReasonLabel(value: string) {
  switch (value) {
    case 'client_disconnect':
      return '客户端断开'
    case 'quota_exhausted':
      return '额度用尽'
    case 'policy_revoked':
      return '权限变更'
    case 'certificate_changed':
      return '证书更换'
    case 'certificate_expired':
      return '证书过期'
    case 'profile_expired':
      return '配置到期'
    case 'admin_disconnect':
      return '管理员断开'
    case 'address_reused':
      return '地址被重新分配'
    case 'runtime_stop':
      return 'VPN 服务停止'
    default:
      return value || '未记录'
  }
}

function stateLabel(state: Operation['state']) {
  switch (state) {
    case 'pending': return '等待中'
    case 'running': return '进行中'
    case 'succeeded': return '已完成'
    case 'failed': return '失败'
    default: return state
  }
}

onMounted(load)
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <span class="eyebrow"><ScrollText :size="14" /> 日志</span>
        <h1>日志</h1>
        <p class="page-meta">连接和操作记录。</p>
      </div>
      <div class="header-tools">
        <span v-if="updatedAt" class="muted">更新于 {{ updatedAt }}</span>
        <NButton quaternary circle :loading="loading" aria-label="刷新" title="刷新" @click="load">
          <template #icon><RefreshCw :size="17" /></template>
        </NButton>
      </div>
    </header>

    <NAlert v-if="error" type="error" :bordered="false" class="stack-gap">
      {{ error }}
      <NButton text type="primary" @click="load">重试</NButton>
    </NAlert>

    <div class="tabs" role="tablist" aria-label="日志类型">
      <button class="tab" role="tab" :aria-selected="tab === 'connections'" :class="{ active: tab === 'connections' }" @click="tab = 'connections'">
        <History :size="15" /> 连接历史
      </button>
      <button class="tab" role="tab" :aria-selected="tab === 'tasks'" :class="{ active: tab === 'tasks' }" @click="tab = 'tasks'">
        <ScrollText :size="15" /> 操作记录
      </button>
    </div>

    <template v-if="tab === 'connections'">
      <div class="toolbar">
        <NInput v-model:value="query" clearable placeholder="搜索用户、客户端、IP 或原因" />
        <NSelect v-model:value="reason" :options="reasonOptions" class="reason-filter" />
      </div>
      <section class="surface">
        <header class="surface-head">
          <h2>连接历史</h2>
          <span class="surface-meta">{{ filteredHistory.length }} / {{ history.length }} 条</span>
        </header>
        <div v-if="filteredHistory.length" class="log-list">
          <div class="log-head" aria-hidden="true">
            <span>用户 / 客户端</span>
            <span>VPN 地址</span>
            <span>连接时间</span>
            <span class="align-end">流量</span>
            <span class="align-end">结束原因</span>
          </div>
          <article v-for="item in filteredHistory" :key="item.id" class="log-row">
            <div class="log-main">
              <RouterLink :to="`/profiles/${item.profileId}`" class="log-title">{{ item.profileName || '未知配置' }}</RouterLink>
              <span class="log-meta">{{ item.userDisplayName || '—' }}</span>
            </div>
            <div class="log-cell" data-label="VPN 地址">
              <span class="mono">{{ item.vpnIp || '—' }}</span>
              <span class="muted">来源 {{ item.sourceIp || '—' }}</span>
            </div>
            <div class="log-cell" data-label="连接时间">
              <span>{{ formatDate(item.connectedAt) }}</span>
              <span class="muted">{{ formatDuration(item.durationSeconds) }}</span>
            </div>
            <div class="log-cell align-end" data-label="流量">
              <span>↑ {{ formatBytes(item.upBytes) }}</span>
              <span class="muted">↓ {{ formatBytes(item.downBytes) }}</span>
            </div>
            <div class="log-cell align-end" data-label="结束原因">
              <span class="reason" :class="{ danger: item.endReason === 'quota_exhausted' || item.endReason === 'policy_revoked' }">
                {{ endReasonLabel(item.endReason) }}
              </span>
              <span class="muted">{{ item.disconnectedAt ? formatDate(item.disconnectedAt) : '未结束' }}</span>
            </div>
          </article>
        </div>
        <div v-else class="surface-empty">
          <strong>没有匹配记录</strong>
          <span>清除筛选条件后重试。</span>
        </div>
      </section>
    </template>

    <section v-else class="surface">
      <header class="surface-head">
        <h2>操作记录</h2>
        <span class="surface-meta">{{ operations.length }} 条</span>
      </header>
      <ul v-if="operations.length" class="task-list">
        <li v-for="operation in operations" :key="operation.id">
          <div class="task-main">
            <strong>{{ operationKindLabel(operation.kind) }}</strong>
            <span>{{ operation.message || '—' }}</span>
          </div>
          <div class="task-side">
            <span class="chip" :class="`chip--${operation.state === 'succeeded' ? 'ok' : operation.state === 'failed' ? 'danger' : 'warn'}`">
              {{ stateLabel(operation.state) }}
            </span>
            <time>{{ formatDate(operation.startedAt) }}</time>
          </div>
        </li>
      </ul>
      <div v-else class="surface-empty">
        <strong>暂无操作记录</strong>
        <span>备份、恢复等操作记录。</span>
      </div>
    </section>
  </div>
</template>

<style scoped>
.page { width: min(1180px, 100%); margin: 0 auto; }
.page-header { display: flex; align-items: flex-end; justify-content: space-between; gap: 16px; margin-bottom: 18px; }
.page-header h1 { margin: 6px 0 0; font-size: clamp(24px, 3vw, 30px); font-weight: 680; letter-spacing: -0.02em; }
.page-meta { margin: 5px 0 0; color: var(--text-3); font-size: 12px; }
.header-tools { display: flex; align-items: center; gap: 8px; }
.tabs { display: flex; gap: 4px; width: fit-content; margin-bottom: 14px; padding: 4px; border: 1px solid var(--border); border-radius: 10px; background: var(--surface-2); }
.tab { display: inline-flex; align-items: center; gap: 7px; min-height: 38px; padding: 0 14px; border: 0; border-radius: 7px; background: transparent; color: var(--text-2); font: inherit; font-size: 13px; cursor: pointer; }
.tab:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.tab.active { background: var(--surface); color: var(--text-1); box-shadow: var(--shadow-soft); }
.toolbar { display: flex; gap: 10px; margin-bottom: 14px; }
.toolbar :deep(.n-input) { max-width: 380px; }
.reason-filter { width: 170px; }
.surface { padding: 20px; border: 1px solid var(--border); border-radius: 12px; background: var(--surface); box-shadow: var(--shadow-soft); }
.surface-head { display: flex; align-items: baseline; justify-content: space-between; gap: 12px; margin-bottom: 14px; }
.surface-head h2 { margin: 0; font-size: 16px; font-weight: 640; }
.surface-meta { color: var(--text-3); font-size: 11.5px; }
.log-list { display: flex; flex-direction: column; }
.log-head { display: grid; grid-template-columns: minmax(0, 1.4fr) minmax(0, 1.2fr) minmax(0, 1fr) minmax(0, 0.8fr) minmax(0, 1fr); gap: 14px; padding: 0 0 10px; border-bottom: 1px solid var(--border); color: var(--text-3); font-size: 11.5px; }
.log-row { display: grid; grid-template-columns: minmax(0, 1.4fr) minmax(0, 1.2fr) minmax(0, 1fr) minmax(0, 0.8fr) minmax(0, 1fr); gap: 14px; align-items: center; padding: 12px 0; border-top: 1px solid var(--border); font-size: 12.5px; }
.log-row:first-child { border-top: 0; padding-top: 0; }
.log-main, .log-cell { display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.log-main span, .log-cell span { overflow-wrap: anywhere; }
.log-title { display: inline-flex; align-items: center; min-height: 24px; color: var(--text-1); font-weight: 620; text-decoration: none; }
.log-title:hover { color: var(--accent); }
.log-meta, .muted { color: var(--text-3); font-size: 11.5px; }
.mono { font-family: var(--font-code); font-variant-numeric: tabular-nums; }
.align-end { align-items: flex-end; text-align: right; }
.reason { color: var(--text-2); }
.reason.danger { color: var(--danger); }
.task-list { margin: 0; padding: 0; list-style: none; }
.task-list li { display: flex; align-items: flex-start; justify-content: space-between; gap: 14px; padding: 12px 0; border-top: 1px solid var(--border); }
.task-list li:first-child { border-top: 0; padding-top: 0; }
.task-main { display: flex; flex-direction: column; gap: 4px; min-width: 0; }
.task-main strong { font-family: var(--font-code); font-size: 12.5px; font-weight: 600; }
.task-main span { color: var(--text-3); font-size: 12px; overflow-wrap: anywhere; }
.task-side { display: flex; flex: none; flex-direction: column; align-items: flex-end; gap: 5px; }
.task-side time { color: var(--text-3); font-size: 11.5px; }
.surface-empty { display: flex; flex-direction: column; gap: 6px; justify-content: center; min-height: 180px; color: var(--text-3); font-size: 12.5px; }
.surface-empty strong { color: var(--text-2); font-size: 14px; }
@media (max-width: 1100px) {
  .log-head { display: none; }
  .log-cell[data-label]::before {
    content: attr(data-label);
    color: var(--text-3);
    font-size: 10.5px;
  }
  .log-row { grid-template-columns: 1fr 1fr; }
  .log-main { grid-column: 1 / -1; }
  .align-end { align-items: flex-start; text-align: left; }
}
@media (max-width: 640px) {
  .page-header { align-items: flex-start; flex-direction: column; }
  .header-tools { width: 100%; justify-content: flex-end; }
  .toolbar { flex-direction: column; }
  .toolbar :deep(.n-input) { max-width: none; }
  .reason-filter { width: 100%; }
  .surface { padding: 16px; }
  .log-row { grid-template-columns: 1fr; }
}
</style>
