<script setup lang="ts">
import { computed, ref } from 'vue'
import { KeyRound, ShieldCheck } from '@lucide/vue'
import { NAlert, NButton, NForm, NFormItem, NInput } from 'naive-ui'
import BrandMark from '../components/BrandMark.vue'
import { api, ApiError, type SetupStatus } from '../api'

const props = defineProps<{
  status: SetupStatus
}>()

const emit = defineEmits<{
  done: [username: string]
}>()

const username = ref('admin')
const password = ref('')
const confirm = ref('')
const token = ref('')
const error = ref('')
const busy = ref(false)

const connectionLabel = computed(() => (props.status.tlsEnabled ? 'HTTPS' : 'HTTP（直连未启用 TLS）'))

async function submit() {
  if (busy.value) {
    return
  }
  error.value = ''
  const name = username.value.trim()
  if (name.length < 3 || name.length > 64) {
    error.value = '用户名长度需在 3 到 64 个字符之间'
    return
  }
  if (password.value.length < 12) {
    error.value = '密码至少需要 12 个字符'
    return
  }
  if (password.value !== confirm.value) {
    error.value = '两次输入的密码不一致'
    return
  }
  if (props.status.requiresToken && token.value.trim() === '') {
    error.value = '请输入初始化口令'
    return
  }

  busy.value = true
  try {
    await api.setup(name, password.value, token.value.trim())
    emit('done', name)
  } catch (err) {
    if (err instanceof ApiError && err.code === 'setup_closed') {
      error.value = '面板已完成初始化，请刷新页面后登录'
    } else {
      error.value = err instanceof Error ? err.message : '初始化失败'
    }
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="auth-shell">
    <section class="auth-card" aria-labelledby="setup-title">
      <header class="auth-brand">
        <BrandMark :size="40" />
        <div>
          <strong>OVPanel</strong>
          <span>首次初始化</span>
        </div>
      </header>

      <div class="auth-heading">
        <span class="eyebrow"><ShieldCheck :size="14" /> 创建管理员</span>
        <h1 id="setup-title">设置面板的唯一管理员</h1>
        <p>初始化成功后，这个入口会永久关闭。密码只保存为哈希，无法找回明文。</p>
      </div>

      <NAlert v-if="error" type="error" :bordered="false" class="stack-gap">{{ error }}</NAlert>

      <div v-if="status.requiresToken" class="token-block">
        <div class="token-title"><KeyRound :size="15" /> 需要初始化口令</div>
        <p>
          当前请求不是本机直连。可在面板容器内读取
          <code>/data/bootstrap-token</code>，或使用部署时注入的
          <code>OVPANEL_BOOTSTRAP_TOKEN</code>。
        </p>
      </div>

      <NForm class="auth-form">
        <NFormItem label="管理员用户名" path="username">
          <NInput
            v-model:value="username"
            placeholder="admin"
            maxlength="64"
            autocomplete="username"
            @keyup.enter="submit"
          />
        </NFormItem>
        <NFormItem label="密码" path="password">
          <NInput
            v-model:value="password"
            type="password"
            show-password-on="click"
            placeholder="至少 12 个字符"
            autocomplete="new-password"
            @keyup.enter="submit"
          />
        </NFormItem>
        <NFormItem label="确认密码" path="confirm">
          <NInput
            v-model:value="confirm"
            type="password"
            show-password-on="click"
            placeholder="再次输入密码"
            autocomplete="new-password"
            @keyup.enter="submit"
          />
        </NFormItem>
        <NFormItem v-if="status.requiresToken" label="初始化口令" path="token">
          <NInput
            v-model:value="token"
            type="password"
            show-password-on="click"
            placeholder="读取 bootstrap-token 文件"
            autocomplete="off"
            @keyup.enter="submit"
          />
        </NFormItem>

        <NButton type="primary" size="large" block :loading="busy" :disabled="busy" @click="submit">
          创建并进入面板
        </NButton>
      </NForm>

      <footer class="auth-foot">
        <span>连接：{{ connectionLabel }}</span>
      </footer>
    </section>
  </main>
</template>

<style scoped>
.token-block {
  margin: 18px 0 4px;
  padding: 14px 16px;
  border: 1px solid var(--warn-border);
  border-radius: 10px;
  background: var(--warn-soft);
  color: var(--warn);
}

.token-title {
  display: flex;
  align-items: center;
  gap: 7px;
  font-size: 13px;
  font-weight: 650;
}

.token-block p {
  margin: 7px 0 0;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--text-2);
}

.token-block code {
  padding: 1px 5px;
  border-radius: 4px;
  background: var(--surface);
  color: var(--text-1);
  font-size: 12px;
}
</style>
