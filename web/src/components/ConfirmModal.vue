<script setup lang="ts">
import { NButton, NModal } from 'naive-ui'

const props = withDefaults(
  defineProps<{
    show: boolean
    title: string
    description?: string
    actionLabel?: string
    loading?: boolean
    danger?: boolean
  }>(),
  { description: '', actionLabel: '确认', loading: false, danger: false },
)

const emit = defineEmits<{
  'update:show': [value: boolean]
  confirm: []
}>()

function close() {
  if (!props.loading) {
    emit('update:show', false)
  }
}
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    :title="title"
    :style="{ width: 'min(440px, calc(100vw - 28px))' }"
    :mask-closable="!loading"
    @update:show="emit('update:show', $event)"
  >
    <p v-if="description" class="confirm-description">{{ description }}</p>
    <template #footer>
      <div class="modal-actions">
        <NButton :disabled="loading" @click="close">取消</NButton>
        <NButton :type="danger ? 'error' : 'primary'" :loading="loading" @click="emit('confirm')">
          {{ actionLabel }}
        </NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.confirm-description {
  margin: 0;
  color: var(--text-2);
  font-size: 13px;
  line-height: 1.65;
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
