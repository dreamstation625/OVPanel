<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Plus, RefreshCw, Search, Users } from '@lucide/vue'
import { NAlert, NButton, NInput, NSelect } from 'naive-ui'
import CreateUserModal from '../components/CreateUserModal.vue'
import OneTimePasswordModal from '../components/OneTimePasswordModal.vue'
import { api, ApiError, type CreateUserResult, type User } from '../api'
import { errorMessage, formatBytes, formatDate } from '../labels'

const route = useRoute()
const router = useRouter()
const users = ref<User[]>([])
const loading = ref(false)
const error = ref('')
const query = ref('')
const status = ref('')
const createShow = ref(false)

const oneTime = ref({ show: false, password: '', username: '' })
const pendingUserId = ref('')

const statusOptions = [
  { label: '全部状态', value: '' },
  { label: '启用', value: 'active' },
  { label: '停用', value: 'disabled' },
]

async function load() {
  if (loading.value) {
    return
  }
  loading.value = true
  error.value = ''
  try {
    const result = await api.listUsers(query.value.trim(), status.value)
    users.value = result.users ?? []
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      return
    }
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

function openCreate() {
  createShow.value = true
}

function handleCreated(result: CreateUserResult) {
  load()
  if (result.password) {
    pendingUserId.value = result.user.id
    oneTime.value = { show: true, password: result.password, username: result.user.displayName }
    return
  }
  router.push(`/users/${result.user.id}`)
}

function closeOneTime() {
  oneTime.value.show = false
  if (pendingUserId.value) {
    const id = pendingUserId.value
    pendingUserId.value = ''
    router.push(`/users/${id}`)
  }
}

function userStatusLabel(value: User['status']) {
  return value === 'active' ? '启用' : '停用'
}

onMounted(() => {
  if (route.query.new === '1') {
    createShow.value = true
    router.replace({ path: '/users' })
  }
  load()
})
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <span class="eyebrow"><Users :size="14" /> 用户与客户端</span>
        <h1>用户</h1>
        <p class="page-meta">{{ users.length }} 个用户</p>
      </div>
      <div class="header-tools">
        <NButton quaternary circle :loading="loading" aria-label="刷新" title="刷新" @click="load">
          <template #icon><RefreshCw :size="17" /></template>
        </NButton>
        <NButton type="primary" @click="openCreate">
          <template #icon><Plus :size="16" /></template>
          添加用户
        </NButton>
      </div>
    </header>

    <NAlert v-if="error" type="error" :bordered="false" class="stack-gap">
      {{ error }}
      <NButton text type="primary" @click="load">重试</NButton>
    </NAlert>

    <div class="toolbar">
      <NInput
        v-model:value="query"
        clearable
        placeholder="搜索名称或备注"
        class="search"
        @keyup.enter="load"
      >
        <template #prefix><Search :size="15" /></template>
      </NInput>
      <NSelect v-model:value="status" :options="statusOptions" class="status-filter" @update:value="load" />
      <NButton @click="load">搜索</NButton>
    </div>

    <section class="list-surface">
      <div class="list-head">
        <span>用户</span>
        <span>状态</span>
        <span>配置</span>
        <span>在线</span>
        <span>累计流量</span>
        <span>创建时间</span>
        <span aria-hidden="true" />
      </div>

      <RouterLink
        v-for="user in users"
        :key="user.id"
        :to="`/users/${user.id}`"
        class="list-row"
      >
        <div class="user-cell">
          <strong>{{ user.displayName }}</strong>
          <span v-if="user.notes" class="user-note">{{ user.notes }}</span>
          <div v-if="user.tags.length" class="tag-row">
            <span v-for="tag in user.tags.slice(0, 3)" :key="tag" class="tag">{{ tag }}</span>
            <span v-if="user.tags.length > 3" class="tag">+{{ user.tags.length - 3 }}</span>
          </div>
        </div>
        <div>
          <span class="chip" :class="user.status === 'active' ? 'chip--ok' : 'chip--neutral'">
            {{ userStatusLabel(user.status) }}
          </span>
        </div>
        <div class="count-cell">
          <span class="count-label">配置 </span>
          <strong>{{ user.activeProfileCount }}</strong>
          <span> / {{ user.profileCount }}</span>
        </div>
        <div class="online-cell">
          <span class="count-label">在线 </span>
          <strong>{{ user.onlineSessions }}</strong>
        </div>
        <div class="usage-cell">
          <span class="count-label">流量 </span>
          <span>{{ formatBytes(user.usageBytes) }}</span>
        </div>
        <time class="muted">{{ formatDate(user.createdAt) }}</time>
        <span class="row-action">查看</span>
      </RouterLink>

      <div v-if="!users.length && !loading" class="empty-state">
        <strong>暂无用户</strong>
        <span>创建用户时可以直接生成第一份客户端配置。</span>
        <NButton type="primary" size="small" @click="openCreate">添加用户</NButton>
      </div>
      <div v-else-if="loading && !users.length" class="empty-state">正在读取…</div>
    </section>

    <CreateUserModal v-model:show="createShow" @created="handleCreated" />
    <OneTimePasswordModal
      :show="oneTime.show"
      :password="oneTime.password"
      :username="oneTime.username"
      title="用户与配置已创建"
      @update:show="!$event && closeOneTime()"
    />
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
  margin-bottom: 20px;
}

.page-header h1 {
  margin: 6px 0 0;
  font-size: clamp(24px, 3vw, 30px);
  font-weight: 680;
  letter-spacing: -0.02em;
}

.page-meta {
  margin: 5px 0 0;
  color: var(--text-3);
  font-size: 12px;
}

.header-tools {
  display: flex;
  align-items: center;
  gap: 8px;
}

.toolbar {
  display: flex;
  gap: 8px;
  margin-bottom: 14px;
}

.search {
  max-width: 320px;
}

.status-filter {
  width: 130px;
}

.list-surface {
  overflow: hidden;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: var(--surface);
  box-shadow: var(--shadow-soft);
}

.list-head,
.list-row {
  display: grid;
  grid-template-columns: minmax(0, 2.2fr) 90px 80px 64px 120px 150px 56px;
  gap: 16px;
  align-items: center;
  padding: 13px 18px;
}

.list-head {
  border-bottom: 1px solid var(--border);
  background: var(--surface-2);
  color: var(--text-3);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.06em;
}

.list-row {
  border-bottom: 1px solid var(--border);
  color: inherit;
  text-decoration: none;
  transition: background 130ms ease;
}

.list-row:last-of-type {
  border-bottom: 0;
}

.list-row:hover {
  background: var(--surface-2);
}

.list-row:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: -2px;
}

.user-cell {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

.user-cell strong {
  font-size: 13.5px;
  font-weight: 620;
}

.user-note {
  overflow: hidden;
  color: var(--text-3);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.tag-row {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  margin-top: 2px;
}

.tag {
  padding: 1px 7px;
  border: 1px solid var(--border);
  border-radius: 999px;
  color: var(--text-3);
  font-size: 10.5px;
}

.count-cell {
  font-variant-numeric: tabular-nums;
}

.count-label {
  display: none;
  color: var(--text-3);
  font-size: 12px;
}

.count-cell strong {
  font-size: 14px;
}

.count-cell span {
  color: var(--text-3);
  font-size: 12px;
}

.online-cell,
.usage-cell {
  color: var(--text-2);
  font-size: 12.5px;
  font-variant-numeric: tabular-nums;
}

.online-cell strong {
  font-size: 14px;
}

.row-action {
  display: inline-flex;
  align-items: center;
  min-height: 28px;
  color: var(--accent);
  font-size: 12.5px;
  font-weight: 600;
  text-align: right;
}

.chip--neutral {
  background: var(--surface-2);
  color: var(--text-2);
}

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 56px 20px;
  color: var(--text-3);
  font-size: 12.5px;
  text-align: center;
}

.empty-state strong {
  color: var(--text-2);
  font-size: 15px;
}

@media (max-width: 960px) {
  .page-header {
    align-items: flex-start;
    flex-direction: column;
  }

  .header-tools {
    width: 100%;
    justify-content: flex-end;
  }

  .toolbar {
    flex-wrap: wrap;
  }

  .search {
    flex: 1 1 100%;
    max-width: none;
  }

  .list-head {
    display: none;
  }

  .list-row {
    grid-template-columns: minmax(0, 1fr) auto;
    grid-template-areas:
      'user status'
      'count online'
      'usage created'
      'action action';
    gap: 8px 12px;
    padding: 15px 16px;
  }

  .user-cell {
    grid-area: user;
  }

  .chip {
    align-self: start;
  }

  .count-cell {
    grid-area: count;
    color: var(--text-3);
    font-size: 12px;
  }

  .count-label {
    display: inline;
  }

  .online-cell {
    grid-area: online;
    justify-self: end;
  }

  .usage-cell {
    grid-area: usage;
  }

  .list-row time {
    grid-area: created;
    justify-self: end;
    font-size: 11.5px;
  }

  .row-action {
    grid-area: action;
    justify-self: start;
  }
}

@media (min-width: 961px) and (max-width: 1200px) {
  .list-head,
  .list-row {
    grid-template-columns: minmax(0, 2fr) 74px 62px 52px 100px 128px 44px;
    gap: 10px;
    padding: 12px 14px;
  }
}
</style>
