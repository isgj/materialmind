import { ReasoningEffort } from './models';

export const REASONING_EFFORT_OPTIONS: readonly {
  value: ReasoningEffort;
  label: string;
}[] = [
  { value: 'low', label: 'Low' },
  { value: 'medium', label: 'Medium' },
  { value: 'high', label: 'High' },
  { value: 'xhigh', label: 'XHigh' },
  { value: 'max', label: 'Max' },
  { value: 'ultra', label: 'Ultra' },
];

const ANTHROPIC_REASONING_EFFORT_OPTIONS = REASONING_EFFORT_OPTIONS.filter(
  (option) => option.value !== 'ultra',
);

export function reasoningEffortOptionsForCompatibility(
  apiCompatibility: string | undefined,
): typeof REASONING_EFFORT_OPTIONS {
  if (apiCompatibility === 'anthropic') {
    return ANTHROPIC_REASONING_EFFORT_OPTIONS;
  }
  return apiCompatibility?.startsWith('openai-') ? REASONING_EFFORT_OPTIONS : [];
}

export function supportsReasoningEffort(apiCompatibility: string | undefined): boolean {
  return reasoningEffortOptionsForCompatibility(apiCompatibility).length > 0;
}

export function formatReasoningEffort(value: ReasoningEffort): string {
  return value === 'xhigh' ? 'XHigh' : value.charAt(0).toUpperCase() + value.slice(1);
}
