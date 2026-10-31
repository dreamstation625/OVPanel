<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { LayoutDashboard, LogOut, Server, Settings, Users } from '@lucide/vue'
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
const usersActive = computed(() => route.path.startsWith('/users') || route.path.startsWith('/profiles'))

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
        <RouterLink to="/overview" class="nav-link">
          <LayoutDashboard :size="17" aria-hidden="true" />
          <span>概览</span>
        </RouterLink>
        <RouterLink to="/users" class="nav-link" :class="{ active: usersActive }">
          <Users :size="17" aria-hidden="true" />
          <span>用户与客户端</span>
        </RouterLink>
        <RouterLink to="/server" class="nav-link">
          <Server :size="17" aria-hidden="true" />
          <span>服务端</span>
        </RouterLink>
        <RouterLink to="/system" class="nav-link">
          <Settings :size="17" aria-hidden="true" />
          <span>系统</span>
        </RouterLink>
      </nav>

      <div class="sidebar-foot">
        <ThemeToggle />
        <div class="account">
          <span class="account-name" :title="username">{{ username }}</span>
          <NButton quaternary circle aria-label="退出登录" title="退出登录" @click="logout">
            <template #icon><LogOut :size="17" /></template>
          </NButton>
        </div>
      </div>
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

@media (max-width: 900px) {
  .shell {
    display: block;
  }

  .sidebar {
    position: static;
    display: flex;
    flex-direction: row;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    height: auto;
    padding: 10px 14px;
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
    order: 3;
    flex: 1 1 100%;
    flex-direction: row;
    overflow-x: auto;
    padding-bottom: 2px;
  }

  .nav-link {
    flex: none;
    min-height: 38px;
    padding: 0 12px;
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
