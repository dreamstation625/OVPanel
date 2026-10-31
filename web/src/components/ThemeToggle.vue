<script setup lang="ts">
import { Monitor, Moon, Sun } from '@lucide/vue'
import { useTheme, type ThemeMode } from '../theme'

const { mode, setMode } = useTheme()

const options: { value: ThemeMode; label: string; icon: typeof Sun }[] = [
  { value: 'auto', label: '跟随系统', icon: Monitor },
  { value: 'light', label: '浅色', icon: Sun },
  { value: 'dark', label: '深色', icon: Moon },
]
</script>

<template>
  <div class="theme-toggle" role="group" aria-label="主题">
    <button
      v-for="option in options"
      :key="option.value"
      type="button"
      class="theme-toggle__button"
      :class="{ active: mode === option.value }"
      :title="option.label"
      :aria-label="option.label"
      :aria-pressed="mode === option.value"
      @click="setMode(option.value)"
    >
      <component :is="option.icon" :size="16" :stroke-width="2" aria-hidden="true" />
    </button>
  </div>
</template>

<style scoped>
.theme-toggle {
  display: inline-flex;
  gap: 2px;
  padding: 3px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--surface);
  box-shadow: var(--shadow-soft);
}

.theme-toggle__button {
  display: grid;
  place-items: center;
  width: 32px;
  height: 32px;
  padding: 0;
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: var(--text-3);
  cursor: pointer;
  transition:
    background 130ms ease,
    color 130ms ease;
}

.theme-toggle__button:hover {
  color: var(--text-1);
  background: var(--surface-2);
}

.theme-toggle__button:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}

.theme-toggle__button.active {
  background: var(--accent-soft);
  color: var(--accent);
}

@media (max-width: 900px) {
  .theme-toggle__button {
    width: 38px;
    height: 38px;
  }
}
</style>
