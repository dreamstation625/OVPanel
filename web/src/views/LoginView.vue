<script setup lang="ts">
import { ref } from 'vue'
import { LogIn } from '@lucide/vue'
import { NAlert, NButton, NForm, NFormItem, NInput } from 'naive-ui'
import BrandMark from '../components/BrandMark.vue'
import { api, ApiError } from '../api'

defineProps<{
  notice?: string
}>()

const emit = defineEmits<{
  done: [username: string]
}>()

const username = ref('admin')
const password = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  if (busy.value) {
    return
  }
  error.value = ''

  busy.value = true
  try {
    const result = await api.login(username.value.trim(), password.value)
    emit('done', result.username)
  } catch (err) {
    if (err instanceof ApiError && err.code === 'rate_limited') {
      error.value = err.message
    } else if (err instanceof ApiError && err.code === 'setup_required') {
      error.value = '面板尚未初始化，请刷新页面'
    } else {
      error.value = err instanceof Error ? err.message : '登录失败'
    }
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main class="auth-shell">
    <section class="auth-card" aria-labelledby="login-title">
      <header class="auth-brand">
        <BrandMark :size="40" />
        <div>
          <strong>OVPanel</strong>
          <span>管理员登录</span>
        </div>
      </header>

      <div class="auth-heading">
        <span class="eyebrow"><LogIn :size="14" /> 管理员</span>
        <h1 id="login-title">进入管理面板</h1>
        <p>管理员账号与 VPN 用户账号分开。</p>
      </div>

      <NAlert v-if="notice" type="warning" :bordered="false" class="stack-gap">{{ notice }}</NAlert>
      <NAlert v-if="error" type="error" :bordered="false" class="stack-gap">{{ error }}</NAlert>

      <NForm class="auth-form">
        <NFormItem label="用户名" path="username">
          <NInput
            v-model:value="username"
            placeholder="admin"
            autocomplete="username"
            @keyup.enter="submit"
          />
        </NFormItem>
        <NFormItem label="密码" path="password">
          <NInput
            v-model:value="password"
            type="password"
            show-password-on="click"
            placeholder="管理员密码"
            autocomplete="current-password"
            @keyup.enter="submit"
          />
        </NFormItem>

        <NButton type="primary" size="large" block :loading="busy" :disabled="busy" @click="submit">
          登录
        </NButton>
      </NForm>
    </section>
  </main>
</template>
