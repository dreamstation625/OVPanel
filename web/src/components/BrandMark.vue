<script setup lang="ts">
withDefaults(
  defineProps<{
    size?: number
  }>(),
  { size: 36 },
)
</script>

<template>
  <svg
    :width="size"
    :height="size"
    viewBox="0 0 64 64"
    role="img"
    aria-label="OVPanel"
    class="brand-mark"
  >
    <rect width="64" height="64" rx="15" fill="var(--accent)" />
    <path
      d="M18 34.5 32 20l14 14.5V46a2 2 0 0 1-2 2H20a2 2 0 0 1-2-2z"
      fill="none"
      stroke="var(--on-accent)"
      stroke-width="4"
      stroke-linejoin="round"
    />
    <circle cx="32" cy="38" r="4" fill="var(--on-accent)" opacity="0.85" />
  </svg>
</template>

<style scoped>
.brand-mark {
  display: block;
  flex: none;
}
</style>
