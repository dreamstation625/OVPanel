<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ArrowLeft, Copy, Download, Gauge, KeyRound, RefreshCw, ShieldOff, ShieldCheck } from '@lucide/vue'
import {
  NAlert,
  NButton,
  NDatePicker,
  NForm,
  NFormItem,
  NInput,
  NInputNumber,
  NPopconfirm,
  NSelect,
  NSwitch,
} from 'naive-ui'
import PasswordPromptModal from '../components/PasswordPromptModal.vue'
import OneTimePasswordModal from '../components/OneTimePasswordModal.vue'
import { api, ApiError, type AccessMode, type Profile, type RouteMode } from '../api'
import {
  authLabel,
  authRequiresCertificate,
  authRequiresPassword,
  bpsToMbps,
  bytesToGb,
  errorMessage,
  formatBytes,
  formatDate,
  formatFingerprint,
  gbToBytes,
  mbpsToBps,
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
  fixedVpnIp: '',
})
const policyBusy = ref(false)
const limits = reactive({
  expiresAt: null as number | null,
  concurrentEnabled: false,
  concurrentLimit: 3,
  quotaEnabled: false,
  quotaGb: 10,
  uploadEnabled: false,
  uploadMbps: 10,
  downloadEnabled: false,
  downloadMbps: 50,
})
const limitsBusy = ref(false)
const routeOptions = [
  { label: '继承服务端', value: 'inherit' },
  { label: '仅内网流量经过 VPN', value: 'split' },
  { label: '全部 IPv4 流量经过 VPN', value: 'global' },
]
const accessOptions = [
  { label: '继承服务端', value: 'inherit' },
  { label: '仅允许指定网段', value: 'allow' },
  { label: '禁止访问内网', value: 'deny' },
]

const backTarget = computed(() => (profile.value ? `/users/${profile.value.userId}` : '/users'))
const hasCertificate = computed(() => Boolean(profile.value?.certificate))
const fixedIPActive = computed(() => Boolean(profile.value?.fixedVpnIp))
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
    '文件包含客户端私钥和加密密钥，请通过安全方式交给使用者，不要公开转发。',
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
    '更换客户端证书',
    '旧证书会立即失效，所有使用旧配置的设备都需要重新下载 .ovpn。',
    '更换证书',
    async (password) => {
      const result = await api.rotateCertificate(id, password)
      profile.value = result.profile
      success.value = '证书已更换。'
    },
  )
}

function revokeCertificate() {
  if (!profile.value) {
    return
  }
  const id = profile.value.id
  openPrompt(
    '作废客户端证书',
    '作废后无法恢复。该配置将无法连接，需要重新签发证书。',
    '作废证书',
    async (password) => {
      const result = await api.revokeCertificate(id, password)
      profile.value = result.profile
      success.value = '证书已作废。'
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
      fixedVpnIp: policy.fixedVpnIp.trim(),
    })
    success.value = '网络策略已保存；路由和固定 IP 在客户端重新连接后生效。'
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    policyBusy.value = false
  }
}

async function saveLimits() {
  const current = profile.value
  if (!current || limitsBusy.value) {
    return
  }
  if (limits.concurrentEnabled && (limits.concurrentLimit < 1 || limits.concurrentLimit > 1000)) {
    error.value = '并发上限需在 1 到 1000 之间'
    return
  }
  if (limits.quotaEnabled && limits.quotaGb < 0.001) {
    error.value = '流量额度至少为 0.001 GB'
    return
  }
  if (limits.uploadEnabled && (limits.uploadMbps < 0.008 || limits.uploadMbps > 10000)) {
    error.value = '上传限速需在 0.008 到 10000 Mbps 之间'
    return
  }
  if (limits.downloadEnabled && (limits.downloadMbps < 0.008 || limits.downloadMbps > 10000)) {
    error.value = '下载限速需在 0.008 到 10000 Mbps 之间'
    return
  }

  limitsBusy.value = true
  error.value = ''
  success.value = ''
  try {
    const result = await api.updateProfileLimits(current.id, {
      expiresAt: limits.expiresAt ? new Date(limits.expiresAt).toISOString() : '',
      concurrentLimit: fixedIPActive.value ? 1 : limits.concurrentEnabled ? Math.round(limits.concurrentLimit) : 0,
      quotaBytes: limits.quotaEnabled ? gbToBytes(limits.quotaGb) : 0,
      uploadBps: limits.uploadEnabled ? mbpsToBps(limits.uploadMbps) : 0,
      downloadBps: limits.downloadEnabled ? mbpsToBps(limits.downloadMbps) : 0,
    })
    profile.value = result.profile
    success.value = '使用限制已保存并生效。'
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    limitsBusy.value = false
  }
}

function resetQuota() {
  const current = profile.value
  if (!current) {
    return
  }
  openPrompt(
    '重置流量额度',
    '重置后从当前用量重新计算；历史连接和统计不会删除，在线连接不会断开。',
    '重置额度',
    async (password) => {
      const result = await api.resetQuota(current.id, password)
      profile.value = result.profile
      success.value = '流量额度已重置，并从当前用量重新计算。'
      // VPN 服务按秒读取新配置；短暂延迟后静默刷新，显示新的用量基线。
      window.setTimeout(async () => {
        try {
          const fresh = await api.getProfile(current.id)
          if (profile.value?.id === current.id) {
            profile.value = fresh
          }
        } catch {
          // 保留当前页面，下一次手动刷新仍可获取新状态。
        }
      }, 2000)
    },
  )
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
    policy.fixedVpnIp = value.fixedVpnIp ?? ''
    limits.expiresAt = value.expiresAt ? new Date(value.expiresAt).getTime() : null
    limits.concurrentEnabled = value.concurrentLimit > 0
    limits.concurrentLimit = value.concurrentLimit > 0 ? value.concurrentLimit : 3
    limits.quotaEnabled = value.quotaBytes > 0
    limits.quotaGb = value.quotaBytes > 0 ? bytesToGb(value.quotaBytes) : 10
    limits.uploadEnabled = value.uploadBps > 0
    limits.uploadMbps = value.uploadBps > 0 ? bpsToMbps(value.uploadBps) : 10
    limits.downloadEnabled = value.downloadBps > 0
    limits.downloadMbps = value.downloadBps > 0 ? bpsToMbps(value.downloadBps) : 50
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
        {{ profile.statusReason }}。VPN 服务会拒绝新连接，并断开已有连接。
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
              {{ hasCertificate ? '更换证书' : '签发新证书' }}
            </NButton>
            <NButton v-if="hasCertificate" size="small" type="error" ghost @click="revokeCertificate">
              <template #icon><ShieldOff :size="14" /></template>
              作废证书
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
            <dt>到期时间</dt>
            <dd>{{ formatDate(profile.expiresAt) }}</dd>
          </div>
        </dl>
      </section>

      <section class="surface">
        <header class="surface-head">
          <h2>网络策略</h2>
          <span class="surface-meta">分别设置路由和内网访问权限</span>
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
          <NFormItem v-if="policy.accessMode === 'allow'" label="可访问内网网段">
            <NInput
              v-model:value="policy.allowedText"
              type="textarea"
              :autosize="{ minRows: 2, maxRows: 4 }"
              placeholder="例如 10.0.16.0/22，用逗号或换行分隔"
            />
          </NFormItem>
          <NFormItem label="固定 VPN 地址">
            <NInput
              v-model:value="policy.fixedVpnIp"
              placeholder="留空自动分配；填写 VPN 网段末尾保留段内的地址"
            />
          </NFormItem>
          <p class="policy-hint">
            路由方式决定哪些流量经过 VPN；内网权限决定能访问哪些内网地址。固定 IP 使用网段末尾的保留段<template v-if="profile.fixedIpRangeStart">（{{ profile.fixedIpRangeStart }} 至 {{ profile.fixedIpRangeEnd }}）</template>，保存后并发上限固定为 1，客户端重新连接后生效；首次使用或修改 VPN 网段后，请到“服务端”应用一次配置。
          </p>
          <div class="policy-actions">
            <NButton type="primary" :loading="policyBusy" @click="savePolicy">保存网络策略</NButton>
          </div>
        </NForm>
      </section>

      <section class="surface">
        <header class="surface-head">
          <h2>使用限制</h2>
          <span class="surface-meta">{{ fixedIPActive ? '已设置固定 IP，并发上限固定为 1' : '由 VPN 服务执行；0 或关闭表示不限制' }}</span>
        </header>

        <div class="usage-row">
          <div class="usage-main">
            <span class="usage-label">当前用量周期</span>
            <strong>{{ profile.usageKnown ? formatBytes(profile.usageBytes) : '—' }}</strong>
            <span class="usage-total">
              / {{ profile.quotaBytes > 0 ? formatBytes(profile.quotaBytes) : '不限量' }}
            </span>
          </div>
          <div class="usage-side">
            <span v-if="profile.usageKnown">在线 {{ profile.onlineSessions }} 条</span>
            <span v-else>暂时无法获取用量</span>
            <span v-if="profile.quotaResetAt">上次重置 {{ formatDate(profile.quotaResetAt) }}</span>
            <span v-if="profile.usageGaps > 0" class="usage-gap">
              统计中断 {{ profile.usageGaps }} 次，期间流量可能少计
            </span>
          </div>
        </div>
        <div
          v-if="profile.quotaBytes > 0 && profile.usageKnown"
          class="usage-bar"
          role="progressbar"
          :aria-valuenow="Math.min(100, Math.round(profile.usagePercent))"
          aria-valuemin="0"
          aria-valuemax="100"
          :aria-label="`流量额度已使用 ${Math.min(100, Math.round(profile.usagePercent))}%`"
        >
          <i :class="{ danger: profile.usagePercent >= 90 }" :style="{ width: `${Math.min(100, profile.usagePercent)}%` }" />
        </div>

        <NForm class="limits-form">
          <NFormItem label="到期时间">
            <NDatePicker
              v-model:value="limits.expiresAt"
              type="datetime"
              clearable
              placeholder="不设到期时间"
              class="control-wide"
            />
          </NFormItem>
          <div class="limit-line">
            <NSwitch
              v-model:value="limits.concurrentEnabled"
              aria-label="并发上限开关"
              :disabled="fixedIPActive"
            />
            <span class="limit-name">并发上限</span>
            <NInputNumber
              v-model:value="limits.concurrentLimit"
              :min="1"
              :max="1000"
              :disabled="fixedIPActive || !limits.concurrentEnabled"
              :show-button="false"
              class="control-number"
            />
            <span class="limit-unit">条</span>
          </div>
          <div class="limit-line">
            <NSwitch v-model:value="limits.quotaEnabled" aria-label="流量额度开关" />
            <span class="limit-name">流量额度</span>
            <NInputNumber
              v-model:value="limits.quotaGb"
              :min="0.001"
              :max="1000000"
              :step="1"
              :disabled="!limits.quotaEnabled"
              :show-button="false"
              class="control-number"
            />
            <span class="limit-unit">GB</span>
            <NButton
              v-if="profile.quotaBytes > 0"
              size="small"
              type="warning"
              ghost
              class="limit-reset"
              @click="resetQuota"
            >
              重置额度
            </NButton>
          </div>
          <div class="limit-line">
            <NSwitch v-model:value="limits.uploadEnabled" aria-label="上传限速开关" />
            <span class="limit-name">上传限速</span>
            <NInputNumber
              v-model:value="limits.uploadMbps"
              :min="0.008"
              :max="10000"
              :step="1"
              :disabled="!limits.uploadEnabled"
              :show-button="false"
              class="control-number"
            />
            <span class="limit-unit">Mbps / 每条连接</span>
          </div>
          <div class="limit-line">
            <NSwitch v-model:value="limits.downloadEnabled" aria-label="下载限速开关" />
            <span class="limit-name">下载限速</span>
            <NInputNumber
              v-model:value="limits.downloadMbps"
              :min="0.008"
              :max="10000"
              :step="1"
              :disabled="!limits.downloadEnabled"
              :show-button="false"
              class="control-number"
            />
            <span class="limit-unit">Mbps / 每条连接</span>
          </div>
          <div class="limits-actions">
            <NButton type="primary" :loading="limitsBusy" @click="saveLimits">
              <template #icon><Gauge :size="15" /></template>
              保存使用限制
            </NButton>
          </div>
        </NForm>
      </section>

      <section v-if="authRequiresCertificate(profile.authType)" class="surface">
        <header class="surface-head">
          <h2>客户端证书</h2>
          <span class="surface-meta">{{ profile.certificate ? '当前有效' : '没有有效证书' }}</span>
        </header>

        <template v-if="profile.certificate">
          <dl class="fact-list">
            <div>
              <dt>证书编号</dt>
              <dd class="is-code">{{ profile.certificate.serialNumber }}</dd>
            </div>
            <div>
              <dt>证书名称</dt>
              <dd class="is-code">{{ profile.certificate.commonName }}</dd>
            </div>
            <div>
              <dt>有效期</dt>
              <dd>{{ formatDate(profile.certificate.issuedAt) }} 至 {{ formatDate(profile.certificate.expiresAt) }}</dd>
            </div>
          </dl>
          <div class="fingerprint">
            <span>证书指纹</span>
            <code tabindex="0">{{ formatFingerprint(profile.certificate.fingerprint) }}</code>
            <NButton size="tiny" @click="copyFingerprint">
              <template #icon><Copy :size="13" /></template>
              {{ copied ? '已复制' : '复制' }}
            </NButton>
          </div>
        </template>
        <NAlert v-else type="error" :bordered="false">
          没有有效证书。请先签发新证书，再下载配置。
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

.usage-row {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 8px;
}

.usage-main {
  display: flex;
  align-items: baseline;
  gap: 7px;
  flex-wrap: wrap;
}

.usage-label {
  color: var(--text-3);
  font-size: 12px;
}

.usage-main strong {
  font-size: 22px;
  font-weight: 660;
  font-variant-numeric: tabular-nums;
  letter-spacing: -0.02em;
}

.usage-total {
  color: var(--text-3);
  font-size: 12.5px;
}

.usage-side {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: 3px;
  color: var(--text-3);
  font-size: 11.5px;
}

.usage-gap {
  color: var(--warn);
}

.usage-bar {
  height: 6px;
  margin-bottom: 18px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--surface-2);
}

.usage-bar i {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: var(--accent);
  transition: width 240ms ease;
}

.usage-bar i.danger {
  background: var(--danger);
}

.limits-form {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.limit-line {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  min-height: 44px;
  padding: 4px 0;
  border-top: 1px solid var(--border);
}

.limit-line:first-of-type {
  border-top: 0;
}

.limit-name {
  min-width: 74px;
  color: var(--text-2);
  font-size: 13px;
}

.limit-unit {
  color: var(--text-3);
  font-size: 11.5px;
}

.limit-reset {
  margin-left: auto;
}

.control-wide {
  width: min(320px, 100%);
}

.control-number {
  width: 132px;
}

.limits-actions {
  display: flex;
  justify-content: flex-end;
  margin-top: 10px;
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

  .usage-row {
    align-items: flex-start;
    flex-direction: column;
    gap: 6px;
  }

  .usage-side {
    align-items: flex-start;
  }

  .limits-actions {
    justify-content: flex-start;
  }

  .limit-reset {
    margin-left: 0;
  }

  .limit-line {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr) auto;
    gap: 6px 10px;
    align-items: center;
    padding: 10px 0;
  }

  .limit-line > :deep(.n-switch) {
    grid-column: 1;
    grid-row: 1;
  }

  .limit-name {
    grid-column: 2;
    grid-row: 1;
    min-width: 0;
  }

  .limit-unit {
    grid-column: 3;
    grid-row: 1;
    text-align: right;
    white-space: nowrap;
  }

  .control-number {
    grid-column: 2 / 4;
    grid-row: 2;
    width: min(220px, 100%);
  }

  .limit-reset {
    grid-column: 2 / 4;
    grid-row: 3;
    justify-self: start;
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
