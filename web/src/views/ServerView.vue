<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { Play, RefreshCw, RotateCw, Server, ShieldCheck, Square } from '@lucide/vue'
import { NAlert, NButton, NForm, NFormItem, NInput, NInputNumber, NSelect, NSwitch } from 'naive-ui'
import PasswordPromptModal from '../components/PasswordPromptModal.vue'
import { api, ApiError, type ServerConfig, type ServerConfigResponse } from '../api'
import { errorMessage, formatDate } from '../labels'

const meta = ref<ServerConfigResponse | null>(null)
const form = reactive<ServerConfig>({
  vpnNetwork: '10.8.0.0/16',
  protocol: 'udp',
  port: 1194,
  dns: [],
  routeMode: 'split',
  allowedInternal: [],
  allowInterClient: false,
  blockIpv6: true,
  extraDirectives: [],
})
const dnsText = ref('')
const internalText = ref('')
const extrasText = ref('')
const rendered = ref('')
const renderedHash = ref('')
const loading = ref(false)
const busy = ref(false)
const error = ref('')
const success = ref('')
type PromptMode = 'apply' | 'start' | 'stop' | 'restart'
const prompt = ref({ show: false, loading: false, error: '', mode: 'apply' as PromptMode })

const promptMeta = computed(() => {
  switch (prompt.value.mode) {
    case 'start':
      return { title: '启动 OpenVPN', description: '使用当前生效配置启动 OpenVPN。', actionLabel: '启动' }
    case 'stop':
      return { title: '停止 OpenVPN', description: '停止后全部客户端立即断开，直到再次启动。', actionLabel: '停止' }
    case 'restart':
      return { title: '重启 OpenVPN', description: '重启会断开全部现有连接，配置不变。', actionLabel: '重启' }
    default:
      return {
        title: '应用服务端配置',
        description: '应用会下发新配置并可能重启 OpenVPN，现有连接会中断；失败时自动回退上一版本。',
        actionLabel: '应用配置',
      }
  }
})

const protocolOptions = [
  { label: 'UDP', value: 'udp' },
  { label: 'TCP', value: 'tcp' },
]
const routeOptions = [
  { label: '内网分流', value: 'split' },
  { label: '全部 IPv4 流量经过 VPN', value: 'global' },
]

function applyToForm(config: ServerConfig) {
  Object.assign(form, config)
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
    renderedHash.value = data.draft?.hash ?? data.active?.hash ?? ''
  } catch (err) {
    if (!(err instanceof ApiError && err.status === 401)) error.value = errorMessage(err)
  } finally {
    loading.value = false
  }
}

async function preview() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  success.value = ''
  try {
    const result = await api.previewServerConfig(collectConfig())
    rendered.value = result.rendered
    renderedHash.value = result.hash
    success.value = '配置校验通过。'
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    busy.value = false
  }
}

async function saveDraft() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  success.value = ''
  try {
    await api.saveServerDraft(collectConfig())
    success.value = '草稿已保存。'
    await load()
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    busy.value = false
  }
}

function askApply() {
  prompt.value = { show: true, loading: false, error: '', mode: 'apply' }
}

function askCommand(action: 'start' | 'stop' | 'restart') {
  prompt.value = { show: true, loading: false, error: '', mode: action }
}

async function confirmPrompt(password: string) {
  const mode = prompt.value.mode
  prompt.value.loading = true
  prompt.value.error = ''
  try {
    if (mode === 'apply') {
      await api.applyServerConfig(password)
      success.value = '配置已下发，等待运行代理确认。'
    } else {
      await api.serverCommand(mode, password)
      success.value =
        mode === 'stop' ? '已发送停止命令。' : mode === 'start' ? '已发送启动命令。' : '已发送重启命令。'
    }
    prompt.value.show = false
    window.setTimeout(load, 1500)
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
        <p class="page-meta">
          生效修订 r{{ meta?.active?.revision ?? '—' }}
          · 草稿 r{{ meta?.draft?.revision ?? '—' }}
        </p>
      </div>
      <NButton quaternary circle :loading="loading" aria-label="刷新" title="刷新" @click="load">
        <template #icon><RefreshCw :size="17" /></template>
      </NButton>
    </header>

    <NAlert v-if="error" type="error" :bordered="false" closable class="stack-gap" @close="error = ''">
      {{ error }}
    </NAlert>
    <NAlert v-if="success" type="success" :bordered="false" closable class="stack-gap" @close="success = ''">
      {{ success }}
    </NAlert>

    <div class="server-grid">
      <section class="surface">
        <header class="surface-head">
          <h2>网络与路由</h2>
          <span class="surface-meta">{{ renderedHash ? renderedHash.slice(0, 12) : '未渲染' }}</span>
        </header>
        <NForm>
          <NFormItem label="VPN 网段" required>
            <NInput v-model:value="form.vpnNetwork" placeholder="10.8.0.0/16" />
          </NFormItem>
          <div class="two-col">
            <NFormItem label="协议" required>
              <NSelect v-model:value="form.protocol" :options="protocolOptions" />
            </NFormItem>
            <NFormItem label="端口" required>
              <NInputNumber v-model:value="form.port" :min="1" :max="65535" />
            </NFormItem>
          </div>
          <NFormItem label="DNS">
            <NInput v-model:value="dnsText" placeholder="用逗号分隔，可留空" />
          </NFormItem>
          <NFormItem label="路由方式" required>
            <NSelect v-model:value="form.routeMode" :options="routeOptions" />
          </NFormItem>
          <NFormItem label="授权内网网段">
            <NInput
              v-model:value="internalText"
              type="textarea"
              :autosize="{ minRows: 2, maxRows: 4 }"
              placeholder="例如 192.168.99.0/24，用逗号或换行分隔"
            />
          </NFormItem>
          <div class="switch-row">
            <div>
              <strong>允许客户端互访</strong>
              <span>默认关闭；内网权限与互访分别控制。</span>
            </div>
            <NSwitch v-model:value="form.allowInterClient" aria-label="允许客户端互访" />
          </div>
          <div class="switch-row">
            <div>
              <strong>阻断 IPv6</strong>
              <span>第一版不转发 IPv6，按兼容客户端执行阻断。</span>
            </div>
            <NSwitch v-model:value="form.blockIpv6" aria-label="阻断 IPv6" />
          </div>
          <NFormItem label="额外指令">
            <NInput
              v-model:value="extrasText"
              type="textarea"
              :autosize="{ minRows: 3, maxRows: 6 }"
              placeholder="每行一条；只允许兼容清单中的整数参数指令"
            />
          </NFormItem>
        </NForm>

        <div class="action-row">
          <NButton :loading="busy" @click="preview">校验并预览</NButton>
          <NButton :loading="busy" @click="saveDraft">保存草稿</NButton>
          <NButton type="primary" :disabled="!meta?.draft" :loading="busy" @click="askApply">
            应用草稿
          </NButton>
        </div>
      </section>

      <section class="surface">
        <header class="surface-head">
          <h2>生效与代理状态</h2>
          <span class="surface-meta">{{ meta?.agentStatusPresent ? '代理已回传' : '等待代理' }}</span>
        </header>
        <dl class="fact-list">
          <div>
            <dt>生效修订</dt>
            <dd>r{{ meta?.active?.revision ?? '—' }} · {{ meta?.active?.hash?.slice(0, 12) || '—' }}</dd>
          </div>
          <div>
            <dt>草稿修订</dt>
            <dd>r{{ meta?.draft?.revision ?? '—' }} · {{ meta?.draft?.hash?.slice(0, 12) || '—' }}</dd>
          </div>
          <div>
            <dt>OpenVPN</dt>
            <dd>
              {{
                meta?.agentStatus?.stopped
                  ? '已停止（管理员）'
                  : meta?.agentStatus?.openvpnRunning
                    ? '运行中'
                    : '未运行'
              }}
            </dd>
          </div>
          <div>
            <dt>管理接口</dt>
            <dd>{{ meta?.agentStatus?.managementConnected ? '已连接' : '未连接' }}</dd>
          </div>
          <div>
            <dt>在线会话</dt>
            <dd>{{ meta?.agentStatus?.sessions ?? 0 }}</dd>
          </div>
          <div>
            <dt>实际版本一致</dt>
            <dd>
              <span class="chip" :class="agentHashMatches() ? 'chip--ok' : 'chip--warn'">
                {{ agentHashMatches() ? '一致' : '未确认' }}
              </span>
            </dd>
          </div>
        </dl>
        <div class="action-row command-row">
          <NButton :disabled="!meta?.agentStatus" @click="askCommand('start')">
            <template #icon><Play :size="15" /></template>
            启动
          </NButton>
          <NButton :disabled="!meta?.agentStatus?.openvpnRunning" @click="askCommand('stop')">
            <template #icon><Square :size="15" /></template>
            停止
          </NButton>
          <NButton :disabled="!meta?.agentStatus?.openvpnRunning" @click="askCommand('restart')">
            <template #icon><RotateCw :size="15" /></template>
            重启
          </NButton>
        </div>
        <NAlert v-if="meta?.agentStatus?.rolledBack" type="warning" :bordered="false" class="stack-gap">
          新配置应用失败，已回退到上一版本：{{ meta.agentStatus.rollbackReason }}
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
          兼容指令：{{ meta?.supportedDirectives.join('、') || '—' }}
        </p>
      </section>
    </div>

    <section class="surface">
      <header class="surface-head">
        <h2><ShieldCheck :size="16" /> 配置预览</h2>
        <span class="surface-meta">{{ formatDate(meta?.draft?.createdAt || meta?.active?.createdAt) }}</span>
      </header>
      <pre class="config-preview" tabindex="0">{{ rendered || '保存或预览草稿后显示渲染结果' }}</pre>
    </section>

    <PasswordPromptModal
      v-model:show="prompt.show"
      :title="promptMeta.title"
      :description="promptMeta.description"
      :action-label="promptMeta.actionLabel"
      :loading="prompt.loading"
      :error="prompt.error"
      @confirm="confirmPrompt"
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

.two-col {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 12px;
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
