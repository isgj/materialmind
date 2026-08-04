import { describe, expect, it } from 'vitest';

import {
  REASONING_EFFORT_OPTIONS,
  reasoningEffortOptionsForCompatibility,
  supportsReasoningEffort,
} from './reasoning-effort';

describe('reasoning effort compatibility', () => {
  it('keeps all configured effort levels for OpenAI-compatible providers', () => {
    expect(reasoningEffortOptionsForCompatibility('openai-responses')).toEqual(
      REASONING_EFFORT_OPTIONS,
    );
  });

  it('omits OpenAI-only ultra effort for Anthropic-compatible providers', () => {
    expect(
      reasoningEffortOptionsForCompatibility('anthropic').map((option) => option.value),
    ).toEqual(['low', 'medium', 'high', 'xhigh', 'max']);
    expect(supportsReasoningEffort('anthropic')).toBe(true);
  });

  it('does not expose effort for providers without the capability', () => {
    expect(reasoningEffortOptionsForCompatibility(undefined)).toEqual([]);
    expect(supportsReasoningEffort('acp')).toBe(false);
  });
});
