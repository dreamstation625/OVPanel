<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Activity, Clock3, Database, LogOut, RefreshCw, Server, ShieldCheck } from '@lucide/vue'
import { NAlert, NButton } from 'naive-ui'
import BrandMark from '../components/BrandMark.vue'
import ThemeToggle from '../components/ThemeToggle.vue'
import { api, ApiError, type Operation, type SystemInfo } from '../api'

defineProps<{
  username: string
}>()

const emit = defineEmits<{
  logout: []
  expired: []
}>()

const system = ref<SystemInfo | null>(null)
const operations = ref<Operation[]>([])
const loading = ref(false)
const error = ref('')
const updatedAt = ref('')

const stats = computed(() => {
  const info = system.value
  if (!info) {
    return []
  }
  return [
    {
      label: '版本',
      value: info.version,
      hint: info.commit === 'none' ? '未注入提交信息' : shortCommit(info.commit),
      icon: Server,
    },
    {
      label: '管理会话',
      value: String(info.sessions),
      hint: '当前有效',
      icon: ShieldCheck,
    },
    {
      label: '数据库',
      value: `v${info.schemaVersion}`,
      hint: 'SQLite · 已迁移',
      icon: Database,
    },
    {
      label: '运行时间',
      value: formatUptime(info.uptimeSeconds),
      hint: '自本次启动',
      icon: Clock3,
    },
  ]
})

const facts = computed(() => {
  const info = system.value
  if (!info) {
    return []
  }
  return [
    { label: '版本', value: info.version },
    { label: '提交', value: shortCommit(info.commit), code: true },
    { label: '构建时间', value: formatTime(info.buildTime) },
    { label: 'Go 版本', value: info.goVersion },
    { label: '数据库', value: `SQLite · schema v${info.schemaVersion}` },
    { label: '直连 HTTPS', value: info.tlsEnabled ? '已启用' : '未启用' },
  ]
})

async function load() {
  if (loading.value) {
    return
  }
  loading.value = true
  error.value = ''
  try {
    const [systemInfo, operationList] = await Promise.all([api.system(), api.operations()])
    system.value = systemInfo
    operations.value = operationList.operations ?? []
    updatedAt.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      emit('expired')
      return
    }
    error.value = err instanceof Error ? err.message : '读取系统状态失败'
  } finally {
    loading.value = false
  }
}

async function logout() {
  try {
    await api.logout()
  } catch {
    // 会话已失效时本地同样退出；服务端清理失败不影响下一次登录。
  }
  emit('logout')
}

function shortCommit(value: string) {
  if (!value || value === 'none') {
    return value || '—'
  }
  return value.slice(0, 7)
}

function formatTime(value: string) {
  if (!value || value === 'unknown') {
    return value || '—'
  }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) {
    return value
  }
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'medium',
    timeStyle: 'medium',
    hour12: false,
  }).format(date)
}

function formatUptime(seconds: number) {
  const total = Math.max(0, Math.floor(seconds))
  const days = Math.floor(total / 86400)
  const hours = Math.floor((total % 86400) / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  if (days > 0) {
    return `${days} 天 ${hours} 小时`
  }
  if (hours > 0) {
    return `${hours} 小时 ${minutes} 分`
  }
  if (minutes > 0) {
    return `${minutes} 分`
  }
  return `${total} 秒`
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

onMounted(load)
</script>

<template>
  <div class="app-shell">
    <header class="app-header">
      <a class="app-brand" href="/" aria-label="OVPanel 概览">
        <BrandMark :size="34" />
        <span>
          <strong>OVPanel</strong>
          <small>OpenVPN 管理面板</small>
        </span>
      </a>

      <div class="header-actions">
        <span v-if="updatedAt" class="update-time">更新于 {{ updatedAt }}</span>
        <ThemeToggle />
        <NButton quaternary circle :loading="loading" aria-label="刷新状态" title="刷新状态" @click="load">
          <template #icon><RefreshCw :size="17" /></template>
        </NButton>
        <span class="user-chip" :title="username">{{ username }}</span>
        <NButton quaternary circle aria-label="退出登录" title="退出登录" @click="logout">
          <template #icon><LogOut :size="17" /></template>
        </NButton>
      </div>
    </header>

    <main class="app-main">
      <div class="page-heading">
        <div>
          <span class="eyebrow"><Activity :size="14" /> 系统状态</span>
          <div class="title-row">
            <h1>运行概览</h1>
            <span v-if="system" class="chip chip--ok"><i class="dot" />面板在线</span>
          </div>
        </div>
      </div>

      <NAlert v-if="error" type="error" :bordered="false" class="stack-gap">
        {{ error }}
        <NButton text type="primary" @click="load">重试</NButton>
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
        <section class="surface">
          <header class="surface-head">
            <h2>系统信息</h2>
          </header>
          <dl v-if="system" class="fact-list">
            <div v-for="fact in facts" :key="fact.label">
              <dt>{{ fact.label }}</dt>
              <dd :class="{ 'is-code': fact.code }">{{ fact.value }}</dd>
            </div>
          </dl>
          <div v-else class="surface-empty">正在读取…</div>
        </section>

        <section class="surface">
          <header class="surface-head">
            <h2>最近任务</h2>
            <span class="surface-meta">{{ operations.length }} 条</span>
          </header>
          <ul v-if="operations.length" class="operation-list">
            <li v-for="operation in operations" :key="operation.id">
              <div class="operation-main">
                <strong>{{ operation.kind }}</strong>
                <span class="operation-message">{{ operation.message || '—' }}</span>
              </div>
              <div class="operation-side">
                <span class="chip" :class="`chip--${operation.state}`">{{ stateLabel(operation.state) }}</span>
                <time>{{ formatTime(operation.startedAt) }}</time>
              </div>
            </li>
          </ul>
          <div v-else class="surface-empty">
            <strong>暂无任务记录</strong>
            <span>备份、升级和恢复任务会记录在这里。</span>
          </div>
        </section>
      </div>
    </main>
  </div>
</template>

<style scoped>
.app-shell {
  min-height: 100vh;
}

.app-header {
  position: sticky;
  top: 0;
  z-index: 10;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 14px clamp(16px, 4vw, 40px);
  border-bottom: 1px solid var(--border);
  background: color-mix(in srgb, var(--bg) 88%, transparent);
  backdrop-filter: blur(12px);
}

.app-brand {
  display: flex;
  align-items: center;
  gap: 11px;
  color: inherit;
  text-decoration: none;
}

.app-brand span {
  display: flex;
  flex-direction: column;
  line-height: 1.15;
}

.app-brand strong {
  font-size: 16px;
  letter-spacing: -0.01em;
}

.app-brand small {
  margin-top: 3px;
  color: var(--text-3);
  font-size: 11.5px;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.update-time {
  color: var(--text-3);
  font-size: 12px;
  font-variant-numeric: tabular-nums;
}

.user-chip {
  max-width: 140px;
  overflow: hidden;
  padding: 5px 10px;
  border: 1px solid var(--border);
  border-radius: 999px;
  color: var(--text-2);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.app-main {
  width: min(1120px, 100%);
  margin: 0 auto;
  padding: clamp(24px, 4vw, 44px) clamp(16px, 4vw, 40px) 64px;
}

.page-heading {
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

.page-heading h1 {
  margin: 0;
  font-size: clamp(24px, 3vw, 30px);
  font-weight: 680;
  letter-spacing: -0.02em;
}

.title-row .chip {
  margin-top: 4px;
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
  color: var(--text-3);
  font-size: 12px;
  overflow: hidden;
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
    grid-template-columns: 1fr;
  }

  .update-time {
    display: none;
  }
}

@media (max-width: 640px) {
  .app-header {
    flex-wrap: wrap;
    gap: 10px;
  }

  .header-actions {
    width: 100%;
    justify-content: flex-end;
  }

  .user-chip {
    margin-left: auto;
  }

  .page-heading {
    align-items: flex-start;
    flex-direction: column;
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
