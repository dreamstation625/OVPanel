<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeft, Copy, Download, KeyRound, RefreshCw, ShieldOff, ShieldCheck } from '@lucide/vue'
import { NAlert, NButton, NForm, NFormItem, NInput, NPopconfirm, NSelect } from 'naive-ui'
import PasswordPromptModal from '../components/PasswordPromptModal.vue'
import OneTimePasswordModal from '../components/OneTimePasswordModal.vue'
import { api, ApiError, type AccessMode, type Profile, type RouteMode } from '../api'
import {
  authLabel,
  authRequiresCertificate,
  authRequiresPassword,
  errorMessage,
  formatDate,
  formatFingerprint,
  statusLabel,
  statusTone,
} from '../labels'
import { copyText, downloadBlob } from '../utils'

interface PromptState {
  show: boolean
  title: string
  description: string
  actionLabel: string
  loading: boolean
  error: string
  action: ((password: string) => Promise<void>) | null
}

const route = useRoute()
const router = useRouter()
const profile = ref<Profile | null>(null)
const loading = ref(true)
const error = ref('')
const success = ref('')
const copied = ref(false)
const oneTime = ref({ show: false, password: '', username: '' })
const prompt = ref<PromptState>({
  show: false,
  title: '',
  description: '',
  actionLabel: '确认',
  loading: false,
  error: '',
  action: null,
})

const policy = reactive({
  routeMode: 'inherit' as RouteMode,
  accessMode: 'inherit' as AccessMode,
  allowedText: '',
})
const policyBusy = ref(false)
const routeOptions = [
  { label: '继承服务端', value: 'inherit' },
  { label: '仅访问内网', value: 'split' },
  { label: '全部 IPv4 流量经过 VPN', value: 'global' },
]
const accessOptions = [
  { label: '继承服务端', value: 'inherit' },
  { label: '仅允许指定网段', value: 'allow' },
  { label: '禁止访问内网', value: 'deny' },
]

const backTarget = computed(() => (profile.value ? `/users/${profile.value.userId}` : '/users'))
const hasCertificate = computed(() => Boolean(profile.value?.certificate))
const canDownload = computed(() => {
  if (!profile.value) {
    return false
  }
  return !authRequiresCertificate(profile.value.authType) || hasCertificate.value
})

async function load() {
  const id = String(route.params.id ?? '')
  if (!id) {
    return
  }
  loading.value = true
  error.value = ''
  success.value = ''
  profile.value = null
  try {
    profile.value = await api.getProfile(id)
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      return
    }
    error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

function openPrompt(title: string, description: string, actionLabel: string, action: (password: string) => Promise<void>) {
  prompt.value = {
    show: true,
    title,
    description,
    actionLabel,
    loading: false,
    error: '',
    action,
  }
}

async function confirmPassword(password: string) {
  if (!prompt.value.action) {
    return
  }
  prompt.value.loading = true
  prompt.value.error = ''
  try {
    await prompt.value.action(password)
    prompt.value.show = false
  } catch (err) {
    if (err instanceof ApiError && (err.code === 'reauth_failed' || err.code === 'rate_limited')) {
      prompt.value.error = err.message
      return
    }
    prompt.value.show = false
    error.value = errorMessage(err)
  } finally {
    prompt.value.loading = false
  }
}

function exportProfile() {
  if (!profile.value) {
    return
  }
  const id = profile.value.id
  openPrompt(
    '下载客户端配置',
    '文件包含客户端私钥与 tls-crypt 密钥，请通过安全方式交给使用者，不要再次公开传输。',
    '下载',
    async (password) => {
      const result = await api.exportProfile(id, password)
      downloadBlob(result.blob, result.filename)
      success.value = '客户端配置已开始下载。'
    },
  )
}

function resetPassword() {
  if (!profile.value) {
    return
  }
  const id = profile.value.id
  openPrompt('重置 VPN 密码', '旧密码立即失效；新密码只会显示一次。', '重置密码', async (password) => {
    const result = await api.resetProfilePassword(id, password)
    profile.value = result.profile
    oneTime.value = {
      show: true,
      password: result.password,
      username: result.profile.username ?? '',
    }
    success.value = '密码已重置。'
  })
}

function rotateCertificate() {
  if (!profile.value) {
    return
  }
  const id = profile.value.id
  openPrompt(
    '轮换客户端证书',
    '旧证书会立即吊销，所有使用旧配置的设备都需要重新下载 .ovpn。',
    '轮换证书',
    async (password) => {
      const result = await api.rotateCertificate(id, password)
      profile.value = result.profile
      success.value = '证书已轮换。'
    },
  )
}

function revokeCertificate() {
  if (!profile.value) {
    return
  }
  const id = profile.value.id
  openPrompt(
    '吊销客户端证书',
    '吊销不可恢复。吊销后该配置无法连接，需要签发新证书才能恢复。',
    '吊销证书',
    async (password) => {
      const result = await api.revokeCertificate(id, password)
      profile.value = result.profile
      success.value = '证书已吊销。'
    },
  )
}

async function toggleStatus() {
  if (!profile.value) {
    return
  }
  const next = profile.value.status === 'active' ? 'disabled' : 'active'
  try {
    profile.value = await api.updateProfile(profile.value.id, { status: next })
    success.value = next === 'active' ? '配置已启用。' : '配置已停用。'
  } catch (err) {
    error.value = errorMessage(err)
  }
}

async function copyFingerprint() {
  if (!profile.value?.certificate) {
    return
  }
  copied.value = await copyText(profile.value.certificate.fingerprint)
}

async function savePolicy() {
  const current = profile.value
  if (!current || policyBusy.value) {
    return
  }
  policyBusy.value = true
  error.value = ''
  success.value = ''
  try {
    profile.value = await api.updateProfile(current.id, {
      routeMode: policy.routeMode,
      accessMode: policy.accessMode,
      allowedInternal:
        policy.accessMode === 'allow'
          ? policy.allowedText
              .split(/[,，\s]+/)
              .map((value) => value.trim())
              .filter(Boolean)
          : [],
    })
    success.value = '网络策略已保存；路由变更在重新连接后生效。'
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    policyBusy.value = false
  }
}

watch(
  profile,
  (value) => {
    if (!value) {
      return
    }
    policy.routeMode = value.routeMode
    policy.accessMode = value.accessMode
    policy.allowedText = value.allowedInternal.join(', ')
  },
  { immediate: true },
)

watch(() => route.params.id, load, { immediate: true })
</script>

<template>
  <div class="page">
    <RouterLink :to="backTarget" class="back-link">
      <ArrowLeft :size="15" /> 返回用户
    </RouterLink>

    <NAlert v-if="error" type="error" :bordered="false" closable class="stack-gap" @close="error = ''">
      {{ error }}
      <NButton v-if="error.includes('VPN 连接地址')" text type="primary" @click="router.push('/system')">
        前往系统设置
      </NButton>
      <NButton v-else text type="primary" @click="load">重试</NButton>
    </NAlert>
    <NAlert v-if="success" type="success" :bordered="false" closable class="stack-gap" @close="success = ''">
      {{ success }}
    </NAlert>

    <template v-if="profile">
      <header class="page-header">
        <div>
          <span class="eyebrow"><ShieldCheck :size="14" /> 客户端配置</span>
          <div class="title-row">
            <h1>{{ profile.name }}</h1>
            <span class="chip" :class="`chip--${statusTone(profile.effectiveStatus)}`">
              {{ statusLabel(profile.effectiveStatus) }}
            </span>
          </div>
          <p class="page-meta">
            {{ profile.userDisplayName }} · {{ authLabel(profile.authType) }}
          </p>
        </div>
        <div class="header-tools">
          <NButton type="primary" :disabled="!canDownload" @click="exportProfile">
            <template #icon><Download :size="16" /></template>
            下载 .ovpn
          </NButton>
        </div>
      </header>

      <NAlert v-if="profile.effectiveStatus !== 'active'" type="warning" :bordered="false" class="stack-gap">
        {{ profile.statusReason }}。运行代理的实际准入与断开在后续阶段接入。
      </NAlert>

      <section class="surface">
        <header class="surface-head">
          <h2>认证与状态</h2>
          <div class="action-row">
            <NButton v-if="authRequiresPassword(profile.authType)" size="small" @click="resetPassword">
              <template #icon><KeyRound :size="14" /></template>
              重置密码
            </NButton>
            <NButton v-if="authRequiresCertificate(profile.authType)" size="small" @click="rotateCertificate">
              <template #icon><RefreshCw :size="14" /></template>
              {{ hasCertificate ? '轮换证书' : '签发新证书' }}
            </NButton>
            <NButton v-if="hasCertificate" size="small" type="error" ghost @click="revokeCertificate">
              <template #icon><ShieldOff :size="14" /></template>
              吊销证书
            </NButton>
            <NPopconfirm @positive-click="toggleStatus">
              <template #trigger>
                <NButton size="small" :type="profile.status === 'active' ? 'default' : 'primary'">
                  {{ profile.status === 'active' ? '停用配置' : '启用配置' }}
                </NButton>
              </template>
              {{ profile.status === 'active' ? '停用后所有使用该配置的设备都不可连接，确认继续？' : '启用该配置？' }}
            </NPopconfirm>
          </div>
        </header>

        <dl class="fact-list">
          <div>
            <dt>所属用户</dt>
            <dd>
              <RouterLink :to="backTarget" class="text-link">{{ profile.userDisplayName }}</RouterLink>
            </dd>
          </div>
          <div>
            <dt>认证方式</dt>
            <dd>{{ authLabel(profile.authType) }}</dd>
          </div>
          <div v-if="profile.username">
            <dt>登录名</dt>
            <dd class="is-code">{{ profile.username }}</dd>
          </div>
          <div>
            <dt>创建时间</dt>
            <dd>{{ formatDate(profile.createdAt) }}</dd>
          </div>
          <div v-if="profile.expiresAt">
            <dt>访问到期</dt>
            <dd>{{ formatDate(profile.expiresAt) }}</dd>
          </div>
          <div>
            <dt>配置 ID</dt>
            <dd class="is-code">{{ profile.id }}</dd>
          </div>
        </dl>
      </section>

      <section class="surface">
        <header class="surface-head">
          <h2>网络策略</h2>
          <span class="surface-meta">路由与访问权限分别执行</span>
        </header>
        <NForm>
          <div class="policy-grid">
            <NFormItem label="路由方式">
              <NSelect v-model:value="policy.routeMode" :options="routeOptions" />
            </NFormItem>
            <NFormItem label="内网权限">
              <NSelect v-model:value="policy.accessMode" :options="accessOptions" />
            </NFormItem>
          </div>
          <NFormItem v-if="policy.accessMode === 'allow'" label="授权内网网段">
            <NInput
              v-model:value="policy.allowedText"
              type="textarea"
              :autosize="{ minRows: 2, maxRows: 4 }"
              placeholder="例如 10.0.16.0/22，用逗号或换行分隔"
            />
          </NFormItem>
          <p class="policy-hint">
            路由方式决定客户端拿到哪些路由；内网权限决定运行代理实际放行哪些目标。继承时使用服务端默认值。
          </p>
          <div class="policy-actions">
            <NButton type="primary" :loading="policyBusy" @click="savePolicy">保存网络策略</NButton>
          </div>
        </NForm>
      </section>

      <section v-if="authRequiresCertificate(profile.authType)" class="surface">
        <header class="surface-head">
          <h2>客户端证书</h2>
          <span class="surface-meta">{{ profile.certificate ? '当前生效' : '无生效证书' }}</span>
        </header>

        <template v-if="profile.certificate">
          <dl class="fact-list">
            <div>
              <dt>序列号</dt>
              <dd class="is-code">{{ profile.certificate.serialNumber }}</dd>
            </div>
            <div>
              <dt>Common Name</dt>
              <dd class="is-code">{{ profile.certificate.commonName }}</dd>
            </div>
            <div>
              <dt>有效期</dt>
              <dd>{{ formatDate(profile.certificate.issuedAt) }} 至 {{ formatDate(profile.certificate.expiresAt) }}</dd>
            </div>
          </dl>
          <div class="fingerprint">
            <span>SHA-256 指纹</span>
            <code>{{ formatFingerprint(profile.certificate.fingerprint) }}</code>
            <NButton size="tiny" @click="copyFingerprint">
              <template #icon><Copy :size="13" /></template>
              {{ copied ? '已复制' : '复制' }}
            </NButton>
          </div>
        </template>
        <NAlert v-else type="error" :bordered="false">
          没有生效证书。请签发新证书后再下载配置。
        </NAlert>
      </section>
    </template>
    <div v-else-if="loading" class="surface loading-surface">正在读取配置…</div>

    <PasswordPromptModal
      v-model:show="prompt.show"
      :title="prompt.title"
      :description="prompt.description"
      :action-label="prompt.actionLabel"
      :loading="prompt.loading"
      :error="prompt.error"
      @confirm="confirmPassword"
    />
    <OneTimePasswordModal
      :show="oneTime.show"
      :password="oneTime.password"
      :username="oneTime.username"
      title="VPN 密码已重置"
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
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 4px;
}

.page-meta {
  margin: 7px 0 0;
  color: var(--text-3);
  font-size: 12.5px;
}

.header-tools {
  display: flex;
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

.action-row {
  display: flex;
  flex-wrap: wrap;
  justify-content: flex-end;
  gap: 8px;
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

.fact-list dd.is-code,
.fingerprint code {
  font-family: var(--font-code);
  font-size: 12px;
}

.text-link {
  color: var(--accent);
  text-decoration: none;
}

.text-link:hover {
  text-decoration: underline;
}

.fingerprint {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-top: 12px;
  padding: 12px 14px;
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

.policy-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.policy-hint {
  margin: 0 0 14px;
  color: var(--text-3);
  font-size: 11.5px;
  line-height: 1.65;
}

.policy-actions {
  display: flex;
  justify-content: flex-end;
}

.loading-surface {
  color: var(--text-3);
  font-size: 13px;
}

@media (max-width: 720px) {
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

  .surface-head {
    align-items: flex-start;
    flex-direction: column;
  }

  .action-row {
    justify-content: flex-start;
  }

  .fact-list > div {
    grid-template-columns: 88px minmax(0, 1fr);
  }

  .policy-grid {
    grid-template-columns: 1fr;
    gap: 0;
  }

  .policy-actions {
    justify-content: flex-start;
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
