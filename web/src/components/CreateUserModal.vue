<script setup lang="ts">
import { ref, watch } from 'vue'
import { NAlert, NButton, NForm, NFormItem, NInput, NModal, NSwitch } from 'naive-ui'
import ProfileFields from './ProfileFields.vue'
import { api, type AuthType, type CreateUserResult } from '../api'
import { errorMessage, profileValidationMessage, suggestUsername } from '../labels'

const props = defineProps<{
  show: boolean
}>()

const emit = defineEmits<{
  'update:show': [value: boolean]
  created: [result: CreateUserResult]
}>()

const displayName = ref('')
const notes = ref('')
const tagsText = ref('')
const withProfile = ref(true)
const profileName = ref('默认配置')
const authType = ref<AuthType>('certificate')
const username = ref(suggestUsername())
const error = ref('')
const busy = ref(false)

watch(
  () => props.show,
  (visible) => {
    if (visible) {
      displayName.value = ''
      notes.value = ''
      tagsText.value = ''
      withProfile.value = true
      profileName.value = '默认配置'
      authType.value = 'certificate'
      username.value = suggestUsername()
      error.value = ''
      busy.value = false
    }
  },
)

function parsedTags(): string[] {
  return tagsText.value
    .split(/[,，、]/)
    .map((tag) => tag.trim())
    .filter(Boolean)
}

async function submit() {
  if (busy.value) {
    return
  }
  error.value = ''
  if (!displayName.value.trim()) {
    error.value = '请输入用户名称'
    return
  }
  if (withProfile.value) {
    const profileError = profileValidationMessage(profileName.value, authType.value, username.value)
    if (profileError) {
      error.value = profileError
      return
    }
  }

  busy.value = true
  try {
    const result = await api.createUser({
      displayName: displayName.value.trim(),
      notes: notes.value.trim(),
      tags: parsedTags(),
      profile: withProfile.value
        ? {
            name: profileName.value.trim(),
            authType: authType.value,
            username: authType.value === 'certificate' ? '' : username.value.trim(),
          }
        : undefined,
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
    title="添加用户"
    :style="{ width: 'min(560px, calc(100vw - 28px))' }"
    :mask-closable="!busy"
    @update:show="emit('update:show', $event)"
  >
    <NAlert v-if="error" type="error" :bordered="false" class="stack-gap">{{ error }}</NAlert>
    <NForm>
      <NFormItem label="用户名称" required>
        <NInput v-model:value="displayName" placeholder="例如：张三" maxlength="80" autofocus />
      </NFormItem>
      <NFormItem label="备注">
        <NInput v-model:value="notes" type="textarea" placeholder="可选" :autosize="{ minRows: 2, maxRows: 4 }" maxlength="2000" />
      </NFormItem>
      <NFormItem label="标签">
        <NInput v-model:value="tagsText" placeholder="用逗号分隔，例如：家庭, 手机" maxlength="200" />
      </NFormItem>

      <div class="profile-switch">
        <div>
          <strong>同时创建首份配置</strong>
          <span>创建后直接得到可下载的客户端配置。</span>
        </div>
        <NSwitch v-model:value="withProfile" aria-label="同时创建首份配置" />
      </div>

      <div v-if="withProfile" class="profile-block">
        <ProfileFields
          v-model:name="profileName"
          v-model:auth-type="authType"
          v-model:username="username"
        />
      </div>
    </NForm>

    <template #footer>
      <div class="modal-actions">
        <NButton :disabled="busy" @click="emit('update:show', false)">取消</NButton>
        <NButton type="primary" :loading="busy" @click="submit">创建</NButton>
      </div>
    </template>
  </NModal>
</template>

<style scoped>
.profile-switch {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  margin: 4px 0 14px;
  padding: 12px 14px;
  border: 1px solid var(--border);
  border-radius: 9px;
  background: var(--surface-2);
}

.profile-switch div {
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.profile-switch strong {
  font-size: 13px;
}

.profile-switch span {
  color: var(--text-3);
  font-size: 11.5px;
}

.profile-block {
  padding-top: 6px;
  border-top: 1px solid var(--border);
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
