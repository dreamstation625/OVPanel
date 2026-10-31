<script setup lang="ts">
import { computed } from 'vue'
import { NFormItem, NInput, NSelect } from 'naive-ui'
import type { AuthType } from '../api'

const props = defineProps<{
  name: string
  authType: AuthType
  username: string
}>()

const emit = defineEmits<{
  'update:name': [value: string]
  'update:authType': [value: AuthType]
  'update:username': [value: string]
}>()

const options = [
  { label: '证书', value: 'certificate' },
  { label: '证书 + 密码', value: 'certificate_password' },
  { label: '仅密码', value: 'password' },
]

const hint = computed(() => {
  switch (props.authType) {
    case 'certificate':
      return '保存时自动签发客户端证书，登录时只需要证书。'
    case 'certificate_password':
      return '自动签发证书，同时要求登录名和密码。密码创建后只显示一次。'
    default:
      return '只使用登录名和密码，不签发客户端证书。密码创建后只显示一次。'
  }
})
</script>

<template>
  <NFormItem label="配置名称" required>
    <NInput
      :value="name"
      placeholder="例如：电脑专用"
      maxlength="80"
      @update:value="emit('update:name', $event)"
    />
  </NFormItem>
  <NFormItem label="认证方式" required>
    <NSelect
      :value="authType"
      :options="options"
      @update:value="emit('update:authType', $event as AuthType)"
    />
  </NFormItem>
  <NFormItem v-if="authType !== 'certificate'" label="登录名" required>
    <NInput
      :value="username"
      placeholder="3-64 位字母、数字、点、下划线或连字符"
      maxlength="64"
      autocomplete="off"
      @update:value="emit('update:username', $event)"
    />
  </NFormItem>
  <p class="field-hint">{{ hint }}</p>
</template>

<style scoped>
.field-hint {
  margin: -4px 0 8px;
  color: var(--text-3);
  font-size: 12px;
  line-height: 1.55;
}
</style>
