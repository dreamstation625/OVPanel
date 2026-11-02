<script setup lang="ts">
import { onMounted, reactive, ref, watch } from 'vue'
import { Archive, Download, RefreshCw, ShieldCheck, Trash2, Upload } from '@lucide/vue'
import { NAlert, NButton, NForm, NFormItem, NInput, NInputNumber, NModal, NSwitch } from 'naive-ui'
import ConfirmModal from '../components/ConfirmModal.vue'
import {
  api,
  ApiError,
  type BackupItem,
  type BackupListResponse,
  type BackupSettings,
  type RestorePreview,
  type RestoreResult,
} from '../api'
import { errorMessage, formatBytes, formatDate } from '../labels'
import { downloadBlob } from '../utils'
import { useNotify } from '../notify'

interface PromptState {
  show: boolean
  title: string
  description: string
  actionLabel: string
  loading: boolean
  danger: boolean
  action: (() => Promise<void>) | null
}

const notify = useNotify()
const data = ref<BackupListResponse | null>(null)
const loading = ref(false)
const error = ref('')
const settingsForm = reactive({
  enabled: false,
  scheduleTime: '03:00',
  retentionCount: 7,
  backupPassword: '',
  backupConfirm: '',
})
const settingsBusy = ref(false)
const restoreFile = ref<File | null>(null)
const restorePassword = ref('')
const preview = ref<RestorePreview | null>(null)
const restoreResult = ref<RestoreResult | null>(null)
const previewBusy = ref(false)
const restoreBusy = ref(false)

const prompt = ref<PromptState>({
  show: false,
  title: '',
  description: '',
  actionLabel: '确认',
  loading: false,
  danger: false,
  action: null,
})

function openPrompt(
  title: string,
  description: string,
  actionLabel: string,
  action: () => Promise<void>,
  danger = false,
) {
  prompt.value = { show: true, title, description, actionLabel, loading: false, danger, action }
}

async function confirmPrompt() {
  const action = prompt.value.action
  if (!action) {
    return
  }
  prompt.value.loading = true
  try {
    await action()
    prompt.value.show = false
  } catch (err) {
    prompt.value.show = false
    notify.error(errorMessage(err))
  } finally {
    prompt.value.loading = false
  }
}

async function load() {
  if (loading.value) {
    return
  }
  loading.value = true
  error.value = ''
  try {
    data.value = await api.listBackups()
    syncSettings(data.value.settings)
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      return
    }
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

function syncSettings(settings: BackupSettings) {
  settingsForm.enabled = settings.enabled
  settingsForm.scheduleTime = settings.scheduleTime
  settingsForm.retentionCount = settings.retentionCount
}

function saveSettings() {
  const time = settingsForm.scheduleTime.trim()
  if (!/^([01][0-9]|2[0-3]):[0-5][0-9]$/.test(time)) {
    notify.warning('备份时间必须为 HH:MM')
    return
  }
  if (settingsForm.retentionCount < 1 || settingsForm.retentionCount > 90) {
    notify.warning('保留数量需在 1 到 90 之间')
    return
  }
  const configured = data.value?.settings.configured ?? false
  if (!configured && !settingsForm.backupPassword) {
    notify.warning('首次设置必须填写备份恢复密码')
    return
  }
  if (settingsForm.backupPassword && settingsForm.backupPassword !== settingsForm.backupConfirm) {
    notify.warning('两次输入的恢复密码不一致')
    return
  }
  if (settingsForm.backupPassword && settingsForm.backupPassword.length < 12) {
    notify.warning('恢复密码至少需要 12 个字符')
    return
  }

  openPrompt('确认备份设置', '修改备份计划或恢复密码会影响后续备份，确认？', '保存设置', async () => {
    settingsBusy.value = true
    try {
      const result = await api.saveBackupSettings({
        backupPassword: settingsForm.backupPassword || undefined,
        enabled: settingsForm.enabled,
        scheduleTime: time,
        retentionCount: settingsForm.retentionCount,
      })
      settingsForm.backupPassword = ''
      settingsForm.backupConfirm = ''
      data.value = data.value ? { ...data.value, settings: result.settings } : data.value
      notify.success('备份设置已保存。')
      await load()
    } finally {
      settingsBusy.value = false
    }
  })
}

function manualBackup() {
  if (!data.value?.settings.configured) {
    notify.warning('请先设置备份恢复密码')
    return
  }
  openPrompt(
    '立即创建备份',
    '备份包含用户、证书、用量记录和密钥；请保存好恢复密码。',
    '开始备份',
    async () => {
      await api.createBackup('手动备份')
      notify.success('备份已创建。')
      await load()
    },
  )
}

function download(item: BackupItem) {
  openPrompt('下载备份包', '备份包使用恢复密码加密，确认下载？', '下载', async () => {
    const result = await api.downloadBackup(item.id)
    downloadBlob(result.blob, result.filename)
    notify.success('备份包已开始下载。')
  })
}

function remove(item: BackupItem) {
  openPrompt(
    '删除备份',
    `删除 ${item.filename}？此操作不可恢复。`,
    '删除',
    async () => {
      await api.deleteBackup(item.id)
      notify.success('备份已删除。')
      await load()
    },
    true,
  )
}

function pickFile(event: Event) {
  const input = event.target as HTMLInputElement
  restoreFile.value = input.files?.[0] ?? null
  preview.value = null
  restoreResult.value = null
}

function startPreview() {
  if (!restoreFile.value) {
    notify.warning('请选择备份包文件')
    return
  }
  if (!restorePassword.value) {
    notify.warning('请输入备份恢复密码')
    return
  }
  openPrompt(
    '检查备份包',
    '解密并检查备份包，显示恢复内容，不修改数据。',
    '检查',
    async () => {
      previewBusy.value = true
      try {
        preview.value = await api.previewRestore(restoreFile.value!, restorePassword.value)
        notify.success('备份包检查通过，请确认恢复影响。')
      } finally {
        previewBusy.value = false
      }
    },
  )
}

function executeRestore() {
  if (!preview.value) {
    return
  }
  const token = preview.value.token
  openPrompt(
    '恢复数据',
    '恢复会断开所有 VPN 连接，将数据恢复到备份时间点，完成后需重新登录。确认继续？',
    '开始恢复',
    async () => {
      restoreBusy.value = true
      try {
        const result = await api.executeRestore(token)
        restoreResult.value = result
        preview.value = null
        if (result.warnings.length > 0) {
          notify.warning('恢复已完成，请查看结果中的提醒。')
        } else {
          notify.success('恢复完成，请使用备份时间点的管理员密码重新登录。')
        }
      } finally {
        restoreBusy.value = false
      }
    },
    true,
  )
}

function kindLabel(kind: BackupItem['kind']) {
  switch (kind) {
    case 'manual':
      return '手动'
    case 'scheduled':
      return '定时'
    case 'pre_restore':
      return '恢复前'
    default:
      return kind
  }
}

watch(data, (value) => {
  if (value) {
    syncSettings(value.settings)
  }
})

onMounted(load)
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <span class="eyebrow"><Archive :size="14" /> 备份</span>
        <h1>备份与恢复</h1>
        <p class="page-meta">
          {{ data?.settings.configured ? '恢复密码已设置' : '尚未设置恢复密码' }}
          <template v-if="data?.settings.nextRunAt"> · 下次自动备份 {{ formatDate(data.settings.nextRunAt) }}</template>
        </p>
      </div>
      <div class="header-tools">
        <NButton quaternary circle :loading="loading" aria-label="刷新" title="刷新" @click="load">
          <template #icon><RefreshCw :size="17" /></template>
        </NButton>
        <NButton type="primary" :loading="settingsBusy" @click="manualBackup">
          <template #icon><Archive :size="16" /></template>
          立即备份
        </NButton>
      </div>
    </header>

    <NAlert v-if="error" type="error" :bordered="false" closable class="stack-gap" @close="error = ''">
      {{ error }}
    </NAlert>

    <NAlert v-if="restoreResult" type="warning" :bordered="false" class="stack-gap">
      恢复完成，已创建恢复前备份 <code>{{ restoreResult.preRestoreBackupId }}</code>。
      请使用备份时间点的管理员密码重新登录。
      <RouterLink to="/login" class="text-link">前往登录</RouterLink>
    </NAlert>

    <section class="surface">
      <header class="surface-head">
        <h2>备份设置</h2>
        <span class="surface-meta">自动备份保存在本机，不同步到其他服务器</span>
      </header>
      <NForm>
        <div class="settings-grid">
          <NFormItem label="备份恢复密码">
            <NInput
              v-model:value="settingsForm.backupPassword"
              type="password"
              show-password-on="click"
              :placeholder="data?.settings.configured ? '留空表示不修改' : '至少 12 个字符'"
              autocomplete="new-password"
            />
          </NFormItem>
          <NFormItem label="确认备份恢复密码">
            <NInput
              v-model:value="settingsForm.backupConfirm"
              type="password"
              show-password-on="click"
              placeholder="再次输入"
              autocomplete="new-password"
            />
          </NFormItem>
          <NFormItem label="每日备份时间">
            <NInput v-model:value="settingsForm.scheduleTime" placeholder="03:00" maxlength="5" />
          </NFormItem>
          <NFormItem label="保留备份数">
            <NInputNumber v-model:value="settingsForm.retentionCount" :min="1" :max="90" :show-button="false" />
          </NFormItem>
        </div>
        <div class="schedule-line">
          <NSwitch v-model:value="settingsForm.enabled" aria-label="启用每日自动备份" />
          <span>启用每日自动备份</span>
          <span class="muted">备份失败时保留最近一次可用备份</span>
        </div>
        <div class="actions-line">
          <NButton type="primary" :loading="settingsBusy" @click="saveSettings">保存备份设置</NButton>
        </div>
      </NForm>
    </section>

    <section class="surface">
      <header class="surface-head">
        <h2>备份包</h2>
        <span class="surface-meta">
          {{ data?.backups.length ?? 0 }} 个 · 占用 {{ formatBytes(data?.storageBytes) }}
        </span>
      </header>
      <div v-if="data?.backups.length" class="backup-list">
        <div v-for="item in data.backups" :key="item.id" class="backup-row">
          <div class="backup-main">
            <div class="backup-title">
              <span class="chip" :class="item.kind === 'pre_restore' ? 'chip--warn' : 'chip--neutral'">
                {{ kindLabel(item.kind) }}
              </span>
              <strong>{{ item.note || item.filename }}</strong>
            </div>
            <span class="backup-meta">
              {{ formatDate(item.createdAt) }} · {{ formatBytes(item.sizeBytes) }} ·
              服务端配置版本 {{ item.configRevision || '默认' }}
            </span>
          </div>
          <div class="backup-actions">
            <NButton size="small" @click="download(item)">
              <template #icon><Download :size="14" /></template>
              下载
            </NButton>
            <NButton size="small" type="error" ghost @click="remove(item)">
              <template #icon><Trash2 :size="14" /></template>
              删除
            </NButton>
          </div>
        </div>
      </div>
      <div v-else class="surface-empty">
        <strong>暂无备份</strong>
        <span>设置恢复密码后可立即备份，或启用每日自动备份。</span>
      </div>
    </section>

    <section class="surface">
      <header class="surface-head">
        <h2>从备份恢复</h2>
        <span class="surface-meta">恢复时 VPN 会中断，完成后需重新登录</span>
      </header>
      <div class="restore-grid">
        <label class="file-picker">
          <input type="file" accept=".ovpbak,application/octet-stream" @change="pickFile" />
          <Upload :size="17" />
          <span>{{ restoreFile ? restoreFile.name : '选择 .ovpbak 备份包' }}</span>
        </label>
        <NInput
          v-model:value="restorePassword"
          type="password"
          show-password-on="click"
          placeholder="备份恢复密码"
          autocomplete="off"
        />
        <NButton type="warning" :loading="previewBusy" @click="startPreview">检查备份包</NButton>
      </div>
      <p class="restore-hint">
        恢复前会备份当前数据并停止 VPN；检查失败、密码错误或空间不足时不修改数据。
      </p>
    </section>

    <NModal
      :show="Boolean(preview)"
      preset="card"
      title="确认恢复影响"
      :style="{ width: 'min(620px, calc(100vw - 28px))' }"
      :mask-closable="!restoreBusy"
      @update:show="!$event && (preview = null)"
    >
      <template v-if="preview">
        <dl class="preview-facts">
          <div><dt>备份时间</dt><dd>{{ formatDate(preview.manifest.createdAt) }}</dd></div>
          <div><dt>面板版本</dt><dd>{{ preview.manifest.appVersion }}</dd></div>
          <div><dt>用户 / 配置</dt><dd>{{ preview.manifest.users }} / {{ preview.manifest.profiles }}</dd></div>
          <div><dt>证书</dt><dd>有效 {{ preview.manifest.validCertificates }} · 已作废 {{ preview.manifest.revokedCertificates }}</dd></div>
          <div><dt>额度重置次数</dt><dd>{{ preview.manifest.quotaResets }}</dd></div>
          <div><dt>用量记录</dt><dd>{{ preview.manifest.agentPresent ? '包含' : '缺失' }}</dd></div>
        </dl>
        <ul class="warning-list">
          <li v-for="warning in preview.warnings" :key="warning">{{ warning }}</li>
        </ul>
        <div class="modal-actions">
          <NButton :disabled="restoreBusy" @click="preview = null">取消</NButton>
          <NButton type="error" :loading="restoreBusy" @click="executeRestore">
            <template #icon><ShieldCheck :size="15" /></template>
            确认恢复
          </NButton>
        </div>
      </template>
    </NModal>

    <ConfirmModal
      v-model:show="prompt.show"
      :title="prompt.title"
      :description="prompt.description"
      :action-label="prompt.actionLabel"
      :loading="prompt.loading"
      :danger="prompt.danger"
      @confirm="confirmPrompt"
    />
  </div>
</template>

<style scoped>
.page {
  width: min(1080px, 100%);
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

.settings-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0 14px;
}

.schedule-line {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  padding: 8px 0;
  color: var(--text-2);
  font-size: 13px;
}

.muted {
  color: var(--text-3);
  font-size: 11.5px;
}

.actions-line,
.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 10px;
}

.backup-list {
  display: flex;
  flex-direction: column;
}

.backup-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  padding: 13px 0;
  border-top: 1px solid var(--border);
}

.backup-row:first-child {
  border-top: 0;
  padding-top: 0;
}

.backup-main {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.backup-title {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.backup-title strong {
  font-size: 13.5px;
  font-weight: 620;
}

.backup-meta {
  color: var(--text-3);
  font-size: 11.5px;
}

.backup-actions {
  display: flex;
  flex: none;
  gap: 6px;
}

.restore-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.4fr) minmax(0, 1fr) auto;
  gap: 10px;
  align-items: center;
}

.file-picker {
  position: relative;
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 38px;
  padding: 0 12px;
  overflow: hidden;
  border: 1px dashed var(--border-strong, var(--border));
  border-radius: 8px;
  color: var(--text-2);
  font-size: 12.5px;
  cursor: pointer;
}

.file-picker input {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
  cursor: pointer;
  opacity: 0;
}

.file-picker:focus-within {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}

.file-picker span {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.restore-hint {
  margin: 10px 0 0;
  color: var(--text-3);
  font-size: 11.5px;
  line-height: 1.6;
}

.surface-empty {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 6px;
  min-height: 120px;
  justify-content: center;
  color: var(--text-3);
  font-size: 12.5px;
}

.surface-empty strong {
  color: var(--text-2);
  font-size: 14px;
  font-weight: 600;
}

.text-link {
  margin-left: 6px;
  color: var(--accent);
  text-decoration: none;
}

.preview-facts {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 8px 16px;
  margin: 0 0 12px;
}

.preview-facts > div {
  display: flex;
  justify-content: space-between;
  gap: 10px;
  padding: 7px 0;
  border-bottom: 1px solid var(--border);
}

.preview-facts dt {
  color: var(--text-3);
  font-size: 12px;
}

.preview-facts dd {
  margin: 0;
  color: var(--text-1);
  font-size: 12.5px;
}

.warning-list {
  margin: 0 0 8px;
  padding-left: 18px;
  color: var(--warn);
  font-size: 12.5px;
  line-height: 1.7;
}

@media (max-width: 760px) {
  .page-header {
    align-items: flex-start;
    flex-direction: column;
  }

  .header-tools {
    width: 100%;
    justify-content: flex-end;
  }

  .settings-grid,
  .preview-facts {
    grid-template-columns: 1fr;
  }

  .restore-grid {
    grid-template-columns: 1fr;
  }

  .actions-line,
  .modal-actions {
    justify-content: flex-start;
  }

  .backup-row {
    align-items: flex-start;
    flex-direction: column;
  }

  .surface {
    padding: 16px;
  }

  .surface-head {
    align-items: flex-start;
    flex-direction: column;
    gap: 4px;
  }

  .file-picker {
    min-height: 44px;
  }

  .backup-actions :deep(.n-button) {
    min-height: 36px;
  }
}
</style>
