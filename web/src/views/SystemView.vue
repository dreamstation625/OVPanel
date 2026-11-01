<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { Activity, Copy, Database, KeyRound, Network, RefreshCw, Settings } from '@lucide/vue'
import { NAlert, NButton, NForm, NFormItem, NInput, NInputNumber, NSelect } from 'naive-ui'
import {
  api,
  ApiError,
  type ExportSettings,
  type Operation,
  type PKIStatus,
  type SystemInfo,
} from '../api'
import { errorMessage, formatDate, operationKindLabel } from '../labels'
import { copyText } from '../utils'
import { useNotify } from '../notify'

const notify = useNotify()
const system = ref<SystemInfo | null>(null)
const pki = ref<PKIStatus | null>(null)
const operations = ref<Operation[]>([])
const exportSettings = ref<ExportSettings | null>(null)
const loading = ref(false)
const error = ref('')

const currentPassword = ref('')
const newPassword = ref('')
const confirmPasswordInput = ref('')
const passwordBusy = ref(false)
const passwordError = ref('')

const exportHost = ref('')
const exportPort = ref<number | null>(1194)
const exportProto = ref<'udp' | 'tcp'>('udp')
const exportBusy = ref(false)
const copied = ref(false)

const protoOptions = [
  { label: 'UDP', value: 'udp' },
  { label: 'TCP', value: 'tcp' },
]

const facts = computed(() => {
  const info = system.value
  if (!info) {
    return []
  }
  return [
    {
      label: '版本',
      value: info.version === 'dev' ? '开发版' : `v${info.version.replace(/^v/, '')}`,
    },
    { label: '构建时间', value: info.buildTime === 'unknown' ? '未知' : formatDate(info.buildTime) },
    { label: '数据存储', value: info.dataStore === 'sqlite' ? '本地数据库' : info.dataStore },
    { label: 'HTTPS', value: info.tlsEnabled ? '已启用' : '未启用' },
  ]
})

async function load() {
  if (loading.value) {
    return
  }
  loading.value = true
  error.value = ''
  try {
    const [info, pkiInfo, settings, ops] = await Promise.all([
      api.system(),
      api.pkiStatus(),
      api.getExportSettings(),
      api.operations(),
    ])
    system.value = info
    pki.value = pkiInfo
    exportSettings.value = settings
    exportHost.value = settings.host
    exportPort.value = settings.port || 1194
    exportProto.value = settings.proto === 'tcp' ? 'tcp' : 'udp'
    operations.value = ops.operations ?? []
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      return
    }
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function changePassword() {
  if (passwordBusy.value) {
    return
  }
  passwordError.value = ''
  if (newPassword.value.length < 8) {
    passwordError.value = '新密码至少需要 8 个字符'
    return
  }
  if (newPassword.value !== confirmPasswordInput.value) {
    passwordError.value = '两次输入的新密码不一致'
    return
  }
  passwordBusy.value = true
  try {
    await api.changeAdminPassword(currentPassword.value, newPassword.value)
    notify.success('密码已更新，需要重新登录，即将返回登录页。')
    currentPassword.value = ''
    newPassword.value = ''
    confirmPasswordInput.value = ''
    window.setTimeout(() => window.location.assign('/'), 1500)
  } catch (err) {
    notify.error(errorMessage(err))
  } finally {
    passwordBusy.value = false
  }
}

async function saveExportSettings() {
  if (exportBusy.value) {
    return
  }
  if (!exportHost.value.trim() || !exportPort.value) {
    notify.warning('请填写 VPN 地址和端口')
    return
  }
  exportBusy.value = true
  try {
    exportSettings.value = await api.saveExportSettings({
      host: exportHost.value.trim(),
      port: exportPort.value,
      proto: exportProto.value,
    })
    notify.success('VPN 连接地址已保存。')
  } catch (err) {
    notify.error(errorMessage(err))
  } finally {
    exportBusy.value = false
  }
}

async function copyFingerprint() {
  if (!pki.value?.ca?.fingerprint) {
    return
  }
  copied.value = await copyText(pki.value.ca.fingerprint)
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
  <div class="page">
    <header class="page-header">
      <div>
        <span class="eyebrow"><Settings :size="14" /> 系统</span>
        <div class="title-row">
          <h1>系统设置</h1>
          <span v-if="system" class="chip chip--ok"><i class="dot" />面板在线</span>
        </div>
      </div>
      <NButton quaternary circle :loading="loading" aria-label="刷新" title="刷新" @click="load">
        <template #icon><RefreshCw :size="17" /></template>
      </NButton>
    </header>

    <NAlert v-if="error" type="error" :bordered="false" class="stack-gap">
      {{ error }}
      <NButton text type="primary" @click="load">重试</NButton>
    </NAlert>

    <div class="settings-grid">
      <section class="surface">
        <header class="surface-head">
          <h2><Network :size="17" /> VPN 连接地址</h2>
          <span class="surface-meta">{{ exportSettings?.configured ? '已配置' : '未配置' }}</span>
        </header>
        <p class="section-description">
          下载的 .ovpn 会使用这里的地址和端口。修改地址不会更换加密密钥。
        </p>
        <NAlert v-if="exportSettings && !exportSettings.configured" type="info" :bordered="false" class="stack-gap">
          下载客户端配置前需要先填写 VPN 地址与端口。
        </NAlert>
        <NForm>
          <NFormItem label="VPN 地址" required>
            <NInput v-model:value="exportHost" placeholder="vpn.example.com 或 IP 地址" maxlength="255" />
          </NFormItem>
          <NFormItem label="端口" required>
            <NInputNumber
              v-model:value="exportPort"
              :min="1"
              :max="65535"
              placeholder="1194"
              class="port-input"
            />
          </NFormItem>
          <NFormItem label="协议" required>
            <NSelect v-model:value="exportProto" :options="protoOptions" />
          </NFormItem>
        </NForm>
        <NButton type="primary" :loading="exportBusy" @click="saveExportSettings">保存连接地址</NButton>
      </section>

      <section class="surface">
        <header class="surface-head">
          <h2><Database :size="17" /> 证书</h2>
        </header>
        <div v-if="pki" class="pki-counts">
          <div><strong>{{ pki.activeCertificates }}</strong><span>有效证书</span></div>
          <div><strong>{{ pki.revokedCertificates }}</strong><span>已作废</span></div>
          <div><strong>{{ pki.expiredCertificates }}</strong><span>已过期</span></div>
        </div>
        <template v-if="pki?.ca">
          <dl class="fact-list">
            <div>
              <dt>颁发机构</dt>
              <dd>{{ pki.ca.subject }}</dd>
            </div>
            <div>
              <dt>证书编号</dt>
              <dd class="is-code">{{ pki.ca.serial }}</dd>
            </div>
            <div>
              <dt>有效期</dt>
              <dd>{{ formatDate(pki.ca.notBefore) }} 至 {{ formatDate(pki.ca.notAfter) }}</dd>
            </div>
          </dl>
          <div class="fingerprint">
            <span>证书指纹</span>
            <code tabindex="0">{{ pki.ca.fingerprint }}</code>
            <NButton size="tiny" @click="copyFingerprint">
              <template #icon><Copy :size="13" /></template>
              {{ copied ? '已复制' : '复制' }}
            </NButton>
          </div>
        </template>
        <NAlert v-else-if="pki" type="info" :bordered="false">
          还没有证书。创建首个证书配置或导出仅密码配置时会自动创建。
        </NAlert>
      </section>

      <section class="surface">
        <header class="surface-head">
          <h2><KeyRound :size="17" /> 管理员密码</h2>
        </header>
        <p class="section-description">修改后需要重新登录，其他设备上的登录也会失效。</p>
        <NAlert v-if="passwordError" type="error" :bordered="false" class="stack-gap">{{ passwordError }}</NAlert>
        <NForm>
          <NFormItem label="当前密码" required>
            <NInput
              v-model:value="currentPassword"
              type="password"
              show-password-on="click"
              placeholder="当前管理员密码"
              autocomplete="current-password"
            />
          </NFormItem>
          <NFormItem label="新密码" required>
            <NInput
              v-model:value="newPassword"
              type="password"
              show-password-on="click"
              placeholder="至少 8 个字符"
              autocomplete="new-password"
            />
          </NFormItem>
          <NFormItem label="确认新密码" required>
            <NInput
              v-model:value="confirmPasswordInput"
              type="password"
              show-password-on="click"
              placeholder="再次输入新密码"
              autocomplete="new-password"
            />
          </NFormItem>
        </NForm>
        <NButton type="primary" :loading="passwordBusy" @click="changePassword">更新密码</NButton>
      </section>

      <section class="surface">
        <header class="surface-head">
          <h2><Activity :size="17" /> 系统信息</h2>
          <span class="surface-meta">{{ system?.users ?? 0 }} 用户 · {{ system?.profiles ?? 0 }} 配置</span>
        </header>
        <dl v-if="system" class="fact-list">
          <div v-for="fact in facts" :key="fact.label">
            <dt>{{ fact.label }}</dt>
            <dd>{{ fact.value }}</dd>
          </div>
        </dl>
        <div v-else class="surface-empty">正在读取…</div>
      </section>

      <section class="surface span-2">
        <header class="surface-head">
          <h2>最近操作</h2>
          <span class="surface-meta">{{ operations.length }} 条</span>
        </header>
        <ul v-if="operations.length" class="operation-list">
          <li v-for="operation in operations" :key="operation.id">
            <div>
              <strong>{{ operationKindLabel(operation.kind) }}</strong>
              <span :title="operation.message">{{ operation.message || '—' }}</span>
            </div>
            <div class="operation-side">
              <span class="chip">{{ stateLabel(operation.state) }}</span>
              <time>{{ formatDate(operation.startedAt) }}</time>
            </div>
          </li>
        </ul>
        <div v-else class="surface-empty">暂无操作记录</div>
      </section>
    </div>


  </div>
</template>

<style scoped>
.page {
  width: min(1000px, 100%);
  margin: 0 auto;
}

.page-header {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 20px;
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

.settings-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 14px;
  align-items: start;
}

.surface {
  min-width: 0;
  padding: 20px;
  border: 1px solid var(--border);
  border-radius: 12px;
  background: var(--surface);
  box-shadow: var(--shadow-soft);
}

.settings-grid > .surface {
  min-width: 0;
}

.span-2 {
  grid-column: 1 / -1;
}

.surface-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}

.surface-head h2 {
  display: flex;
  align-items: center;
  gap: 7px;
  margin: 0;
  font-size: 16px;
  font-weight: 640;
}

.surface-meta {
  color: var(--text-3);
  font-size: 11.5px;
}

.section-description {
  margin: 0 0 14px;
  color: var(--text-3);
  font-size: 12.5px;
  line-height: 1.6;
}

.port-input {
  width: 100%;
}

.pki-counts {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
  margin-bottom: 14px;
}

.pki-counts div {
  display: flex;
  flex-direction: column;
  padding: 10px 12px;
  border-radius: 8px;
  background: var(--surface-2);
}

.pki-counts strong {
  font-size: 20px;
  font-variant-numeric: tabular-nums;
}

.pki-counts span {
  color: var(--text-3);
  font-size: 11px;
}

.fact-list {
  margin: 0;
}

.fact-list > div {
  display: grid;
  grid-template-columns: 104px minmax(0, 1fr);
  gap: 12px;
  padding: 9px 0;
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

.fact-list dd.is-code,
.fingerprint code {
  font-family: var(--font-code);
  font-size: 12px;
}

.fingerprint {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 10px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 9px;
  background: var(--surface-2);
}

.fingerprint > span {
  flex: none;
  color: var(--text-3);
  font-size: 11.5px;
}

.fingerprint code {
  flex: 1;
  min-width: 0;
  overflow-x: auto;
  color: var(--text-2);
  white-space: nowrap;
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
  padding: 10px 0;
  border-top: 1px solid var(--border);
}

.operation-list li:first-child {
  border-top: 0;
  padding-top: 0;
}

.operation-list li > div:first-child {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

.operation-list strong {
  font-family: var(--font-code);
  font-size: 12.5px;
}

.operation-list li > div:first-child span {
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
  gap: 4px;
}

.operation-side time {
  color: var(--text-3);
  font-size: 11.5px;
}

.surface-empty {
  padding: 20px 0;
  color: var(--text-3);
  font-size: 12.5px;
}

@media (max-width: 860px) {
  .settings-grid {
    grid-template-columns: 1fr;
  }

  .span-2 {
    grid-column: auto;
  }
}

@media (max-width: 640px) {
  .page-header {
    align-items: flex-start;
  }

  .surface {
    padding: 16px;
  }

  .fact-list > div {
    grid-template-columns: 88px minmax(0, 1fr);
  }

  .surface-head {
    align-items: flex-start;
    flex-direction: column;
    gap: 4px;
  }


  .fingerprint {
    align-items: flex-start;
    flex-wrap: wrap;
  }

  .fingerprint code {
    flex: 1 1 100%;
    overflow-x: visible;
    white-space: normal;
    overflow-wrap: anywhere;
  }
}
</style>
