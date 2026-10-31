<script setup lang="ts">
import { ref, watch } from 'vue'
import { KeyRound } from '@lucide/vue'
import { NAlert, NButton, NInput, NModal } from 'naive-ui'

const props = withDefaults(
  defineProps<{
    show: boolean
    title: string
    description?: string
    actionLabel?: string
    loading?: boolean
    error?: string
  }>(),
  { description: '', actionLabel: '确认', loading: false, error: '' },
)

const emit = defineEmits<{
  'update:show': [value: boolean]
  confirm: [password: string]
}>()

const password = ref('')
const localError = ref('')

watch(
  () => props.show,
  (visible) => {
    if (visible) {
      password.value = ''
      localError.value = ''
    }
  },
)

function close() {
  if (!props.loading) {
    emit('update:show', false)
  }
}

function confirm() {
  if (!password.value) {
    localError.value = '请输入管理员密码'
    return
  }
  localError.value = ''
  emit('confirm', password.value)
}
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    :title="title"
    :style="{ width: 'min(420px, calc(100vw - 28px))' }"
    :mask-closable="!loading"
    @update:show="emit('update:show', $event)"
  >
    <div class="prompt-body">
      <p v-if="description" class="prompt-description">{{ description }}</p>
      <NAlert v-if="localError || error" type="error" :bordered="false">{{ localError || error }}</NAlert>
      <NInput
        v-model:value="password"
        type="password"
        show-password-on="click"
        placeholder="管理员密码"
        autocomplete="current-password"
        autofocus
        @keyup.enter="confirm"
      />
      <span class="prompt-hint"><KeyRound :size="14" /> 该密码仅用于本次操作，不会保存。</span>
    </div>
    <template #footer>
      <div class="modal-actions">
        <NButton :disabled="loading" @click="close">取消</NButton>
        <NButton type="primary" :loading="loading" @click="confirm">{{ actionLabel }}</NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.prompt-body {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.prompt-description {
  margin: 0;
  color: var(--text-2);
  font-size: 13px;
  line-height: 1.65;
}

.prompt-hint {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: var(--text-3);
  font-size: 11.5px;
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
