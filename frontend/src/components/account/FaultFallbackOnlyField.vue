<script setup lang="ts">
import { useI18n } from 'vue-i18n'

defineProps<{ platform?: string; type?: string }>()
const enabled = defineModel<boolean>({ default: false })
const { t } = useI18n()
</script>

<template>
  <div v-if="platform === 'openai' && type === 'apikey'" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
    <label class="flex cursor-pointer items-center gap-2">
      <input v-model="enabled" type="checkbox" class="checkbox" data-testid="fault-fallback-only" />
      <span class="font-medium">{{ t('admin.accounts.faultFallbackOnly') }}</span>
    </label>
    <p class="input-hint mt-2">{{ t('admin.accounts.faultFallbackOnlyHint') }}</p>
  </div>
</template>
