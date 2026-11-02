<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import { NButton, NModal } from 'naive-ui'

const props = withDefaults(
  defineProps<{
    show: boolean
    title: string
    description?: string
    actionLabel?: string
    loading?: boolean
    danger?: boolean
    countdown?: number
  }>(),
  { description: '', actionLabel: '确认', loading: false, danger: false, countdown: 0 },
)

const emit = defineEmits<{
  'update:show': [value: boolean]
  confirm: []
}>()

const remaining = ref(0)
let timer: number | undefined

function stopCountdown() {
  if (timer !== undefined) {
    window.clearInterval(timer)
    timer = undefined
  }
}

function startCountdown() {
  stopCountdown()
  if (!props.show || props.countdown <= 0) {
    remaining.value = 0
    return
  }
  remaining.value = props.countdown
  timer = window.setInterval(() => {
    if (remaining.value <= 1) {
      remaining.value = 0
      stopCountdown()
      return
    }
    remaining.value -= 1
  }, 1000)
}

watch([() => props.show, () => props.countdown], startCountdown, { immediate: true })
onBeforeUnmount(stopCountdown)

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
        <NButton
          :type="danger ? 'error' : 'primary'"
          :loading="loading"
          :disabled="loading || remaining > 0"
          @click="emit('confirm')"
        >
          {{ remaining > 0 ? `${actionLabel}（${remaining} 秒）` : actionLabel }}
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
