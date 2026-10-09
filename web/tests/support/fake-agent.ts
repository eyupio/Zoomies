/**
 * A fake agent for the specs that need a host to answer.
 *
 * It enrols the way a real agent does (a join token from the Add a host page),
 * then does what an agent does on a loop: heartbeat, long-poll for tasks, and
 * post a result. The seeded embedded host is never used for this, because it
 * really answers.
 *
 * Features are sent on every beat, because a beat that leaves them out withdraws
 * them: that is how the controller learns an agent no longer offers something.
 */
import { expect, type Page } from '@playwright/test';
import { goto } from './fixtures';

export type Credentials = { host_id: string; agent_token: string };

export type Check = {
  id: string;
  title: string;
  tier: 'safe' | 'aggressive' | 'dedicated';
  status: 'ok' | 'warn' | 'skip' | 'error';
  current: string;
  recommended: string;
  rationale: string;
  actionable: boolean;
};

export function check(
  id: string,
  title: string,
  status: Check['status'],
  extra: Partial<Check> = {},
): Check {
  return {
    id,
    title,
    tier: 'safe',
    status,
    current: '',
    recommended: '',
    rationale: `${title}: why it matters.`,
    actionable: false,
    ...extra,
  };
}

/**
 * Enrol a host through the Add a host page's own join token.
 *
 * `version` is the build the agent says it is, which a spec about updating sets
 * to a release behind the controller's.
 */
export async function enrolAgent(page: Page, name: string, version = 'dev'): Promise<Credentials> {
  await goto(page, '/hosts/new', 'Add a host');
  await page.getByRole('button', { name: 'Get the command' }).click();
  const command = await page
    .getByRole('region', { name: 'Run this on the new host' })
    .locator('pre', { hasText: 'zoomies.sh/install.sh' })
    .innerText();
  const token = /--join-token\s+['"]?(zoojoin_[^'"\s]+)/.exec(command)?.[1];
  const join = await page.request.post('/api/v1/agent/join', {
    data: {
      protocol_version: 1,
      join_token: token,
      name,
      capacity: 1,
      os: 'linux',
      arch: 'amd64',
      version,
      backends: [{ kind: 'docker', available: true }],
    },
  });
  expect(join.ok()).toBeTruthy();
  return (await join.json()) as Credentials;
}

export type Task = { id: string; kind: string };

/** What the agent does with a task: answer it, fail it, or say nothing at all. */
export type Answer =
  { kind: 'report'; results: Check[] } | { kind: 'failure'; error: string } | { kind: 'silence' };

export class FakeAgent {
  /** Every check_host task this agent was handed, in order. */
  readonly checks: Task[] = [];
  private running = false;
  private loop: Promise<void> | null = null;
  private gate: Promise<void> | null = null;

  constructor(
    private readonly page: Page,
    readonly credentials: Credentials,
    /** Sent on every beat. An empty list is an agent that cannot check on request. */
    private features: string[] = ['host-check'],
    private answer: (task: Task) => Answer = () => ({ kind: 'report', results: [] }),
  ) {}

  get id(): string {
    return this.credentials.host_id;
  }

  private headers() {
    return { Authorization: `Bearer ${this.credentials.agent_token}` };
  }

  /** One heartbeat, carrying a report when given one. */
  async beat(results?: Check[]): Promise<void> {
    const response = await this.page.request.post('/api/v1/agent/heartbeat', {
      headers: this.headers(),
      data: {
        protocol_version: 1,
        features: this.features,
        ...(results ? { doctor: this.report(results) } : {}),
      },
    });
    expect(response.ok()).toBeTruthy();
  }

  report(results: Check[]) {
    return {
      checked_at: new Date().toISOString(),
      os: 'linux',
      distro: 'ubuntu 24.04',
      container: false,
      reboot_pending: false,
      results,
    };
  }

  setAnswer(answer: (task: Task) => Answer): void {
    this.answer = answer;
  }

  /** Hold the next answer until the returned function is called. */
  hold(): () => void {
    let release!: () => void;
    this.gate = new Promise<void>((resolve) => (release = resolve));
    return release;
  }

  start(): void {
    if (this.running) return;
    this.running = true;
    this.loop = this.run();
  }

  async stop(): Promise<void> {
    this.running = false;
    await this.loop?.catch(() => undefined);
    this.loop = null;
  }

  private async run(): Promise<void> {
    while (this.running) {
      try {
        await this.beat();
        const polled = await this.page.request.get('/api/v1/agent/tasks?wait=1', {
          headers: this.headers(),
        });
        if (!polled.ok()) {
          console.error('fake agent poll refused:', polled.status());
          return;
        }
        const batch = (await polled.json()) as { tasks?: Task[] };
        for (const task of batch.tasks ?? []) await this.handle(task);
      } catch (cause) {
        // The page or the host went away under the loop: the test is over.
        console.error('fake agent stopped:', String(cause).slice(0, 300));
        return;
      }
    }
  }

  private async handle(task: Task): Promise<void> {
    if (task.kind !== 'check_host') return;
    this.checks.push(task);
    const gate = this.gate;
    if (gate) {
      this.gate = null;
      await gate;
    }
    const answer = this.answer(task);
    if (answer.kind === 'silence') return;
    const body =
      answer.kind === 'report'
        ? { task_id: task.id, kind: task.kind, ok: true, doctor: this.report(answer.results) }
        : { task_id: task.id, kind: task.kind, ok: false, error: answer.error };
    const response = await this.page.request.post('/api/v1/agent/results', {
      headers: this.headers(),
      data: body,
    });
    expect(response.ok()).toBeTruthy();
  }
}
