<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Activity, FileBadge, RefreshCw, Users } from '@lucide/vue'
import { NAlert, NButton } from 'naive-ui'
import { api, ApiError, type Operation, type Overview, type SystemInfo } from '../api'
import { errorMessage, formatBytes, formatDate, formatRate, operationKindLabel } from '../labels'

const router = useRouter()
const system = ref<SystemInfo | null>(null)
const overview = ref<Overview | null>(null)
const operations = ref<Operation[]>([])
const loading = ref(false)
const error = ref('')
const updatedAt = ref('')

const stats = computed(() => {
  const info = overview.value
  if (!info) {
    return []
  }
  return [
    { label: '在线连接', value: String(info.onlineConnections), hint: info.agentPresent ? '实时统计' : '实时数据暂不可用', icon: Activity },
    {
      label: '实时速率',
      value: `↑ ${formatRate(info.upRate)}`,
      hint: `↓ ${formatRate(info.downRate)}`,
      icon: Activity,
    },
    { label: '今日流量', value: formatBytes(info.todayUpBytes + info.todayDownBytes), hint: `上行 ${formatBytes(info.todayUpBytes)} · 下行 ${formatBytes(info.todayDownBytes)}`, icon: FileBadge },
    { label: '用户 / 配置', value: `${info.users} / ${info.profiles}`, hint: '按用户和客户端配置统计', icon: Users },
  ]
})

const trendMax = computed(() => {
  const points = overview.value?.trend ?? []
  return Math.max(1, ...points.map((point) => point.upBytes + point.downBytes))
})

const trendBars = computed(() => {
  const points = overview.value?.trend ?? []
  return points.map((point) => {
    const total = point.upBytes + point.downBytes
    return {
      ...point,
      total,
      height: Math.max(2, Math.round((total / trendMax.value) * 100)),
      label: point.day.slice(5),
    }
  })
})

const liveConnections = computed(() => (overview.value?.connections ?? []).slice(0, 6))

const facts = computed(() => {
  const info = system.value
  if (!info) {
    return []
  }
  return [
    { label: '版本', value: info.version === 'dev' ? '开发版' : info.version },
    { label: '构建时间', value: info.buildTime === 'unknown' ? '未知' : formatDate(info.buildTime) },
    { label: '数据存储', value: info.dataStore === 'sqlite' ? '本地数据库' : info.dataStore },
    { label: 'HTTPS', value: info.tlsEnabled ? '已启用' : '未启用' },
  ]
})

async function load(silent = false) {
  if (loading.value && !silent) {
    return
  }
  if (!silent) {
    loading.value = true
  }
  error.value = ''
  try {
    const [info, live, ops] = await Promise.all([api.system(), api.overview(), api.operations()])
    system.value = info
    overview.value = live
    operations.value = ops.operations ?? []
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

function stateLabel(state: Operation['state']) {
  switch (state) {
    case 'pending':
      return '等待中'
    case 'running':
      return '进行中'
    case 'succeeded':
      return '已完成'
    case 'failed':
      return '失败'
    default:
      return state
  }
}

function formatUptime(seconds: number) {
  const total = Math.max(0, Math.floor(seconds))
  const days = Math.floor(total / 86400)
  const hours = Math.floor((total % 86400) / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  if (days > 0) return `${days} 天 ${hours} 小时`
  if (hours > 0) return `${hours} 小时 ${minutes} 分`
  if (minutes > 0) return `${minutes} 分`
  return `${total} 秒`
}

let timer: number | undefined
onMounted(() => {
  load()
  timer = window.setInterval(() => load(true), 5000)
})
onBeforeUnmount(() => {
  if (timer !== undefined) {
    window.clearInterval(timer)
  }
})
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <span class="eyebrow"><Activity :size="14" /> 概览</span>
        <div class="title-row">
          <h1>概览</h1>
          <span v-if="overview" class="chip" :class="overview.agentPresent ? 'chip--ok' : 'chip--warn'">
            <i class="dot" />{{ overview.agentPresent ? '服务运行中' : '实时数据暂不可用' }}
          </span>
        </div>
      </div>
      <div class="header-tools">
        <span v-if="updatedAt" class="muted">更新于 {{ updatedAt }}</span>
        <NButton quaternary circle :loading="loading" aria-label="刷新" title="刷新" @click="load()">
          <template #icon><RefreshCw :size="17" /></template>
        </NButton>
        <NButton type="primary" @click="router.push('/users?new=1')">添加用户</NButton>
      </div>
    </header>

    <NAlert v-if="error" type="error" :bordered="false" class="stack-gap">
      {{ error }}
      <NButton text type="primary" @click="load()">重试</NButton>
    </NAlert>

    <div v-if="system" class="stat-grid">
      <article v-for="stat in stats" :key="stat.label" class="stat-card">
        <div class="stat-top">
          <span>{{ stat.label }}</span>
          <component :is="stat.icon" :size="17" aria-hidden="true" />
        </div>
        <strong class="stat-value">{{ stat.value }}</strong>
        <span class="stat-hint">{{ stat.hint }}</span>
      </article>
    </div>

    <div class="panel-grid">
      <section class="surface trend-surface">
        <header class="surface-head">
          <h2>近 7 天流量</h2>
          <span class="surface-meta">上传 + 下载</span>
        </header>
        <div v-if="trendBars.length" class="trend" role="img" aria-label="最近七天流量柱状图">
          <div v-for="bar in trendBars" :key="bar.day" class="trend-col">
            <div class="trend-track">
              <i :style="{ height: `${bar.height}%` }" :title="`${bar.day} ${formatBytes(bar.total)}`" />
            </div>
            <span class="trend-label">{{ bar.label }}</span>
            <span class="trend-value">{{ formatBytes(bar.total) }}</span>
          </div>
        </div>
        <div v-else class="surface-empty">暂无流量记录</div>
      </section>

      <section class="surface">
        <header class="surface-head">
          <h2>在线连接</h2>
          <RouterLink to="/connections" class="text-link">全部连接</RouterLink>
        </header>
        <ul v-if="liveConnections.length" class="live-list">
          <li v-for="item in liveConnections" :key="item.id">
            <div class="live-main">
              <RouterLink :to="`/profiles/${item.profileId}`" class="live-name" :title="item.profileName">{{ item.profileName }}</RouterLink>
              <span class="live-meta">{{ item.userDisplayName || '未知用户' }} · {{ item.vpnIp || '等待地址' }}</span>
            </div>
            <div class="live-side">
              <span>↑ {{ formatRate(item.upRate) }}</span>
              <span>↓ {{ formatRate(item.downRate) }}</span>
            </div>
          </li>
        </ul>
        <div v-else class="surface-empty">
          <strong>当前没有在线连接</strong>
          <span>打开“在线连接”查看连接详情和历史。</span>
        </div>
      </section>
    </div>

    <div class="panel-grid">
      <section class="surface">
        <header class="surface-head">
          <h2>系统信息</h2>
          <span class="surface-meta">{{ system ? formatUptime(system.uptimeSeconds) : '读取中' }}</span>
        </header>
        <dl v-if="system" class="fact-list">
          <div v-for="fact in facts" :key="fact.label">
            <dt>{{ fact.label }}</dt>
            <dd>{{ fact.value }}</dd>
          </div>
        </dl>
        <div v-else class="surface-empty">正在读取…</div>
      </section>

      <section class="surface">
        <header class="surface-head">
          <h2>最近操作</h2>
          <span class="surface-meta">{{ operations.length }} 条</span>
        </header>
        <ul v-if="operations.length" class="operation-list">
          <li v-for="operation in operations" :key="operation.id">
            <div class="operation-main">
              <strong>{{ operationKindLabel(operation.kind) }}</strong>
              <span class="operation-message" :title="operation.message">{{ operation.message || '—' }}</span>
            </div>
            <div class="operation-side">
              <span class="chip" :class="`chip--${operation.state}`">{{ stateLabel(operation.state) }}</span>
              <time>{{ formatDate(operation.startedAt) }}</time>
            </div>
          </li>
        </ul>
        <div v-else class="surface-empty">
          <strong>暂无操作记录</strong>
          <span>备份、恢复和 OpenVPN 更新会显示在这里。</span>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.page {
  width: min(1120px, 100%);
  margin: 0 auto;
}

.page-header {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 22px;
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

.title-row .chip {
  margin-top: 4px;
}

.header-tools {
  display: flex;
  align-items: center;
  gap: 8px;
}

.stat-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 14px;
  margin-bottom: 18px;
}

.stat-card {
  display: flex;
  flex-direction: column;
  min-height: 132px;
  padding: 18px;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: var(--surface);
  box-shadow: var(--shadow-soft);
}

.stat-top {
  display: flex;
  align-items: center;
  justify-content: space-between;
  color: var(--text-3);
  font-size: 12px;
}

.stat-value {
  margin-top: auto;
  padding-top: 16px;
  color: var(--text-1);
  font-size: clamp(24px, 2.6vw, 30px);
  font-weight: 660;
  font-variant-numeric: tabular-nums;
  letter-spacing: -0.02em;
  overflow-wrap: anywhere;
}

.stat-hint {
  margin-top: 5px;
  color: var(--text-3);
  font-size: 12px;
}

.panel-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.05fr) minmax(0, 0.95fr);
  gap: 14px;
}

.surface {
  min-height: 280px;
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
  margin-bottom: 16px;
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

.text-link {
  display: inline-flex;
  align-items: center;
  min-height: 28px;
  color: var(--accent);
  font-size: 12px;
  text-decoration: none;
}
.text-link:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}

.text-link:hover {
  text-decoration: underline;
}

.trend-surface {
  display: flex;
  flex-direction: column;
}

.trend {
  display: grid;
  grid-template-columns: repeat(7, minmax(0, 1fr));
  gap: 10px;
  align-items: stretch;
  flex: 1;
  min-height: 210px;
  padding-top: 8px;
}

.trend-col {
  display: flex;
  flex-direction: column;
  justify-content: flex-end;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

.trend-track {
  display: flex;
  flex: 1;
  align-items: flex-end;
  width: 100%;
  min-height: 140px;
  border-radius: 6px;
  background: linear-gradient(180deg, transparent, color-mix(in srgb, var(--accent) 7%, transparent));
}

.trend-track i {
  display: block;
  width: 100%;
  min-height: 2px;
  border-radius: 6px 6px 2px 2px;
  background: linear-gradient(180deg, var(--accent), color-mix(in srgb, var(--accent) 55%, transparent));
  transition: height 260ms ease;
}

.trend-label {
  color: var(--text-3);
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
}

.trend-value {
  color: var(--text-2);
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.live-list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.live-list li {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
  padding: 11px 0;
  border-top: 1px solid var(--border);
}

.live-list li:first-child {
  border-top: 0;
  padding-top: 0;
}

.live-main {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

.live-name {
  display: inline-flex;
  align-items: center;
  min-height: 24px;
  overflow: hidden;
  color: var(--text-1);
  font-size: 13px;
  font-weight: 600;
  text-decoration: none;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.live-name:hover {
  color: var(--accent);
}

.live-meta {
  overflow: hidden;
  color: var(--text-3);
  font-size: 11.5px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.live-side {
  display: flex;
  flex: none;
  flex-direction: column;
  align-items: flex-end;
  gap: 3px;
  color: var(--text-2);
  font-size: 11.5px;
  font-variant-numeric: tabular-nums;
}

.fact-list {
  margin: 0;
}

.fact-list > div {
  display: grid;
  grid-template-columns: 104px minmax(0, 1fr);
  gap: 12px;
  padding: 10px 0;
  border-top: 1px solid var(--border);
}

.fact-list > div:first-child {
  border-top: 0;
  padding-top: 0;
}

.fact-list dt {
  color: var(--text-3);
  font-size: 12.5px;
}

.fact-list dd {
  margin: 0;
  color: var(--text-1);
  font-size: 13px;
  overflow-wrap: anywhere;
}

.fact-list dd.is-code {
  font-family: var(--font-code);
  font-size: 12.5px;
}

.operation-list {
  margin: 0;
  padding: 0;
  list-style: none;
}

.operation-list li {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 14px;
  padding: 12px 0;
  border-top: 1px solid var(--border);
}

.operation-list li:first-child {
  border-top: 0;
  padding-top: 0;
}

.operation-main {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.operation-main strong {
  font-family: var(--font-code);
  font-size: 12.5px;
  font-weight: 600;
}

.operation-message {
  overflow: hidden;
  color: var(--text-3);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.operation-side {
  display: flex;
  flex: none;
  flex-direction: column;
  align-items: flex-end;
  gap: 6px;
}

.operation-side time {
  color: var(--text-3);
  font-size: 11.5px;
  font-variant-numeric: tabular-nums;
}

.surface-empty {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  justify-content: center;
  gap: 6px;
  min-height: 180px;
  color: var(--text-3);
  font-size: 12.5px;
}

.surface-empty strong {
  color: var(--text-2);
  font-size: 14px;
  font-weight: 600;
}

.chip--pending,
.chip--running {
  background: var(--warn-soft);
  color: var(--warn);
}

.chip--succeeded {
  background: var(--ok-soft);
  color: var(--ok);
}

.chip--failed {
  background: var(--danger-soft);
  color: var(--danger);
}

@media (max-width: 900px) {
  .stat-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }

  .panel-grid {
    grid-template-columns: minmax(0, 1fr);
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

  .stat-card {
    min-height: 118px;
    padding: 15px;
  }

  .surface {
    padding: 16px;
  }

  .fact-list > div {
    grid-template-columns: 88px minmax(0, 1fr);
  }
}
</style>
