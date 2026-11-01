<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RouterView } from 'vue-router'
import { NButton, NConfigProvider, darkTheme } from 'naive-ui'
import BrandMark from './components/BrandMark.vue'
import ThemeToggle from './components/ThemeToggle.vue'
import AppShell from './components/AppShell.vue'
import LoginView from './views/LoginView.vue'
import SetupView from './views/SetupView.vue'
import { api, ApiError, type SetupStatus } from './api'
import { registerUnauthorizedHandler } from './session'
import { resolvedTheme, themeOverrides } from './theme'

type Phase = 'boot' | 'setup' | 'login' | 'app'

const phase = ref<Phase>('boot')
const setupStatus = ref<SetupStatus | null>(null)
const username = ref('')
const bootError = ref('')
const loginNotice = ref('')

const naiveTheme = computed(() => (resolvedTheme.value === 'dark' ? darkTheme : null))
const overrides = computed(() => themeOverrides(resolvedTheme.value === 'dark'))

async function bootstrap() {
  phase.value = 'boot'
  bootError.value = ''
  try {
    const status = await api.setupStatus()
    setupStatus.value = status
    if (!status.initialized) {
      phase.value = 'setup'
      return
    }

    try {
      const admin = await api.me()
      username.value = admin.username
      phase.value = 'app'
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        phase.value = 'login'
        return
      }
      throw err
    }
  } catch (err) {
    bootError.value = err instanceof Error ? err.message : '无法连接面板服务'
  }
}

function handleSetupDone(name: string) {
  username.value = name
  loginNotice.value = ''
  phase.value = 'app'
}

function handleLoginDone(name: string) {
  username.value = name
  loginNotice.value = ''
  phase.value = 'app'
}

function handleLogout() {
  username.value = ''
  loginNotice.value = ''
  phase.value = 'login'
}

function handleExpired() {
  username.value = ''
  loginNotice.value = '会话已过期，请重新登录'
  phase.value = 'login'
}

registerUnauthorizedHandler(handleExpired)

// Naive UI 的密码可见性切换按钮没有可访问名称，按输入框类型补齐。
// 组件在弹层中动态创建，因此用 MutationObserver 覆盖后续节点。
function labelInputActions(root: ParentNode) {
  root.querySelectorAll<HTMLButtonElement>('.n-input__suffix .n-button').forEach((button) => {
    if (button.getAttribute('aria-label')) {
      return
    }
    const input = button.closest('.n-input')?.querySelector('input')
    button.setAttribute('aria-label', input?.type === 'password' ? '显示或隐藏密码' : '输入框操作')
  })
}

let observer: MutationObserver | undefined

onMounted(() => {
  bootstrap()
  labelInputActions(document)
  observer = new MutationObserver((mutations) => {
    for (const mutation of mutations) {
      mutation.addedNodes.forEach((node) => {
        if (node instanceof Element) {
          labelInputActions(node)
          if (node.parentElement) {
            labelInputActions(node.parentElement)
          }
        }
      })
    }
  })
  observer.observe(document.body, { childList: true, subtree: true })
})

onBeforeUnmount(() => observer?.disconnect())
</script>

<template>
  <NConfigProvider :theme="naiveTheme" :theme-overrides="overrides">
    <div class="app-root">
      <ThemeToggle v-if="phase !== 'app'" class="corner-theme" />

      <main v-if="phase === 'boot'" class="auth-shell">
        <section class="boot-card" aria-live="polite">
          <BrandMark :size="44" />
          <div v-if="!bootError" class="boot-loading">
            <strong>正在连接面板</strong>
            <span class="boot-bar" aria-hidden="true" />
          </div>
          <div v-else class="boot-error">
            <h1>无法连接面板</h1>
            <p>{{ bootError }}</p>
            <NButton type="primary" @click="bootstrap">重试</NButton>
          </div>
        </section>
      </main>

      <SetupView
        v-else-if="phase === 'setup' && setupStatus"
        :status="setupStatus"
        @done="handleSetupDone"
      />

      <LoginView v-else-if="phase === 'login'" :notice="loginNotice" @done="handleLoginDone" />

      <AppShell v-else-if="phase === 'app'" :username="username" @logout="handleLogout">
        <RouterView />
      </AppShell>
    </div>
  </NConfigProvider>
</template>

<style scoped>
.corner-theme {
  position: fixed;
  top: 18px;
  right: 18px;
  z-index: 20;
}

.boot-card {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 18px;
  width: min(380px, 100%);
  padding: 36px 28px;
  border: 1px solid var(--border);
  border-radius: 16px;
  background: var(--surface);
  box-shadow: var(--shadow-card);
  text-align: center;
}

.boot-loading {
  display: flex;
  flex-direction: column;
  gap: 10px;
  width: 100%;
  color: var(--text-2);
  font-size: 13px;
}

.boot-bar {
  position: relative;
  display: block;
  height: 3px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--surface-2);
}

.boot-bar::after {
  position: absolute;
  inset: 0 auto 0 0;
  width: 42%;
  border-radius: inherit;
  background: var(--accent);
  animation: boot-slide 1.2s ease-in-out infinite;
  content: '';
}

.boot-error h1 {
  margin: 0 0 8px;
  font-size: 20px;
}

.boot-error p {
  margin: 0 0 16px;
  color: var(--text-3);
  font-size: 13px;
  overflow-wrap: anywhere;
}

@keyframes boot-slide {
  0% {
    transform: translateX(-100%);
  }
  100% {
    transform: translateX(260%);
  }
}

@media (prefers-reduced-motion: reduce) {
  .boot-bar::after {
    animation: none;
    width: 100%;
  }
}

@media (max-width: 640px) {
  .corner-theme {
    top: 12px;
    right: 12px;
  }
}
</style>
