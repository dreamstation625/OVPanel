<script setup lang="ts">
import { ref, watch } from 'vue'
import { NAlert, NButton, NForm, NModal } from 'naive-ui'
import ProfileFields from './ProfileFields.vue'
import { api, type AddProfileResult, type AuthType } from '../api'
import { errorMessage, profileValidationMessage, suggestUsername } from '../labels'

const props = defineProps<{
  show: boolean
  userId: string
}>()

const emit = defineEmits<{
  'update:show': [value: boolean]
  created: [result: AddProfileResult]
}>()

const profileName = ref('新配置')
const authType = ref<AuthType>('certificate')
const username = ref(suggestUsername())
const error = ref('')
const busy = ref(false)

watch(
  () => props.show,
  (visible) => {
    if (visible) {
      profileName.value = '新配置'
      authType.value = 'certificate'
      username.value = suggestUsername()
      error.value = ''
      busy.value = false
    }
  },
)

async function submit() {
  if (busy.value) {
    return
  }
  error.value = ''
  const profileError = profileValidationMessage(profileName.value, authType.value, username.value)
  if (profileError) {
    error.value = profileError
    return
  }

  busy.value = true
  try {
    const result = await api.addProfile(props.userId, {
      name: profileName.value.trim(),
      authType: authType.value,
      username: authType.value === 'certificate' ? '' : username.value.trim(),
    })
    emit('created', result)
    emit('update:show', false)
  } catch (err) {
    error.value = errorMessage(err)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    title="添加客户端配置"
    :style="{ width: 'min(500px, calc(100vw - 28px))' }"
    :mask-closable="!busy"
    @update:show="emit('update:show', $event)"
  >
    <NAlert v-if="error" type="error" :bordered="false" class="stack-gap">{{ error }}</NAlert>
    <NForm>
      <ProfileFields
        v-model:name="profileName"
        v-model:auth-type="authType"
        v-model:username="username"
      />
    </NForm>

    <template #footer>
      <div class="modal-actions">
        <NButton :disabled="busy" @click="emit('update:show', false)">取消</NButton>
        <NButton type="primary" :loading="busy" @click="submit">创建配置</NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
