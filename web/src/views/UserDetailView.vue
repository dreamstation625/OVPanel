<script setup lang="ts">
import { ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { ArrowLeft, Pencil, Plus, UserRoundX, UserRoundCheck } from '@lucide/vue'
import { NAlert, NButton, NForm, NFormItem, NInput, NModal, NPopconfirm } from 'naive-ui'
import AddProfileModal from '../components/AddProfileModal.vue'
import OneTimePasswordModal from '../components/OneTimePasswordModal.vue'
import { api, ApiError, type AddProfileResult, type UserDetail } from '../api'
import { authLabel, errorMessage, formatBytes, formatDate, shortFingerprint, statusLabel, statusTone } from '../labels'
import { useNotify } from '../notify'

const notify = useNotify()
const route = useRoute()
const detail = ref<UserDetail | null>(null)
const loading = ref(false)
const error = ref('')
const editShow = ref(false)
const editDisplayName = ref('')
const editNotes = ref('')
const editTags = ref('')
const editBusy = ref(false)
const editError = ref('')
const addProfileShow = ref(false)
const oneTime = ref({ show: false, password: '', username: '' })

async function load() {
  const id = String(route.params.id ?? '')
  if (!id) {
    return
  }
  loading.value = true
  error.value = ''
  detail.value = null
  try {
    detail.value = await api.getUser(id)
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      return
    }
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

function openEdit() {
  if (!detail.value) {
    return
  }
  editDisplayName.value = detail.value.user.displayName
  editNotes.value = detail.value.user.notes
  editTags.value = detail.value.user.tags.join(', ')
  editError.value = ''
  editShow.value = true
}

async function saveEdit() {
  if (!detail.value || editBusy.value) {
    return
  }
  editError.value = ''
  if (!editDisplayName.value.trim()) {
    editError.value = '请输入用户名称'
    return
  }
  editBusy.value = true
  try {
    detail.value = await api.updateUser(detail.value.user.id, {
      displayName: editDisplayName.value.trim(),
      notes: editNotes.value.trim(),
      tags: editTags.value
        .split(/[,，、]/)
        .map((tag) => tag.trim())
        .filter(Boolean),
    })
    editShow.value = false
    notify.success('用户信息已更新。')
  } catch (err) {
    notify.error(errorMessage(err))
  } finally {
    editBusy.value = false
  }
}

async function toggleStatus() {
  if (!detail.value) {
    return
  }
  const next = detail.value.user.status === 'active' ? 'disabled' : 'active'
  try {
    detail.value = await api.updateUser(detail.value.user.id, { status: next })
    notify.success(next === 'active' ? '用户已启用。' : '用户已停用。')
  } catch (err) {
    notify.error(errorMessage(err))
  }
}

function handleProfileCreated(result: AddProfileResult) {
  notify.success('客户端配置已创建。')
  load()
  if (result.password) {
    oneTime.value = {
      show: true,
      password: result.password,
      username: result.profile.username ?? '',
    }
  }
}

watch(() => route.params.id, load, { immediate: true })
</script>

<template>
  <div class="page">
    <RouterLink to="/users" class="back-link">
      <ArrowLeft :size="15" /> 返回用户列表
    </RouterLink>

    <NAlert v-if="error" type="error" :bordered="false" class="stack-gap">
      {{ error }}
      <NButton text type="primary" @click="load">重试</NButton>
    </NAlert>

    <template v-if="detail">
      <header class="page-header">
        <div>
          <span class="eyebrow"><UserRoundCheck :size="14" /> 用户详情</span>
          <div class="title-row">
            <h1>{{ detail.user.displayName }}</h1>
            <span class="chip" :class="detail.user.status === 'active' ? 'chip--ok' : 'chip--warn'">
              {{ detail.user.status === 'active' ? '启用' : '已停用' }}
            </span>
          </div>
          <div v-if="detail.user.tags.length" class="tag-row">
            <span v-for="tag in detail.user.tags" :key="tag" class="tag">{{ tag }}</span>
          </div>
        </div>
        <div class="header-tools">
          <NButton @click="openEdit">
            <template #icon><Pencil :size="15" /></template>
            编辑
          </NButton>
          <NButton type="primary" @click="addProfileShow = true">
            <template #icon><Plus :size="16" /></template>
            添加配置
          </NButton>
        </div>
      </header>

      <NAlert v-if="detail.user.status === 'disabled'" type="warning" :bordered="false" class="stack-gap">
        该用户已停用，其所有客户端配置当前都不能连接，已有连接会被断开。
      </NAlert>

      <section class="surface">
        <header class="surface-head">
          <h2>基本信息</h2>
          <NPopconfirm @positive-click="toggleStatus">
            <template #trigger>
              <NButton size="small" :type="detail.user.status === 'active' ? 'default' : 'primary'">
                <template #icon>
                  <component :is="detail.user.status === 'active' ? UserRoundX : UserRoundCheck" :size="15" />
                </template>
                {{ detail.user.status === 'active' ? '停用用户' : '启用用户' }}
              </NButton>
            </template>
            {{ detail.user.status === 'active' ? '停用后该用户的全部配置都不可连接，确认继续？' : '启用该用户及其配置？' }}
          </NPopconfirm>
        </header>
        <dl class="fact-list">
          <div>
            <dt>备注</dt>
            <dd class="notes">{{ detail.user.notes || '—' }}</dd>
          </div>
          <div>
            <dt>创建时间</dt>
            <dd>{{ formatDate(detail.user.createdAt) }}</dd>
          </div>
          <div>
            <dt>配置数量</dt>
            <dd>{{ detail.profiles.length }} 份</dd>
          </div>
        </dl>
      </section>

      <section class="surface">
        <header class="surface-head">
          <h2>客户端配置</h2>
          <span class="surface-meta">{{ detail.profiles.length }} 份</span>
        </header>

        <div v-if="detail.profiles.length" class="profile-list">
          <RouterLink
            v-for="profile in detail.profiles"
            :key="profile.id"
            :to="`/profiles/${profile.id}`"
            class="profile-row"
          >
            <div class="profile-main">
              <strong>{{ profile.name }}</strong>
              <span>
                {{ authLabel(profile.authType) }}
                <template v-if="profile.username"> · {{ profile.username }}</template>
                <template v-else-if="profile.certificate"> · {{ shortFingerprint(profile.certificate.fingerprint) }}</template>
              </span>
            </div>
            <div class="profile-side">
              <span class="profile-stats">
                在线 {{ profile.onlineSessions }} · {{ formatBytes(profile.usageBytes) }}
              </span>
              <span class="chip" :class="`chip--${statusTone(profile.effectiveStatus)}`">
                {{ statusLabel(profile.effectiveStatus) }}
              </span>
              <span class="row-action">查看</span>
            </div>
          </RouterLink>
        </div>
        <div v-else class="empty-state">
          <strong>暂无客户端配置</strong>
          <span>添加配置后可以下载 .ovpn 并查看证书状态。</span>
          <NButton type="primary" size="small" @click="addProfileShow = true">添加配置</NButton>
        </div>
      </section>
    </template>
    <div v-else-if="loading" class="surface loading-surface">正在读取用户…</div>

    <NModal
      v-model:show="editShow"
      preset="card"
      title="编辑用户"
      :style="{ width: 'min(500px, calc(100vw - 28px))' }"
      :mask-closable="!editBusy"
    >
      <NAlert v-if="editError" type="error" :bordered="false" class="stack-gap">{{ editError }}</NAlert>
      <NForm>
        <NFormItem label="用户名称" required>
          <NInput v-model:value="editDisplayName" maxlength="80" />
        </NFormItem>
        <NFormItem label="备注">
          <NInput v-model:value="editNotes" type="textarea" :autosize="{ minRows: 3, maxRows: 6 }" maxlength="2000" />
        </NFormItem>
        <NFormItem label="标签">
          <NInput v-model:value="editTags" placeholder="用逗号分隔" maxlength="200" />
        </NFormItem>
      </NForm>
      <template #footer>
        <div class="modal-actions">
          <NButton :disabled="editBusy" @click="editShow = false">取消</NButton>
          <NButton type="primary" :loading="editBusy" @click="saveEdit">保存</NButton>
        </div>
      </template>
    </NModal>

    <AddProfileModal v-model:show="addProfileShow" :user-id="String(route.params.id)" @created="handleProfileCreated" />
    <OneTimePasswordModal
      :show="oneTime.show"
      :password="oneTime.password"
      :username="oneTime.username"
      title="配置已创建"
      @update:show="oneTime.show = $event"
    />
  </div>
</template>

<style scoped>
.page {
  width: min(1000px, 100%);
  margin: 0 auto;
}

.back-link {
  display: inline-flex;
  align-items: center;
  min-height: 30px;
  gap: 5px;
  margin-bottom: 18px;
  color: var(--text-3);
  font-size: 12.5px;
  text-decoration: none;
}

.back-link:hover {
  color: var(--text-1);
}

.page-header {
  display: flex;
  align-items: flex-start;
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

.title-row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 4px;
}

.header-tools {
  display: flex;
  gap: 8px;
}

.tag-row {
  display: flex;
  flex-wrap: wrap;
  gap: 5px;
  margin-top: 10px;
}

.tag {
  padding: 2px 8px;
  border: 1px solid var(--border);
  border-radius: 999px;
  color: var(--text-3);
  font-size: 11px;
}

.surface {
  margin-bottom: 14px;
  padding: 20px;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: var(--surface);
  box-shadow: var(--shadow-soft);
}

.surface-head {
  display: flex;
  align-items: center;
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

.fact-list dd.notes {
  white-space: pre-wrap;
}

.fact-list dd.is-code {
  font-family: var(--font-code);
  font-size: 12px;
}

.profile-list {
  display: flex;
  flex-direction: column;
}

.profile-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 13px 0;
  border-top: 1px solid var(--border);
  color: inherit;
  text-decoration: none;
}

.profile-row:first-child {
  border-top: 0;
  padding-top: 0;
}

.profile-row:hover .profile-main strong {
  color: var(--accent);
}

.profile-main {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.profile-main strong {
  font-size: 13.5px;
  font-weight: 620;
  transition: color 130ms ease;
}

.profile-main span {
  color: var(--text-3);
  font-size: 11.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.profile-side {
  display: flex;
  align-items: center;
  gap: 12px;
}

.profile-stats {
  color: var(--text-3);
  font-size: 11.5px;
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
}

.row-action {
  color: var(--accent);
  font-size: 12.5px;
  font-weight: 600;
}

.empty-state {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  padding: 40px 16px;
  color: var(--text-3);
  font-size: 12.5px;
  text-align: center;
}

.empty-state strong {
  color: var(--text-2);
  font-size: 14px;
}

.loading-surface {
  color: var(--text-3);
  font-size: 13px;
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

@media (max-width: 640px) {
  .page-header {
    flex-direction: column;
  }

  .header-tools {
    width: 100%;
    justify-content: flex-end;
  }

  .surface {
    padding: 16px;
  }

  .fact-list > div {
    grid-template-columns: 88px minmax(0, 1fr);
  }

  .profile-row {
    align-items: flex-start;
  }

  .profile-side {
    flex-direction: column;
    align-items: flex-end;
    gap: 6px;
  }
}
</style>
