<script setup lang="ts">
import { ref, watch } from 'vue'
import { Check, Copy } from '@lucide/vue'
import { NAlert, NButton, NModal } from 'naive-ui'
import { copyText } from '../utils'

const props = withDefaults(
  defineProps<{
    show: boolean
    password: string
    title?: string
    username?: string
  }>(),
  { title: '密码只显示一次', username: '' },
)

const emit = defineEmits<{
  'update:show': [value: boolean]
}>()

const copied = ref(false)
const copyFailed = ref(false)

watch(
  () => props.show,
  (visible) => {
    if (visible) {
      copied.value = false
      copyFailed.value = false
    }
  },
)

async function copy() {
  copied.value = await copyText(props.password)
  copyFailed.value = !copied.value
}
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    :title="title"
    :style="{ width: 'min(460px, calc(100vw - 28px))' }"
    :closable="false"
    :mask-closable="false"
  >
    <div class="password-body">
      <NAlert type="warning" :bordered="false">
        关闭后无法再次查看。请先复制或妥善保存，再继续操作。
      </NAlert>
      <p v-if="username" class="username">登录名 <code>{{ username }}</code></p>
      <div class="password-value">
        <code>{{ password }}</code>
      </div>
      <span v-if="copyFailed" class="copy-hint">复制失败，请手动选择密码。</span>
    </div>
    <template #footer>
      <div class="modal-actions">
        <NButton @click="copy">
          <template #icon>
            <component :is="copied ? Check : Copy" :size="16" />
          </template>
          {{ copied ? '已复制' : '复制密码' }}
        </NButton>
        <NButton type="primary" @click="emit('update:show', false)">我已保存</NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.password-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.username {
  margin: 0;
  color: var(--text-2);
  font-size: 13px;
}

.username code {
  padding: 2px 6px;
  border-radius: 5px;
  background: var(--surface-2);
  color: var(--text-1);
}

.password-value {
  padding: 14px 16px;
  border: 1px solid var(--border);
  border-radius: 10px;
  background: var(--surface-2);
  overflow-x: auto;
}

.password-value code {
  color: var(--text-1);
  font-size: 16px;
  font-weight: 600;
  letter-spacing: 0.02em;
  white-space: nowrap;
}

.copy-hint {
  color: var(--danger);
  font-size: 12px;
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
