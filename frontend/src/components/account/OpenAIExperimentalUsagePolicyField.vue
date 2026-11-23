<template>
  <section class="space-y-4 border-t border-gray-200 pt-4 dark:border-dark-600" data-testid="openai-experimental-usage-policy">
    <div class="flex items-start justify-between gap-4">
      <div class="min-w-0">
        <h3 class="text-sm font-medium text-gray-900 dark:text-white">
          {{ t('admin.accounts.openai.experimentalUsagePolicy.title') }}
        </h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
          {{ t('admin.accounts.openai.experimentalUsagePolicy.hint') }}
        </p>
      </div>
      <Toggle
        v-model="enabledModel"
        :aria-label="t('admin.accounts.openai.experimentalUsagePolicy.enabled')"
        data-testid="openai-experimental-usage-policy-toggle"
      />
    </div>

    <div v-if="enabled" class="space-y-3">
      <div class="hidden grid-cols-[minmax(0,0.9fr)_minmax(0,1fr)_minmax(0,0.8fr)_2.5rem] gap-3 px-1 text-xs font-medium text-gray-500 dark:text-gray-400 sm:grid">
        <span>{{ t('admin.accounts.openai.experimentalUsagePolicy.transferMode') }}</span>
        <span>{{ t('admin.accounts.openai.experimentalUsagePolicy.transferValue') }}</span>
        <span>{{ t('admin.accounts.openai.experimentalUsagePolicy.probabilityPercent') }}</span>
        <span class="sr-only">{{ t('admin.accounts.openai.experimentalUsagePolicy.removeRule') }}</span>
      </div>

      <div
        v-for="(rule, index) in rules"
        :key="index"
        class="grid grid-cols-1 gap-3 rounded-md border border-gray-200 p-3 dark:border-dark-600 sm:grid-cols-[minmax(0,0.9fr)_minmax(0,1fr)_minmax(0,0.8fr)_2.5rem] sm:items-end"
      >
        <label class="min-w-0">
          <span class="mb-1 block text-xs text-gray-500 dark:text-gray-400 sm:hidden">
            {{ t('admin.accounts.openai.experimentalUsagePolicy.transferMode') }}
          </span>
          <select
            :value="transferModeOf(rule)"
            class="input"
            :data-testid="`openai-cache-transfer-mode-${index}`"
            @change="updateRule(index, 'transfer_mode', $event)"
          >
            <option value="percent">{{ t('admin.accounts.openai.experimentalUsagePolicy.modePercent') }}</option>
            <option value="exact">{{ t('admin.accounts.openai.experimentalUsagePolicy.modeExact') }}</option>
          </select>
        </label>

        <label class="min-w-0">
          <span class="mb-1 block text-xs text-gray-500 dark:text-gray-400 sm:hidden">
            {{ transferModeOf(rule) === 'exact'
              ? t('admin.accounts.openai.experimentalUsagePolicy.keepTokens')
              : t('admin.accounts.openai.experimentalUsagePolicy.transferPercent') }}
          </span>
          <div v-if="transferModeOf(rule) === 'exact'" class="relative">
            <input
              :value="rule.keep_tokens"
              type="number"
              min="0"
              step="1"
              inputmode="numeric"
              class="input pr-16"
              :data-testid="`openai-cache-keep-tokens-${index}`"
              @input="updateRule(index, 'keep_tokens', $event)"
            />
            <span class="pointer-events-none absolute inset-y-0 right-3 flex items-center text-sm text-gray-400">
              {{ t('admin.accounts.openai.experimentalUsagePolicy.tokenUnit') }}
            </span>
          </div>
          <div v-else class="relative">
            <input
              :value="rule.transfer_percent"
              type="number"
              min="0"
              max="100"
              step="1"
              inputmode="numeric"
              class="input pr-8"
              :data-testid="`openai-cache-transfer-percent-${index}`"
              @input="updateRule(index, 'transfer_percent', $event)"
            />
            <span class="pointer-events-none absolute inset-y-0 right-3 flex items-center text-sm text-gray-400">%</span>
          </div>
        </label>

        <label class="min-w-0">
          <span class="mb-1 block text-xs text-gray-500 dark:text-gray-400 sm:hidden">
            {{ t('admin.accounts.openai.experimentalUsagePolicy.probabilityPercent') }}
          </span>
          <div class="relative">
            <input
              :value="rule.probability_percent"
              type="number"
              min="1"
              max="100"
              step="1"
              inputmode="numeric"
              class="input pr-8"
              :data-testid="`openai-cache-probability-percent-${index}`"
              @input="updateRule(index, 'probability_percent', $event)"
            />
            <span class="pointer-events-none absolute inset-y-0 right-3 flex items-center text-sm text-gray-400">%</span>
          </div>
        </label>

        <button
          type="button"
          class="flex h-10 w-10 items-center justify-center text-red-500 hover:text-red-700 disabled:cursor-not-allowed disabled:opacity-40"
          :title="t('admin.accounts.openai.experimentalUsagePolicy.removeRule')"
          :aria-label="t('admin.accounts.openai.experimentalUsagePolicy.removeRule')"
          :disabled="rules.length <= 1"
          @click="removeRule(index)"
        >
          <Icon name="trash" size="sm" />
        </button>
      </div>

      <div class="flex flex-wrap items-center justify-between gap-3">
        <button type="button" class="btn btn-secondary inline-flex items-center gap-2 text-sm" @click="addRule">
          <Icon name="plus" size="sm" />
          {{ t('admin.accounts.openai.experimentalUsagePolicy.addRule') }}
        </button>
        <div class="text-right">
          <p
            class="text-sm tabular-nums"
            :class="probabilityTotal <= 100 ? 'text-green-600 dark:text-green-400' : 'text-red-600 dark:text-red-400'"
            data-testid="openai-cache-probability-total"
          >
            {{ t('admin.accounts.openai.experimentalUsagePolicy.probabilityTotal', { total: probabilityTotal }) }}
          </p>
          <p
            v-if="probabilityTotal < 100"
            class="text-xs text-gray-500 dark:text-gray-400"
            data-testid="openai-cache-probability-unmatched"
          >
            {{ t('admin.accounts.openai.experimentalUsagePolicy.probabilityUnmatched', { remaining: 100 - probabilityTotal }) }}
          </p>
        </div>
      </div>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { OpenAINormalInputCacheRule, OpenAINormalInputCacheTransferMode } from '@/types'
import Icon from '@/components/icons/Icon.vue'
import Toggle from '@/components/common/Toggle.vue'
import {
  DEFAULT_OPENAI_NORMAL_INPUT_CACHE_RULES,
  cloneOpenAINormalInputCacheRules,
  normalizeOpenAINormalInputCacheTransferMode
} from './openaiExperimentalUsagePolicy'

const props = defineProps<{
  enabled: boolean
  rules: OpenAINormalInputCacheRule[]
}>()

const emit = defineEmits<{
  (event: 'update:enabled', value: boolean): void
  (event: 'update:rules', value: OpenAINormalInputCacheRule[]): void
}>()

const { t } = useI18n()

const enabledModel = computed({
  get: () => props.enabled,
  set: (value: boolean) => {
    emit('update:enabled', value)
    if (value && props.rules.length === 0) {
      emit('update:rules', cloneOpenAINormalInputCacheRules(DEFAULT_OPENAI_NORMAL_INPUT_CACHE_RULES))
    }
  }
})

const probabilityTotal = computed(() => props.rules.reduce((sum, rule) => {
  const value = Number(rule.probability_percent)
  return sum + (Number.isFinite(value) ? value : 0)
}, 0))

function transferModeOf(rule: OpenAINormalInputCacheRule): OpenAINormalInputCacheTransferMode {
  return normalizeOpenAINormalInputCacheTransferMode(rule.transfer_mode)
}

function updateRule(index: number, field: keyof OpenAINormalInputCacheRule, event: Event) {
  const raw = (event.target as HTMLInputElement | HTMLSelectElement).value
  const next = cloneOpenAINormalInputCacheRules(props.rules)
  if (field === 'transfer_mode') {
    next[index] = { ...next[index], transfer_mode: normalizeOpenAINormalInputCacheTransferMode(raw as OpenAINormalInputCacheTransferMode) }
  } else {
    const value = raw === '' ? Number.NaN : Number(raw)
    next[index] = { ...next[index], [field]: value }
  }
  emit('update:rules', next)
}

function addRule() {
  emit('update:rules', [
    ...cloneOpenAINormalInputCacheRules(props.rules),
    { transfer_mode: 'percent', transfer_percent: 0, keep_tokens: 0, probability_percent: 1 }
  ])
}

function removeRule(index: number) {
  if (props.rules.length <= 1) return
  emit('update:rules', props.rules.filter((_, ruleIndex) => ruleIndex !== index))
}
</script>
