<script setup lang="ts">
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import {
  Archive,
  LayoutDashboard,
  LogOut,
  Menu,
  Radio,
  ScrollText,
  Server,
  Settings,
  Users,
  X,
} from '@lucide/vue'
import { NButton } from 'naive-ui'
import BrandMark from './BrandMark.vue'
import ThemeToggle from './ThemeToggle.vue'
import { api } from '../api'

defineProps<{
  username: string
}>()

const emit = defineEmits<{
  logout: []
}>()

const route = useRoute()
const menuOpen = ref(false)
const drawer = ref<HTMLElement | null>(null)

const navItems = [
  { to: '/overview', label: '概览', icon: LayoutDashboard },
  { to: '/connections', label: '在线连接', icon: Radio },
  { to: '/users', label: '用户与客户端', icon: Users },
  { to: '/server', label: '服务端', icon: Server },
  { to: '/logs', label: '日志', icon: ScrollText },
  { to: '/backups', label: '备份', icon: Archive },
  { to: '/system', label: '系统', icon: Settings },
]

function isUsersSection() {
  return route.path.startsWith('/users') || route.path.startsWith('/profiles')
}

function setMenu(open: boolean, restoreFocus = false) {
  if (menuOpen.value === open) return
  menuOpen.value = open
  if (open) {
    nextTick(() => drawer.value?.querySelector<HTMLElement>('.nav-link')?.focus())
  } else if (restoreFocus) {
    nextTick(() => document.querySelector<HTMLElement>('.mobile-menu-button')?.focus())
  }
}

function onDrawerKeydown(event: KeyboardEvent) {
  if (event.key !== 'Tab' || !drawer.value) return
  const links = Array.from(drawer.value.querySelectorAll<HTMLElement>('.nav-link'))
  if (!links.length) return
  const first = links[0]
  const last = links[links.length - 1]
  if (!first || !last) return
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

function onGlobalKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && menuOpen.value) {
    event.preventDefault()
    setMenu(false, true)
  }
}

onMounted(() => window.addEventListener('keydown', onGlobalKeydown))
onBeforeUnmount(() => window.removeEventListener('keydown', onGlobalKeydown))

watch(
  () => route.path,
  () => setMenu(false),
)

async function logout() {
  try {
    await api.logout()
  } catch {
    // 会话已失效时本地同样退出。
  }
  emit('logout')
}
</script>

<template>
  <div class="shell">
    <aside class="sidebar">
      <RouterLink to="/overview" class="brand" aria-label="OVPanel 概览">
        <BrandMark :size="34" />
        <span>
          <strong>OVPanel</strong>
          <small>OpenVPN 管理面板</small>
        </span>
      </RouterLink>

      <nav class="nav" aria-label="主导航">
        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="nav-link"
          :class="{ active: item.to === '/users' && isUsersSection() }"
        >
          <component :is="item.icon" :size="17" aria-hidden="true" />
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>

      <div class="sidebar-foot">
        <NButton
          class="mobile-menu-button"
          quaternary
          circle
          :aria-label="menuOpen ? '关闭导航' : '打开导航'"
          :aria-expanded="menuOpen"
          aria-controls="mobile-navigation"
          @click="setMenu(!menuOpen)"
        >
          <template #icon>
            <X v-if="menuOpen" :size="18" />
            <Menu v-else :size="18" />
          </template>
        </NButton>
        <ThemeToggle />
        <div class="account">
          <span class="account-name" :title="username">{{ username }}</span>
          <NButton quaternary circle aria-label="退出登录" title="退出登录" @click="logout">
            <template #icon><LogOut :size="17" /></template>
          </NButton>
        </div>
      </div>

      <div v-if="menuOpen" class="mobile-backdrop" aria-hidden="true" @click="setMenu(false, true)" />
      <nav
        v-if="menuOpen"
        id="mobile-navigation"
        ref="drawer"
        class="mobile-drawer"
        aria-label="移动导航"
        @keydown="onDrawerKeydown"
      >
        <RouterLink
          v-for="item in navItems"
          :key="item.to"
          :to="item.to"
          class="nav-link"
          :class="{ active: item.to === '/users' && isUsersSection() }"
          @click="setMenu(false)"
        >
          <component :is="item.icon" :size="18" aria-hidden="true" />
          <span>{{ item.label }}</span>
        </RouterLink>
      </nav>
    </aside>

    <main class="workspace">
      <slot />
    </main>
  </div>
</template>

<style scoped>
.shell {
  display: grid;
  grid-template-columns: 216px minmax(0, 1fr);
  min-height: 100vh;
}

.sidebar {
  position: sticky;
  top: 0;
  display: flex;
  flex-direction: column;
  gap: 24px;
  height: 100vh;
  padding: 22px 16px 16px;
  border-right: 1px solid var(--border);
  background: color-mix(in srgb, var(--surface) 55%, var(--bg));
}

.brand {
  display: flex;
  align-items: center;
  gap: 11px;
  padding: 0 6px;
  color: inherit;
  text-decoration: none;
}

.brand span {
  display: flex;
  flex-direction: column;
  line-height: 1.15;
}

.brand strong {
  font-size: 16px;
  letter-spacing: -0.01em;
}

.brand small {
  margin-top: 3px;
  color: var(--text-3);
  font-size: 11px;
}

.nav {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.nav-link {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 40px;
  padding: 0 11px;
  border-radius: 8px;
  color: var(--text-2);
  font-size: 13px;
  font-weight: 550;
  text-decoration: none;
  transition:
    background 130ms ease,
    color 130ms ease;
}

.nav-link:hover {
  background: var(--surface-2);
  color: var(--text-1);
}

.nav-link:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}

.nav-link.router-link-active,
.nav-link.active {
  background: var(--accent-soft);
  color: var(--accent);
}

.sidebar-foot {
  display: flex;
  flex-direction: column;
  gap: 10px;
  margin-top: auto;
  padding-top: 14px;
  border-top: 1px solid var(--border);
}

.sidebar-foot :deep(.theme-toggle) {
  align-self: flex-start;
}

.account {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 2px;
  min-width: 0;
}

.account-name {
  max-width: 86px;
  overflow: hidden;
  color: var(--text-3);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.workspace {
  min-width: 0;
  padding: clamp(22px, 4vw, 44px) clamp(16px, 4vw, 40px) 64px;
}

.mobile-menu-button,
.mobile-drawer,
.mobile-backdrop {
  display: none;
}

@media (max-width: 900px) {
  .shell {
    display: block;
  }

  .sidebar {
    position: relative;
    display: flex;
    width: 100%;
    max-width: 100%;
    flex-direction: row;
    flex-wrap: nowrap;
    align-items: center;
    gap: 8px;
    height: auto;
    padding: 10px 14px;
    overflow: visible;
    border-right: 0;
    border-bottom: 1px solid var(--border);
  }

  .brand {
    padding: 0;
  }

  .brand small {
    display: none;
  }

  .nav {
    display: none;
  }

  .mobile-menu-button {
    display: inline-flex;
  }

  .nav-link {
    flex: none;
    min-height: 44px;
    padding: 0 12px;
  }

  .mobile-backdrop {
    position: fixed;
    inset: 54px 0 0;
    z-index: 40;
    display: block;
    background: color-mix(in srgb, var(--bg) 62%, transparent);
  }

  .mobile-drawer {
    position: absolute;
    top: 100%;
    right: 0;
    left: 0;
    z-index: 50;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 8px 12px 12px;
    border-bottom: 1px solid var(--border);
    background: var(--surface);
    box-shadow: var(--shadow-card);
  }

  .sidebar-foot {
    flex-direction: row;
    align-items: center;
    margin-left: auto;
    margin-top: 0;
    padding-top: 0;
    border-top: 0;
  }

  .account {
    gap: 2px;
  }

  .account-name {
    display: none;
  }

  .workspace {
    padding-top: 22px;
  }
}
</style>
