/** Explicit display facts only: callers must never pass credentials or whole API objects. */
export interface EliContext {
  kind: string;
  title: string;
  facts: Record<string, string | number | boolean | null | undefined>;
  question?: string;
}

export interface Suggestion {
  label: string;
  prompt: string;
}

export function contextPrompt(context: EliContext): string {
  const facts = Object.entries(context.facts)
    .filter(([, value]) => value !== undefined && value !== null && value !== '')
    .map(([key, value]) => `${key}: ${value}`)
    .join('\n');
  return `${context.question ?? 'Help me understand this situation, prioritise checks and suggest safe next steps.'}\n\nContext shared from the ${context.kind} UI: ${context.title}\n${facts}\n\nThese are a snapshot of displayed facts, not live access. Ask for missing evidence and distinguish facts from possible causes.`;
}

/** Add a topic here to extend the narrative without coupling pages to the chat view. */
export const NARRATIVES = [
  {
    match: /host|cpu|memory|overload|pressure|unhealthy/i,
    steps: [
      {
        label: 'Find the bottleneck',
        prompt:
          'For the host issue we are discussing, how can I distinguish CPU, memory and runtime pressure? What evidence should I collect first?',
      },
      {
        label: 'Reduce pressure safely',
        prompt:
          'Given the host situation above, suggest a safe sequence to reduce pressure while protecting running jobs. Explain the trade-offs.',
      },
      {
        label: 'Check recovery',
        prompt:
          'How should I verify that this host has recovered, and what would show that the underlying issue remains?',
      },
    ],
  },
  {
    match: /runner|fail|registration|container|docker/i,
    steps: [
      {
        label: 'Trace the failure',
        prompt:
          'For the runner issue above, help me trace the lifecycle and separate the immediate failure from its possible root causes. Which logs or events are needed?',
      },
      {
        label: 'Plan recovery',
        prompt:
          'Based on this runner discussion, propose recovery steps in order, including what to check before retrying and any risk to running work.',
      },
      {
        label: 'Prevent recurrence',
        prompt:
          'What changes could prevent this runner issue recurring? Tie each recommendation to the evidence in this conversation.',
      },
    ],
  },
  {
    match: /queue|pool|label|capacity|schedul/i,
    steps: [
      {
        label: 'Explain placement',
        prompt:
          'Using the context above, walk me through which pool and host placement constraints could be blocking this job. What facts are still missing?',
      },
      {
        label: 'Compare options',
        prompt:
          'For the queue or capacity issue above, compare the safest options to unblock work, including their trade-offs.',
      },
      {
        label: 'Verify scheduling',
        prompt: 'What should I observe to confirm that the scheduling issue above is resolved?',
      },
    ],
  },
];

export function followUps(
  turns: readonly { role: string; content: string; error?: string; streaming?: boolean }[],
): Suggestion[] {
  const last = turns.at(-1);
  if (!last || last.role !== 'assistant' || last.error || last.streaming || !last.content)
    return [];
  const asked = new Set(turns.filter((turn) => turn.role === 'user').map((turn) => turn.content));
  const question = [...turns].reverse().find((turn) => turn.role === 'user')?.content ?? '';
  const topic =
    NARRATIVES.find((topic) => topic.match.test(question.split('\n')[0] ?? question)) ??
    NARRATIVES.find((topic) => topic.match.test(last.content));
  const general = [
    {
      label: 'Make a checklist',
      prompt:
        'Turn your last answer into a prioritised checklist, using the evidence and constraints from our conversation.',
    },
    {
      label: 'Explain the trade-offs',
      prompt:
        'Explain the trade-offs in your last answer and which option best fits the situation we have discussed.',
    },
    {
      label: 'What is missing?',
      prompt:
        'What evidence is still missing from our discussion, and how should I collect it safely?',
    },
  ];
  const available = [...(topic?.steps ?? []), ...general]
    .filter((step) => !asked.has(step.prompt))
    .slice(0, 3);
  // A checklist or trade-off can be revisited after new evidence arrives.
  return available.length ? available : general;
}
