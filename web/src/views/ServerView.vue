<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { Play, RefreshCw, RotateCw, Server, ShieldCheck, Square } from '@lucide/vue'
import { NAlert, NButton, NForm, NFormItem, NInput, NInputNumber, NSelect, NSwitch } from 'naive-ui'
import ConfirmModal from '../components/ConfirmModal.vue'
import { api, ApiError, type ServerConfig, type ServerConfigResponse } from '../api'
import { errorMessage, formatDate } from '../labels'
import { useNotify } from '../notify'

const notify = useNotify()
const meta = ref<ServerConfigResponse | null>(null)
const form = reactive<ServerConfig>({
  vpnNetwork: '10.8.0.0/16',
  protocol: 'udp',
  port: 1194,
  dns: ['119.29.29.29'],
  dataCiphers: ['AES-256-GCM', 'AES-128-GCM'],
  authDigest: 'SHA256',
  tlsMinVersion: '1.2',
  keepaliveInterval: 10,
  keepaliveTimeout: 60,
  maxClients: 0,
  routeMode: 'split',
  allowedInternal: [],
  allowClientNetworks: false,
  allowInterClient: false,
  blockIpv6: true,
  extraDirectives: [],
})
const dnsText = ref('119.29.29.29')
const internalText = ref('')
const extrasText = ref('')
const rendered = ref('')
const loading = ref(false)
const busy = ref(false)
const error = ref('')
type PromptMode = 'apply' | 'start' | 'stop' | 'restart'
const prompt = ref({ show: false, loading: false, mode: 'apply' as PromptMode })
const fixedIPPrompt = ref({ show: false, loading: false })

const configState = computed(() => {
  if (!meta.value) {
    return '正在读取配置'
  }
  if (meta.value.draft) {
    return '已保存，待应用'
  }
  return meta.value.active ? '配置已应用' : '尚未保存'
})

const vpnNetworkChanged = computed(() => {
  const active = meta.value?.active?.config.vpnNetwork
  return Boolean(active && form.vpnNetwork.trim() !== active)
})

const fixedIPProfiles = computed(() => meta.value?.fixedIpProfiles ?? [])
const fixedIPPromptText = computed(() => {
  const names = fixedIPProfiles.value.map((item) => `${item.userDisplayName} · ${item.name}`)
  const summary = names.length <= 3 ? names.join('、') : `${names.slice(0, 3).join('、')} 等 ${names.length} 个`
  return `修改 VPN 网段会断开所有客户端并重新分配地址，失败自动回退。以下客户端设置了固定 VPN 地址：${summary}。是否自动改为自动分配并继续？取消后请先在对应客户端配置里清空固定 VPN 地址，再重新应用。`
})

const promptMeta = computed(() => {
  switch (prompt.value.mode) {
    case 'start':
      return { title: '启动 OpenVPN', description: '按当前配置启动。', actionLabel: '启动', danger: false }
    case 'stop':
      return {
        title: '停止 OpenVPN',
        description: '停止后所有客户端会断开，直到再次启动。',
        actionLabel: '停止',
        danger: true,
      }
    case 'restart':
      return {
        title: '重启 OpenVPN',
        description: '重启会断开所有连接，配置不变。',
        actionLabel: '重启',
        danger: true,
      }
    default:
      return {
        title: '应用配置',
        description: vpnNetworkChanged.value
          ? 'VPN 网段已修改：应用会断开所有客户端并重新分配地址；失败自动回退。'
          : '应用可能重启 OpenVPN，连接会短暂中断；失败自动回退。',
        actionLabel: '应用',
        danger: false,
      }
  }
})

const openVPNStatus = computed(() => {
  const agent = meta.value?.agentStatus
  if (!agent) {
    return { label: '等待数据', tone: 'chip--neutral' }
  }
  if (agent.stopped) {
    return { label: '已停止', tone: 'chip--warn' }
  }
  if (agent.openvpnRunning) {
    return { label: '运行中', tone: 'chip--ok' }
  }
  return { label: '未运行', tone: 'chip--danger' }
})

const protocolOptions = [
  { label: 'UDP', value: 'udp' },
  { label: 'TCP', value: 'tcp' },
]
const vpnNetworkHint = '支持私有 /16 至 /24 网段。修改后会断开全部客户端并重新分配地址；不能与宿主现有网段重叠。'
const routeOptions = [
  { label: '仅指定网段经过 VPN', value: 'split' },
  { label: '全部 IPv4 流量经过 VPN', value: 'global' },
]
const accessRange = computed(() => {
  if (form.routeMode === 'global') {
    return {
      description: '全部 IPv4 流量经过 VPN，公网默认可访问；列表只额外放行私有内网。',
      label: '额外放行的私有网段',
      placeholder: '可选；例如 10.0.16.0/22、192.168.99.0/24',
      hint: '未列出的私有网段仍会被拒绝，不要填写 VPN 服务器自身的地址。',
    }
  }
  return {
    description: '只有列表中的网段经过 VPN，并允许访问这些目标。',
    label: '经过 VPN 的目标网段',
    placeholder: '例如 192.168.99.0/24、10.0.16.0/22、1.1.1.1，用逗号或换行分隔',
    hint: '其他流量不经过 VPN，不要填写 VPN 服务器自身的地址。',
  }
})
const cipherOptions = [
  { label: 'AES-256-GCM', value: 'AES-256-GCM' },
  { label: 'AES-128-GCM', value: 'AES-128-GCM' },
  { label: 'CHACHA20-POLY1305', value: 'CHACHA20-POLY1305' },
  { label: 'AES-256-CBC', value: 'AES-256-CBC' },
  { label: 'AES-128-CBC', value: 'AES-128-CBC' },
]
const authOptions = [
  { label: 'SHA256', value: 'SHA256' },
  { label: 'SHA512', value: 'SHA512' },
]
const tlsOptions = [
  { label: 'TLS 1.2', value: '1.2' },
  { label: 'TLS 1.3', value: '1.3' },
]

function applyToForm(config: ServerConfig) {
  Object.assign(form, config)
  form.dataCiphers = config.dataCiphers?.length ? [...config.dataCiphers] : ['AES-256-GCM', 'AES-128-GCM']
  form.authDigest = config.authDigest || 'SHA256'
  form.tlsMinVersion = config.tlsMinVersion || '1.2'
  form.keepaliveInterval = config.keepaliveInterval || 10
  form.keepaliveTimeout = config.keepaliveTimeout || 60
  form.maxClients = config.maxClients ?? 0
  dnsText.value = config.dns.join(', ')
  internalText.value = config.allowedInternal.join(', ')
  extrasText.value = config.extraDirectives.join('\n')
}

function collectConfig(): ServerConfig {
  return {
    ...form,
    dns: dnsText.value.split(/[,，\s]+/).map((v) => v.trim()).filter(Boolean),
    allowedInternal: internalText.value.split(/[,，\s]+/).map((v) => v.trim()).filter(Boolean),
    extraDirectives: extrasText.value.split('\n').map((v) => v.trim()).filter(Boolean),
  }
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const data = await api.getServerConfig()
    meta.value = data
    applyToForm(data.draft?.config ?? data.active?.config ?? data.defaults)
    rendered.value = data.draft?.rendered ?? data.active?.rendered ?? ''
  } catch (err) {
    if (!(err instanceof ApiError && err.status === 401)) error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function preview() {
  if (busy.value) return
  busy.value = true
  try {
    const result = await api.previewServerConfig(collectConfig())
    rendered.value = result.rendered
    notify.success('配置检查通过，预览已更新。')
  } catch (err) {
    notify.error(errorMessage(err))
  } finally {
    busy.value = false
  }
}

async function saveDraft() {
  if (busy.value) return
  busy.value = true
  try {
    await api.saveServerDraft(collectConfig())
    notify.success('配置已保存，尚未应用。')
    await load()
  } catch (err) {
    notify.error(errorMessage(err))
  } finally {
    busy.value = false
  }
}

function askApply() {
  if (vpnNetworkChanged.value && fixedIPProfiles.value.length > 0) {
    fixedIPPrompt.value = { show: true, loading: false }
    return
  }
  prompt.value = { show: true, loading: false, mode: 'apply' }
}

function askCommand(action: 'start' | 'stop' | 'restart') {
  prompt.value = { show: true, loading: false, mode: action }
}

async function confirmPrompt() {
  const mode = prompt.value.mode
  prompt.value.loading = true
  try {
    if (mode === 'apply') {
      await api.applyServerConfig()
      notify.success('配置已应用，等待 VPN 服务确认。')
    } else {
      await api.serverCommand(mode)
      notify.success(mode === 'stop' ? '已发送停止。' : mode === 'start' ? '已发送启动。' : '已发送重启。')
    }
    prompt.value.show = false
    window.setTimeout(load, 1500)
  } catch (err) {
    prompt.value.show = false
    notify.error(errorMessage(err))
  } finally {
    prompt.value.loading = false
  }
}

async function confirmFixedIPReset() {
  if (fixedIPPrompt.value.loading) {
    return
  }
  fixedIPPrompt.value.loading = true
  try {
    await api.applyServerConfig(true)
    notify.success('固定 VPN 地址已改为自动分配，配置已应用，等待 VPN 服务确认。')
    fixedIPPrompt.value.show = false
    window.setTimeout(load, 1500)
  } catch (err) {
    fixedIPPrompt.value.show = false
    notify.error(errorMessage(err))
    await load()
  } finally {
    fixedIPPrompt.value.loading = false
  }
}

function agentHashMatches(): boolean {
  const agent = meta.value?.agentStatus
  const active = meta.value?.active
  return Boolean(agent?.configHash && active?.hash && agent.configHash === active.hash)
}

onMounted(load)
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <span class="eyebrow"><Server :size="14" /> 服务端</span>
        <div class="title-row">
          <h1>OpenVPN 服务端</h1>
          <span
            v-if="meta?.agentStatus"
            class="chip"
            :class="meta.agentStatus.openvpnRunning ? 'chip--ok' : 'chip--warn'"
          >
            {{
              meta.agentStatus.stopped
                ? '已停止'
                : meta.agentStatus.openvpnRunning
                  ? 'OpenVPN 运行中'
                  : 'OpenVPN 未运行'
            }}
          </span>
        </div>
        <p class="page-meta">{{ configState }}</p>
      </div>
      <NButton quaternary circle :loading="loading" aria-label="刷新" title="刷新" @click="load">
        <template #icon><RefreshCw :size="17" /></template>
      </NButton>
    </header>

    <NAlert v-if="error" type="error" :bordered="false" closable class="stack-gap" @close="error = ''">
      {{ error }}
    </NAlert>

    <div class="server-grid">
      <section class="surface">
        <header class="surface-head">
          <h2>服务端设置</h2>
        </header>
        <NForm>
          <NFormItem label="VPN 网段" required :feedback="vpnNetworkHint">
            <NInput v-model:value="form.vpnNetwork" placeholder="10.8.0.0/16" />
          </NFormItem>
          <div class="two-col">
            <NFormItem label="传输协议" required>
              <NSelect v-model:value="form.protocol" :options="protocolOptions" />
            </NFormItem>
            <NFormItem label="端口" required>
              <NInputNumber v-model:value="form.port" :min="1" :max="65535" />
            </NFormItem>
          </div>
          <NFormItem label="DNS">
            <NInput v-model:value="dnsText" placeholder="例如 119.29.29.29，多个用逗号分隔" />
          </NFormItem>
          <div class="access-scope">
            <div class="access-scope-head">
              <strong>客户端默认访问范围</strong>
              <span>{{ accessRange.description }}</span>
            </div>
            <NFormItem label="流量路由" required>
              <NSelect v-model:value="form.routeMode" :options="routeOptions" />
            </NFormItem>
            <NFormItem :label="accessRange.label">
              <NInput
                v-model:value="internalText"
                type="textarea"
                :autosize="{ minRows: 2, maxRows: 4 }"
                :placeholder="accessRange.placeholder"
              />
            </NFormItem>
            <p class="access-scope-hint">{{ accessRange.hint }}</p>
          </div>
          <div class="switch-row">
            <div>
              <strong>允许客户端互相访问</strong>
              <span>开启后，连接 VPN 的设备可以互相访问。</span>
            </div>
            <NSwitch v-model:value="form.allowInterClient" aria-label="允许客户端互相访问" />
          </div>
          <div class="switch-row">
            <div>
              <strong>允许访问其他客户端内网</strong>
              <span>继承服务端设置的配置可以访问其他客户端通告的内网；保存应用后生效。</span>
            </div>
            <NSwitch v-model:value="form.allowClientNetworks" aria-label="允许访问其他客户端内网" />
          </div>
          <div class="switch-row">
            <div>
              <strong>阻断 IPv6</strong>
              <span>防止 IPv6 流量绕过 VPN，建议保持开启。</span>
            </div>
            <NSwitch v-model:value="form.blockIpv6" aria-label="阻断 IPv6" />
          </div>
          <h3 class="form-section-title">加密与连接</h3>
          <div class="two-col">
            <NFormItem label="数据加密算法" required>
              <NSelect
                v-model:value="form.dataCiphers"
                multiple
                :options="cipherOptions"
                :max-tag-count="3"
              />
            </NFormItem>
            <NFormItem label="控制通道认证算法" required>
              <NSelect v-model:value="form.authDigest" :options="authOptions" />
            </NFormItem>
          </div>
          <div class="two-col">
            <NFormItem label="TLS 最低版本" required>
              <NSelect v-model:value="form.tlsMinVersion" :options="tlsOptions" />
            </NFormItem>
            <NFormItem label="最大连接数">
              <NInputNumber v-model:value="form.maxClients" :min="0" :max="10000" />
            </NFormItem>
          </div>
          <div class="two-col">
            <NFormItem label="keepalive 间隔（秒）" required>
              <NInputNumber v-model:value="form.keepaliveInterval" :min="1" :max="3600" />
            </NFormItem>
            <NFormItem label="keepalive 超时（秒）" required>
              <NInputNumber v-model:value="form.keepaliveTimeout" :min="1" :max="86400" />
            </NFormItem>
          </div>
          <p class="hint">
            数据加密算法按 AES-256-GCM、AES-128-GCM、CHACHA20-POLY1305、AES-256-CBC、AES-128-CBC 的顺序协商，与勾选顺序无关。
          </p>
          <p class="hint">
            优先使用 GCM/CHACHA20；CBC 仅用于兼容旧客户端。修改后需重新下载客户端 .ovpn 并重连；最大连接数为 0 表示不额外限制。
          </p>
          <NFormItem label="额外指令">
            <NInput
              v-model:value="extrasText"
              type="textarea"
              :autosize="{ minRows: 3, maxRows: 6 }"
              placeholder="每行一条；只能填写下方列出的指令"
            />
          </NFormItem>
        </NForm>

        <div class="action-row">
          <NButton :loading="busy" @click="preview">检查配置</NButton>
          <NButton :loading="busy" @click="saveDraft">保存</NButton>
          <NButton type="primary" :disabled="!meta?.draft" :loading="busy" @click="askApply">
            应用
          </NButton>
        </div>
      </section>

      <section class="surface">
        <header class="surface-head">
          <h2>运行状态</h2>
          <span class="surface-meta">{{ meta?.agentStatusPresent ? '数据已更新' : '等待数据' }}</span>
        </header>
        <dl class="fact-list">
          <div>
            <dt>OpenVPN</dt>
            <dd>
              <span class="chip" :class="openVPNStatus.tone">{{ openVPNStatus.label }}</span>
            </dd>
          </div>
          <div>
            <dt>在线连接</dt>
            <dd>{{ meta?.agentStatus?.sessions ?? 0 }}</dd>
          </div>
          <div>
            <dt>配置状态</dt>
            <dd>
              <span class="chip" :class="agentHashMatches() ? 'chip--ok' : 'chip--warn'">
                {{ agentHashMatches() ? '已生效' : '等待生效' }}
              </span>
            </dd>
          </div>
        </dl>
        <div class="action-row command-row">
          <NButton type="success" :disabled="!meta?.agentStatus" @click="askCommand('start')">
            <template #icon><Play :size="15" /></template>
            启动
          </NButton>
          <NButton type="error" :disabled="!meta?.agentStatus?.openvpnRunning" @click="askCommand('stop')">
            <template #icon><Square :size="15" /></template>
            停止
          </NButton>
          <NButton type="warning" :disabled="!meta?.agentStatus?.openvpnRunning" @click="askCommand('restart')">
            <template #icon><RotateCw :size="15" /></template>
            重启
          </NButton>
        </div>
        <NAlert v-if="meta?.agentStatus?.rolledBack" type="warning" :bordered="false" class="stack-gap">
          新配置应用失败，已恢复到上一版本：{{ meta.agentStatus.rollbackReason }}
        </NAlert>
        <NAlert
          v-else-if="meta?.agentStatus?.lastError"
          type="error"
          :bordered="false"
          class="stack-gap"
        >
          {{ meta.agentStatus.lastError }}
        </NAlert>
        <p class="hint">
          可用的额外指令：{{ meta?.supportedDirectives.join('、') || '—' }}
        </p>
      </section>
    </div>

    <section class="surface">
      <header class="surface-head">
        <h2><ShieldCheck :size="16" /> 配置内容</h2>
        <span class="surface-meta">最近修改 {{ formatDate(meta?.draft?.createdAt || meta?.active?.createdAt) }}</span>
      </header>
      <pre class="config-preview" tabindex="0">{{ rendered || '保存或检查配置后显示内容。' }}</pre>
    </section>

    <ConfirmModal
      v-model:show="prompt.show"
      :title="promptMeta.title"
      :description="promptMeta.description"
      :action-label="promptMeta.actionLabel"
      :loading="prompt.loading"
      :danger="promptMeta.danger"
      @confirm="confirmPrompt"
    />
    <ConfirmModal
      v-model:show="fixedIPPrompt.show"
      title="固定 VPN 地址需要改为自动分配"
      :description="fixedIPPromptText"
      action-label="改为自动并应用"
      :loading="fixedIPPrompt.loading"
      danger
      @confirm="confirmFixedIPReset"
    />
  </div>
</template>

<style scoped>
.page {
  width: min(1100px, 100%);
  margin: 0 auto;
}

.page-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 20px;
}

.title-row {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  margin-top: 6px;
}

.page-header h1 {
  margin: 0;
  font-size: clamp(24px, 3vw, 30px);
  font-weight: 680;
  letter-spacing: -0.02em;
}

.page-meta {
  margin: 7px 0 0;
  color: var(--text-3);
  font-size: 12.5px;
}

.server-grid {
  display: grid;
  grid-template-columns: minmax(0, 1.2fr) minmax(0, 0.8fr);
  gap: 14px;
  align-items: start;
  margin-bottom: 14px;
}

.surface {
  min-width: 0;
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

.form-section-title {
  margin: 18px 0 12px;
  padding-top: 16px;
  border-top: 1px solid var(--border);
  color: var(--text-2);
  font-size: 13px;
  font-weight: 640;
}

.two-col {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
}

.access-scope {
  margin: 4px 0 16px;
  padding: 14px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--surface-2);
}

.access-scope-head {
  display: flex;
  flex-direction: column;
  gap: 3px;
  margin-bottom: 12px;
}

.access-scope-head strong {
  font-size: 13px;
  font-weight: 640;
}

.access-scope-head span {
  color: var(--text-3);
  font-size: 11.5px;
  line-height: 1.55;
}

.access-scope-hint {
  margin: 0;
  color: var(--text-3);
  font-size: 11.5px;
  line-height: 1.6;
}

.switch-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin: 4px 0 14px;
  padding: 11px 13px;
  border: 1px solid var(--border);
  border-radius: 9px;
  background: var(--surface-2);
}

.switch-row div {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.switch-row strong {
  font-size: 13px;
}

.switch-row span {
  color: var(--text-3);
  font-size: 11.5px;
}

.action-row {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.command-row {
  margin-top: 16px;
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

.hint {
  margin: 14px 0 0;
  color: var(--text-3);
  font-size: 11.5px;
  line-height: 1.6;
}

.config-preview {
  max-height: 420px;
  margin: 0;
  padding: 14px;
  overflow: auto;
  border: 1px solid var(--border);
  border-radius: 9px;
  background: var(--surface-2);
  color: var(--text-2);
  font-family: var(--font-code);
  font-size: 12px;
  line-height: 1.6;
  white-space: pre;
}

@media (max-width: 900px) {
  .server-grid {
    grid-template-columns: 1fr;
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
}
</style>
