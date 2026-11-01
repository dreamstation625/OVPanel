<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { Activity, History, Radio, RefreshCw, Unplug } from '@lucide/vue'
import { NAlert, NButton, NPopconfirm } from 'naive-ui'
import {
  api,
  ApiError,
  type AuthType,
  type Connection,
  type ConnectionHistoryItem,
  type ConnectionsResponse,
} from '../api'
import {
  authLabel,
  errorMessage,
  formatBytes,
  formatDate,
  formatDuration,
  formatRate,
} from '../labels'
import { useNotify } from '../notify'

const notify = useNotify()
const tab = ref<'online' | 'history'>('online')
const online = ref<ConnectionsResponse | null>(null)
const history = ref<ConnectionHistoryItem[]>([])
const loading = ref(false)
const error = ref('')
const updatedAt = ref('')
let timer: number | undefined

const summary = computed(() => {
  const data = online.value
  if (!data) {
    return []
  }
  return [
    { label: '在线连接', value: String(data.online), hint: data.agentPresent ? '实时统计' : '实时数据暂不可用' },
    { label: '上行速率', value: formatRate(data.upRate), hint: '全部连接合计' },
    { label: '下行速率', value: formatRate(data.downRate), hint: '全部连接合计' },
  ]
})

async function loadOnline(silent = false) {
  if (loading.value && !silent) {
    return
  }
  if (!silent) {
    loading.value = true
  }
  error.value = ''
  try {
    online.value = await api.connections()
    updatedAt.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      return
    }
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function loadHistory() {
  loading.value = true
  error.value = ''
  try {
    const result = await api.connectionHistory('', 100)
    history.value = result.sessions ?? []
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      return
    }
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function refresh() {
  if (tab.value === 'online') {
    await loadOnline()
  } else {
    await loadHistory()
  }
}

async function disconnect(item: Connection) {
  try {
    await api.disconnectConnection(item.id)
    notify.success('连接已断开。')
    await loadOnline()
  } catch (err) {
    notify.error(errorMessage(err))
  }
}

function startTimer() {
  stopTimer()
  if (tab.value === 'online') {
    timer = window.setInterval(() => loadOnline(true), 3000)
  }
}

function stopTimer() {
  if (timer !== undefined) {
    window.clearInterval(timer)
    timer = undefined
  }
}

watch(tab, (value) => {
  if (value === 'online') {
    loadOnline()
  } else {
    loadHistory()
  }
  startTimer()
})

onMounted(() => {
  loadOnline()
  startTimer()
})

onBeforeUnmount(stopTimer)

function endReasonLabel(reason: string) {
  switch (reason) {
    case 'client_disconnect':
      return '客户端断开'
    case 'quota_exhausted':
      return '流量额度用尽'
    case 'policy_revoked':
      return '配置或权限已变更'
    case 'certificate_changed':
      return '证书已更换'
    case 'certificate_expired':
      return '证书已过期'
    case 'profile_expired':
      return '配置已到期'
    case 'admin_disconnect':
      return '管理员断开'
    case 'address_reused':
      return '地址被重新分配'
    case 'runtime_stop':
      return 'VPN 服务停止'
    case 'proxy_restart':
    case '代理重启或离线':
      return 'VPN 服务重启或离线'
    case '未收到结束事件':
      return '未正常结束'
    default:
      return reason || '—'
  }
}
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <span class="eyebrow"><Radio :size="14" /> 在线连接</span>
        <div class="title-row">
          <h1>连接管理</h1>
          <span v-if="online" class="chip" :class="online.agentPresent ? 'chip--ok' : 'chip--warn'">
            <i class="dot" />{{ online.agentPresent ? '实时数据正常' : '实时数据暂不可用' }}
          </span>
        </div>
      </div>
      <div class="header-tools">
        <span v-if="updatedAt" class="muted">更新于 {{ updatedAt }}</span>
        <NButton quaternary circle :loading="loading" aria-label="刷新" title="刷新" @click="refresh">
          <template #icon><RefreshCw :size="17" /></template>
        </NButton>
      </div>
    </header>

    <NAlert v-if="error" type="error" :bordered="false" closable class="stack-gap" @close="error = ''">
      {{ error }}
      <NButton text type="primary" @click="refresh">重试</NButton>
    </NAlert>

    <div class="tabs" role="tablist" aria-label="连接视图">
      <button
        class="tab"
        role="tab"
        :aria-selected="tab === 'online'"
        :class="{ active: tab === 'online' }"
        @click="tab = 'online'"
      >
        <Activity :size="15" /> 在线连接
      </button>
      <button
        class="tab"
        role="tab"
        :aria-selected="tab === 'history'"
        :class="{ active: tab === 'history' }"
        @click="tab = 'history'"
      >
        <History :size="15" /> 连接历史
      </button>
    </div>

    <template v-if="tab === 'online'">
      <div class="stat-grid">
        <article v-for="item in summary" :key="item.label" class="stat-card">
          <span class="stat-label">{{ item.label }}</span>
          <strong class="stat-value">{{ item.value }}</strong>
          <span class="stat-hint">{{ item.hint }}</span>
        </article>
      </div>

      <section class="surface">
        <header class="surface-head">
          <h2>在线连接</h2>
          <span class="surface-meta">{{ online?.connections.length ?? 0 }} 条</span>
        </header>

        <div v-if="online?.connections.length" class="table">
          <div class="table-head" aria-hidden="true">
            <span>用户 / 配置</span>
            <span>连接</span>
            <span>时长</span>
            <span class="align-end">上行</span>
            <span class="align-end">下行</span>
            <span></span>
          </div>
          <div v-for="item in online.connections" :key="item.id" class="table-row">
            <div class="cell cell-main">
              <RouterLink :to="`/profiles/${item.profileId}`" class="primary-link" :title="item.profileName">
                {{ item.profileName }}
              </RouterLink>
              <span>{{ item.userDisplayName || '未知用户' }} · {{ item.authType ? authLabel(item.authType as AuthType) : '—' }}</span>
            </div>
            <div class="cell">
              <span class="mono">{{ item.vpnIp || '等待地址' }}</span>
              <span class="muted">来源 {{ item.sourceIp || '—' }}</span>
            </div>
            <div class="cell">
              <span>{{ formatDuration(item.durationSeconds) }}</span>
              <span class="muted">{{ formatDate(item.connectedAt) }}</span>
            </div>
            <div class="cell align-end">
              <span>↑ {{ formatRate(item.upRate) }}</span>
              <span class="muted">累计 {{ formatBytes(item.upBytes) }}</span>
            </div>
            <div class="cell align-end">
              <span>↓ {{ formatRate(item.downRate) }}</span>
              <span class="muted">累计 {{ formatBytes(item.downBytes) }}</span>
            </div>
            <div class="cell cell-actions">
              <NPopconfirm @positive-click="disconnect(item)">
                <template #trigger>
                  <NButton size="small" type="error" ghost>
                    <template #icon><Unplug :size="14" /></template>
                    断开
                  </NButton>
                </template>
                仅断开这一条连接，该用户仍可重新连接。确认继续？
              </NPopconfirm>
            </div>
          </div>
        </div>
        <div v-else class="surface-empty">
          <strong>当前没有在线连接</strong>
          <span>客户端连接后会显示在这里。</span>
        </div>
      </section>
    </template>

    <section v-else class="surface">
      <header class="surface-head">
        <h2>最近连接</h2>
        <span class="surface-meta">最近 {{ history.length }} 条记录</span>
      </header>
      <div v-if="history.length" class="table table--history">
        <div class="table-head" aria-hidden="true">
          <span>用户 / 配置</span>
          <span>地址</span>
          <span>连接时间</span>
          <span class="align-end">上行</span>
          <span class="align-end">下行</span>
          <span>结束原因</span>
        </div>
        <div v-for="item in history" :key="item.id" class="table-row">
          <div class="cell cell-main">
            <RouterLink :to="`/profiles/${item.profileId}`" class="primary-link" :title="item.profileName">
              {{ item.profileName || '未知配置' }}
            </RouterLink>
            <span>{{ item.userDisplayName || '—' }}</span>
          </div>
          <div class="cell">
            <span class="mono">{{ item.vpnIp || '—' }}</span>
            <span class="muted">来源 {{ item.sourceIp || '—' }}</span>
          </div>
          <div class="cell">
            <span>{{ formatDate(item.connectedAt) }}</span>
            <span class="muted">{{ formatDuration(item.durationSeconds) }}</span>
          </div>
          <div class="cell align-end">
            <span>↑ {{ formatBytes(item.upBytes) }}</span>
          </div>
          <div class="cell align-end">
            <span>↓ {{ formatBytes(item.downBytes) }}</span>
          </div>
          <div class="cell cell-reason">
            <span>{{ endReasonLabel(item.endReason) }}</span>
            <span v-if="!item.disconnectedAt" class="muted">仍在线或没有正常结束</span>
            <span v-else class="muted">{{ formatDate(item.disconnectedAt) }}</span>
          </div>
        </div>
      </div>
      <div v-else class="surface-empty">
        <strong>还没有连接历史</strong>
        <span>连接开始、流量和结束记录会自动保存在这里。</span>
      </div>
    </section>
  </div>
</template>

<style scoped>
.page {
  width: min(1180px, 100%);
  margin: 0 auto;
}

.page-header {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 18px;
}

.title-row {
  display: flex;
  align-items: center;
  gap: 12px;
  margin-top: 6px;
}

.page-header h1 {
  margin: 0;
  font-size: clamp(24px, 3vw, 30px);
  font-weight: 680;
  letter-spacing: -0.02em;
}

.header-tools {
  display: flex;
  align-items: center;
  gap: 8px;
}

.tabs {
  display: flex;
  gap: 4px;
  margin-bottom: 14px;
  padding: 4px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--surface-2);
  width: fit-content;
}

.tab {
  display: inline-flex;
  align-items: center;
  gap: 7px;
  min-height: 34px;
  padding: 0 14px;
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: var(--text-2);
  font: inherit;
  font-size: 13px;
  cursor: pointer;
}

.tab.active {
  background: var(--surface);
  color: var(--text-1);
  box-shadow: var(--shadow-soft);
}

.stat-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 14px;
  margin-bottom: 14px;
}

.stat-card {
  display: flex;
  flex-direction: column;
  gap: 6px;
  padding: 18px;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: var(--surface);
  box-shadow: var(--shadow-soft);
}

.stat-label {
  color: var(--text-3);
  font-size: 12px;
}

.stat-value {
  color: var(--text-1);
  font-size: clamp(22px, 2.4vw, 28px);
  font-weight: 660;
  font-variant-numeric: tabular-nums;
  letter-spacing: -0.02em;
}

.stat-hint {
  color: var(--text-3);
  font-size: 11.5px;
}

.surface {
  padding: 20px;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: var(--surface);
  box-shadow: var(--shadow-soft);
}

.surface-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 14px;
}

.surface-head h2 {
  margin: 0;
  font-size: 16px;
  font-weight: 640;
}

.surface-meta {
  color: var(--text-3);
  font-size: 11.5px;
}

.table-head,
.table-row {
  display: grid;
  grid-template-columns: minmax(0, 1.35fr) minmax(0, 1.1fr) minmax(0, 1fr) minmax(0, 0.8fr) minmax(0, 0.8fr) auto;
  gap: 14px;
  align-items: center;
}

.table--history .table-head,
.table--history .table-row {
  grid-template-columns: minmax(0, 1.3fr) minmax(0, 1fr) minmax(0, 1.1fr) minmax(0, 0.75fr) minmax(0, 0.75fr) minmax(0, 1fr);
}

.table-head {
  padding: 0 4px 10px;
  border-bottom: 1px solid var(--border);
  color: var(--text-3);
  font-size: 11.5px;
}

.table-row {
  padding: 13px 4px;
  border-bottom: 1px solid var(--border);
  font-size: 12.5px;
}

.table-row:last-child {
  border-bottom: 0;
  padding-bottom: 2px;
}

.cell {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

.cell span {
  overflow-wrap: anywhere;
}

.cell-main .primary-link {
  display: inline-flex;
  align-items: center;
  min-height: 24px;
  color: var(--text-1);
  font-weight: 600;
  text-decoration: none;
}

.cell-main .primary-link:hover {
  color: var(--accent);
}

.mono {
  font-family: var(--font-code);
  font-variant-numeric: tabular-nums;
}

.muted {
  color: var(--text-3);
  font-size: 11.5px;
}

.align-end {
  align-items: flex-end;
  text-align: right;
}

.cell-actions {
  align-items: flex-end;
}

.surface-empty {
  display: flex;
  flex-direction: column;
  gap: 6px;
  align-items: flex-start;
  justify-content: center;
  min-height: 190px;
  color: var(--text-3);
  font-size: 12.5px;
}

.surface-empty strong {
  color: var(--text-2);
  font-size: 14px;
  font-weight: 600;
}

@media (max-width: 1100px) {
  .table-head {
    display: none;
  }

  .table-row,
  .table--history .table-row {
    grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
    gap: 10px 14px;
    padding: 14px 4px;
  }

  .cell-main,
  .cell-reason,
  .cell-actions {
    grid-column: 1 / -1;
  }

  .cell-actions {
    align-items: flex-start;
  }

  .stat-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (max-width: 640px) {
  .page-header {
    align-items: flex-start;
    flex-direction: column;
  }

  .header-tools {
    width: 100%;
    justify-content: flex-end;
  }

  .stat-grid {
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 8px;
  }

  .stat-card {
    min-height: 0;
    padding: 10px;
  }

  .stat-value {
    font-size: 17px;
    letter-spacing: -0.03em;
    white-space: nowrap;
  }

  .stat-hint {
    display: none;
  }

  .surface {
    padding: 16px;
  }

  .table-row,
  .table--history .table-row {
    grid-template-columns: minmax(0, 1fr);
  }

  .align-end {
    align-items: flex-start;
    text-align: left;
  }
}
</style>
