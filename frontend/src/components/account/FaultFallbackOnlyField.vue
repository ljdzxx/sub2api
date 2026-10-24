<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed } from 'vue'
import { DEFAULT_FAULT_FALLBACK_STATUS_CODES, parseFaultFallbackStatusCodes } from '@/utils/accountFaultFallback'

defineProps<{ platform?: string; type?: string }>()
const enabled = defineModel<boolean>({ default: false })
const statusCodes = defineModel<string>('statusCodes', { default: DEFAULT_FAULT_FALLBACK_STATUS_CODES })
const invalid = computed(() => parseFaultFallbackStatusCodes(statusCodes.value) === null)
const { t } = useI18n()
</script>

<template>
  <div v-if="platform === 'openai' && type === 'apikey'" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
    <label class="flex cursor-pointer items-center gap-2">
      <input v-model="enabled" type="checkbox" class="checkbox" data-testid="fault-fallback-only" />
      <span class="font-medium">{{ t('admin.accounts.faultFallbackOnly') }}</span>
    </label>
    <p class="input-hint mt-2">{{ t('admin.accounts.faultFallbackOnlyHint') }}</p>
    <div class="mt-3">
      <label class="input-label">{{ t('admin.accounts.faultFallbackStatusCodes') }}</label>
      <input v-model="statusCodes" type="text" class="input" data-testid="fault-fallback-status-codes"
        :aria-invalid="invalid" placeholder="401, 50x, 5x3, 4xx" />
      <p class="input-hint">{{ t('admin.accounts.faultFallbackStatusCodesHint') }}</p>
      <p v-if="invalid" class="mt-1 text-sm text-red-600" role="alert">{{ t('admin.accounts.faultFallbackStatusCodesInvalid') }}</p>
    </div>
  </div>
</template>
