/**
 * The Assistant settings page's words and shapes. The API calls live in the
 * client; this is what the page says about what they return.
 */
import type { AssistantProviderCheck, AssistantProviderKind } from '$lib/api/types';

/** One line for a card: what the last Test learned, or that none was run. */
export function checkSummary(check: AssistantProviderCheck | null | undefined): string {
  if (!check) return 'Never tested';
  if (!check.ok) return check.error || 'The last test failed and gave no reason.';
  const usage = check.usage_reported ? 'usage reported' : 'usage not reported';
  return `Answered as ${check.model || 'the model'} in ${check.latency_ms} ms; ${usage}`;
}

/** What each kind is called where a person chooses one. */
export const KIND_LABELS: Record<AssistantProviderKind, string> = {
  fake: 'Built-in demo model',
  openai_compatible: 'OpenAI-compatible server (Ollama, LM Studio, vLLM, OpenRouter)',
  anthropic: 'Anthropic API',
  openai: 'OpenAI API',
};

/** The hint under the address field, which changes with the kind. */
export function baseURLHint(kind: AssistantProviderKind, defaultBaseURL: string): string {
  if (kind === 'openai_compatible') {
    return 'The address the server listens on, including its API prefix: http://localhost:11434/v1 for Ollama.';
  }
  if (defaultBaseURL)
    return `Leave empty for ${defaultBaseURL}; set it only for a gateway in front of the API.`;
  return 'Where requests go.';
}
