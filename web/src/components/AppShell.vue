<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
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
import { NButton, NModal } from 'naive-ui'
import BrandMark from './BrandMark.vue'
import ThemeToggle from './ThemeToggle.vue'
import { api, type UpdateCheck } from '../api'
import { errorMessage, formatDate } from '../labels'

defineProps<{
  username: string
}>()

const emit = defineEmits<{
  logout: []
}>()

const route = useRoute()
const menuOpen = ref(false)
const drawer = ref<HTMLElement | null>(null)
const version = ref('')
const versionCommit = ref('')
const update = ref<UpdateCheck | null>(null)
const updateError = ref('')
const checkingUpdate = ref(false)
const showUpdate = ref(false)

function formatVersion(value?: string) {
  if (!value || value === 'none') {
    return '—'
  }
  return value === 'dev' ? '开发版' : `v${value.replace(/^v/, '')}`
}

const versionLabel = computed(() => {
  if (!version.value) {
    return '版本读取中'
  }
  return formatVersion(version.value)
})
const hasUpdate = computed(() => Boolean(update.value?.updateAvailable))
const releaseURL = computed(() => update.value?.url || 'https://github.com/dreamstation625/OVPanel/releases')
const releaseNotes = computed(() =>
  (update.value?.notes ?? '')
    .replace(/^#{1,6}\s*/gm, '')
    .replace(/^\s*[-*]\s+/gm, '• ')
    .trim(),
)

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

onMounted(() => {
  window.addEventListener('keydown', onGlobalKeydown)
  void loadVersion()
  void checkUpdates(true)
})
onBeforeUnmount(() => window.removeEventListener('keydown', onGlobalKeydown))

watch(
  () => route.path,
  () => setMenu(false),
)

async function loadVersion() {
  try {
    const info = await api.system()
    version.value = info.version
    versionCommit.value = info.commit
  } catch {
    // 版本读取失败不阻断页面；点击检测时仍会显示当前构建结果。
  }
}

async function checkUpdates(auto = false) {
  if (checkingUpdate.value) {
    if (!auto) {
      showUpdate.value = true
    }
    return
  }
  checkingUpdate.value = true
  updateError.value = ''
  if (!auto) {
    showUpdate.value = true
  }
  try {
    const result = await api.updateCheck()
    version.value = result.currentVersion || version.value
    versionCommit.value = result.currentCommit || versionCommit.value
    if (result.error) {
      updateError.value = result.error
      update.value = null
    } else {
      update.value = result
    }
  } catch (err) {
    updateError.value = errorMessage(err)
  } finally {
    checkingUpdate.value = false
  }
}

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
        <button
          class="version-button"
          :class="{ 'has-update': hasUpdate }"
          :disabled="checkingUpdate"
          :title="hasUpdate ? '发现新版本，查看更新内容' : '检测更新'"
          @click="checkUpdates(false)"
        >
          <span class="version-label">{{ versionLabel }}</span>
          <span v-if="hasUpdate" class="update-badge">有新版本</span>
          <span v-else class="version-action">{{ checkingUpdate ? '检测中…' : '检测更新' }}</span>
        </button>
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

    <NModal
      v-model:show="showUpdate"
      preset="card"
      title="版本与更新"
      :style="{ width: 'min(640px, calc(100vw - 28px))' }"
      :mask-closable="!checkingUpdate"
    >
      <div class="update-body">
        <p>
          当前版本：<strong>{{ versionLabel }}</strong>
          <span v-if="versionCommit && versionCommit !== 'none'" class="update-meta">
            （{{ versionCommit.slice(0, 8) }}）
          </span>
        </p>
        <p v-if="checkingUpdate" class="update-meta">正在检测更新…</p>
        <div v-else-if="updateError" class="update-warning">{{ updateError }}</div>
        <template v-else-if="update">
          <p>
            最新版本：<strong>{{ formatVersion(update.latestVersion) }}</strong>
          </p>
          <p v-if="update.publishedAt" class="update-meta">发布时间 {{ formatDate(update.publishedAt) }}</p>
          <div class="update-state" :class="{ 'is-available': update.updateAvailable }">
            {{ update.updateAvailable ? '发现新版本，请按 Release 说明更新。' : '当前已是最新版本。' }}
          </div>
          <pre v-if="releaseNotes" class="release-notes">{{ releaseNotes }}</pre>
          <p class="update-hint">
            只提示正式版本。按 Release 说明更新面板和 OpenVPN 镜像并重建容器；面板不会在网页内自动更新。
          </p>
        </template>
        <div class="update-actions">
          <NButton @click="showUpdate = false">关闭</NButton>
          <a class="update-link" :href="releaseURL" target="_blank" rel="noreferrer">打开发布页</a>
        </div>
      </div>
    </NModal>
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

.version-button {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  width: 100%;
  min-height: 28px;
  padding: 0 6px;
  border: 0;
  border-radius: 6px;
  background: transparent;
  color: var(--text-3);
  font: inherit;
  font-size: 11.5px;
  text-align: left;
  cursor: pointer;
}

.version-button:hover {
  background: var(--surface-2);
  color: var(--text-1);
}

.version-button:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}

.version-button:disabled {
  cursor: wait;
}

.version-label {
  white-space: nowrap;
}

.version-action {
  color: var(--text-3);
  font-size: 11px;
}

.version-button.has-update .version-label::after {
  display: inline-block;
  width: 6px;
  height: 6px;
  margin-left: 5px;
  border-radius: 50%;
  background: var(--accent);
  content: '';
  vertical-align: 1px;
}

.update-badge {
  color: var(--accent);
  font-size: 11px;
  font-weight: 600;
  white-space: nowrap;
}

.update-body {
  display: flex;
  flex-direction: column;
  gap: 10px;
  color: var(--text-2);
  font-size: 13px;
}

.update-body p {
  margin: 0;
}

.update-meta {
  color: var(--text-3);
  font-size: 11.5px;
}

.update-warning {
  padding: 10px 12px;
  border-radius: 8px;
  background: var(--surface-2);
  color: var(--text-2);
}

.update-state {
  padding: 10px 12px;
  border-radius: 8px;
  background: var(--surface-2);
  color: var(--text-2);
}

.update-state.is-available {
  background: var(--accent-soft);
  color: var(--accent);
}

.release-notes {
  max-height: 300px;
  margin: 0;
  padding: 12px;
  overflow: auto;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--surface-2);
  color: var(--text-2);
  font-family: var(--font-body);
  font-size: 12px;
  line-height: 1.7;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}

.update-hint {
  color: var(--text-3);
  font-size: 11.5px;
}

.update-actions {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 12px;
  margin-top: 4px;
}

.update-link {
  color: var(--accent);
  font-size: 12px;
  text-decoration: none;
}

.update-link:hover {
  text-decoration: underline;
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

  .version-button {
    width: auto;
    padding: 0 4px;
    flex: none;
  }

  .version-action,
  .update-badge {
    display: none;
  }

  .workspace {
    padding-top: 22px;
  }
}
</style>
