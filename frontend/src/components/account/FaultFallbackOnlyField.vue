<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { computed } from 'vue'
import { DEFAULT_FAULT_FALLBACK_STATUS_CODES, parseFaultFallbackStatusCodes, parseFaultFallbackKeywords, type FaultFallbackMatchMode } from '@/utils/accountFaultFallback'

defineProps<{ platform?: string; type?: string }>()
const enabled = defineModel<boolean>({ default: false })
const statusCodes = defineModel<string>('statusCodes', { default: DEFAULT_FAULT_FALLBACK_STATUS_CODES })
const keywords = defineModel<string>('keywords', { default: '' })
const matchMode = defineModel<FaultFallbackMatchMode>('matchMode', { default: 'any' })
const invalid = computed(() => parseFaultFallbackStatusCodes(statusCodes.value) === null)
const invalidKeywords = computed(() => parseFaultFallbackKeywords(keywords.value) === null)
const { t } = useI18n()
const matchModeOptions = computed(() => [
  { value: 'any', label: t('admin.accounts.faultFallbackMatchAny'), description: t('admin.accounts.faultFallbackMatchAnyHint') },
  { value: 'all', label: t('admin.accounts.faultFallbackMatchAll'), description: t('admin.accounts.faultFallbackMatchAllHint') }
])
</script>

<template>
  <div v-if="platform === 'openai' && type === 'apikey'" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600">
    <label class="flex cursor-pointer items-center gap-2">
      <input v-model="enabled" type="checkbox" class="checkbox" data-testid="fault-fallback-only" />
      <span class="font-medium">{{ t('admin.accounts.faultFallbackOnly') }}</span>
    </label>
    <p class="input-hint mt-2">{{ t('admin.accounts.faultFallbackOnlyHint') }}</p>
    <fieldset class="mt-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600">
      <legend class="px-1 text-sm font-medium text-gray-900 dark:text-white">{{ t('admin.accounts.faultFallbackMatchConditions') }}</legend>
      <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <label>
          <span class="input-label text-xs">{{ t('admin.accounts.faultFallbackStatusCodes') }}</span>
          <input v-model="statusCodes" type="text" class="input text-sm" data-testid="fault-fallback-status-codes"
            :aria-invalid="invalid" placeholder="401, 50x, 5x3, 4xx" />
          <span class="input-hint text-xs">{{ t('admin.accounts.faultFallbackStatusCodesHint') }}</span>
          <span v-if="invalid" class="mt-1 block text-sm text-red-600" role="alert">{{ t('admin.accounts.faultFallbackStatusCodesInvalid') }}</span>
        </label>
        <label>
          <span class="input-label text-xs">{{ t('admin.accounts.faultFallbackKeywords') }}</span>
          <textarea v-model="keywords" rows="3" class="input font-mono text-xs" data-testid="fault-fallback-keywords"
            :aria-invalid="invalidKeywords" :placeholder="t('admin.accounts.faultFallbackKeywordsPlaceholder')" />
          <span class="input-hint text-xs">{{ t('admin.accounts.faultFallbackKeywordsHint') }}</span>
          <span v-if="invalidKeywords" class="mt-1 block text-sm text-red-600" role="alert">{{ t('admin.accounts.faultFallbackKeywordsInvalid') }}</span>
        </label>
      </div>
      <div class="mt-3">
        <span class="input-label text-xs">{{ t('admin.accounts.faultFallbackMatchMode') }}</span>
        <div class="mt-1 space-y-2">
          <label v-for="option in matchModeOptions" :key="option.value" class="flex cursor-pointer items-start gap-2">
            <input v-model="matchMode" type="radio" :value="option.value" :data-testid="`fault-fallback-match-${option.value}`"
              class="mt-0.5 h-3.5 w-3.5 border-gray-300 text-primary-600 focus:ring-primary-500" />
            <span class="flex-1">
              <span class="text-xs font-medium text-gray-700 dark:text-gray-300">{{ option.label }}</span>
              <span class="block text-xs text-gray-500 dark:text-gray-400">{{ option.description }}</span>
            </span>
          </label>
        </div>
      </div>
      <p class="input-hint mt-3 text-xs">{{ t('admin.accounts.faultFallbackPriorityHint') }}</p>
    </fieldset>
  </div>
</template>
