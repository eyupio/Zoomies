/**
 * The Assistant settings page's words and shapes. The API calls live in the
 * client; this is what the page says about what they return.
 */
import type {
  AssistantProvider,
  AssistantProviderCheck,
  AssistantProviderKind,
} from '$lib/api/types';

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
  claude_code: 'Claude, your own subscription (through Claude Code)',
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

/**
 * A provider a person can pick by name. A preset is a kind, an address and the
 * words that help with them: which key to paste, and what to know before they
 * do. Several presets can share one kind; Ollama Cloud, OpenCode Zen and OpenCode Go
 * all speak the OpenAI chat protocol, so they are an address and a hint on top of
 * the OpenAI-compatible adapter and not adapters of their own.
 */
export interface ProviderPreset {
  id: string;
  label: string;
  kind: AssistantProviderKind;
  /** The address to fill in, or empty where the kind has its own or none. */
  baseURL: string;
  /** What the provider is called on its card until the person says otherwise. */
  name: string;
  /** Where the key comes from, and anything the person should know first. */
  help: string;
  /**
   * Somebody's own subscription, used through the vendor's own tool on the
   * controller's machine: no address and no key, and the person who adds it is the
   * only one who may use it.
   */
  subscription?: boolean;
}

export const PRESETS: readonly ProviderPreset[] = [
  {
    id: 'ollama-cloud',
    label: 'Ollama Cloud',
    kind: 'openai_compatible',
    baseURL: 'https://ollama.com/v1',
    name: 'Ollama Cloud',
    help: 'Create a key at ollama.com/settings/keys and name a cloud model from Ollama’s catalogue. The traffic leaves this machine, so it cannot be used while Local models only is on.',
  },
  {
    id: 'opencode-zen',
    label: 'OpenCode Zen',
    kind: 'openai_compatible',
    baseURL: 'https://opencode.ai/zen/v1',
    name: 'OpenCode Zen',
    help: 'Pay as you go; use the key from opencode.ai/auth. Zen serves each model over one of three protocols, and only the models it serves over the OpenAI chat protocol work here (DeepSeek, GLM, Kimi, Mistral and its free models). Claude, GPT, Grok and Gemini models are served over other protocols and are not supported yet. The traffic leaves this machine, so it cannot be used while Local models only is on.',
  },
  {
    id: 'opencode-go',
    label: 'OpenCode Go',
    kind: 'openai_compatible',
    baseURL: 'https://opencode.ai/zen/go/v1',
    name: 'OpenCode Go',
    help: 'The subscription plan; use the key from opencode.ai/auth. Only the models OpenCode Go serves over the OpenAI chat protocol work here (GLM, Kimi, DeepSeek and MiMo, as its documentation lists them). Those it serves over the Anthropic Messages or the OpenAI Responses protocol (Claude, MiniMax, Qwen, GPT and Grok) are not supported yet. The traffic leaves this machine, so it cannot be used while Local models only is on.',
  },
  {
    id: 'claude-code',
    label: 'Claude, my own subscription (through Claude Code)',
    kind: 'claude_code',
    baseURL: '',
    name: 'Claude (my subscription)',
    help: 'Uses your own Claude plan through Claude Code, installed on the machine the controller runs on. Sign in there, as the user the controller runs as, with claude auth login: Zoomies never sees the sign-in. It is yours alone, because Anthropic’s terms are that each person uses their own plan, and what you ask goes to Anthropic, so it cannot be used while Local models only is on.',
    subscription: true,
  },
  {
    id: 'openai-compatible',
    label: 'Local or other OpenAI-compatible server (Ollama, LM Studio, vLLM, OpenRouter)',
    kind: 'openai_compatible',
    baseURL: '',
    name: '',
    help: 'A local server usually needs no key. A server on this machine or your network needs the private-address switch below the cards.',
  },
  { id: 'anthropic', label: 'Anthropic API', kind: 'anthropic', baseURL: '', name: '', help: '' },
  { id: 'openai', label: 'OpenAI API', kind: 'openai', baseURL: '', name: '', help: '' },
];

/** The preset that new providers start as. */
export const DEFAULT_PRESET = 'ollama-cloud';

const trim = (url: string) => url.trim().replace(/\/+$/, '').toLowerCase();

/**
 * The preset a saved provider is, so that editing one opens on the same choice
 * adding it did: the kind alone, unless the address is one a preset names.
 */
export function presetFor(
  kind: AssistantProviderKind,
  baseURL: string,
): ProviderPreset | undefined {
  const named = PRESETS.find(
    (p) => p.kind === kind && p.baseURL !== '' && trim(p.baseURL) === trim(baseURL),
  );
  return named ?? PRESETS.find((p) => p.kind === kind && p.baseURL === '');
}

/**
 * The provider that answers a person: the default, if they may use it, and
 * otherwise the first they may. It is the rule the controller applies when it is
 * not told which, written again here so that the page says who will answer before
 * anything is asked.
 */
export function answeringProvider(
  providers: readonly AssistantProvider[],
): AssistantProvider | undefined {
  const usable = providers.filter((p) => p.enabled && p.usable);
  return usable.find((p) => p.is_default) ?? usable[0];
}
