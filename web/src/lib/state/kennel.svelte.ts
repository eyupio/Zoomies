/**
 * Whether Kennel Club is on, for every page of its section at once.
 *
 * Four pages and a rail ask the same question, and the answer changes under them:
 * an administrator turns it off in one tab and the others must say so. The
 * overview document the controller sends whenever the summary changes
 * (`kennel.summary`) carries `enabled`, so the state follows the stream and is
 * asked for once, rather than each page asking and nobody agreeing.
 *
 * `epoch` moves each time the answer changes after it was known. The pages that
 * list repositories read it in the effect that fetches them, which is how they
 * come to ask again the moment Kennel Club is turned on, or to say it is off.
 */
import { ApiError, getKennelOverview, updateSettings } from '../api/client';
import { events } from '../api/sse';
import { KENNEL_ENABLED_KEY, kennelSwitchOutcome } from '../kennel/words';

/** What changing the setting came to: done, or a sentence a person can act on. */
export type KennelSwitchResult = { ok: true } | { ok: false; message: string };

class KennelClub {
  #enabled = $state<boolean | null>(null);
  #epoch = $state(0);
  #asking: Promise<void> | null = null;

  /** `null` until the controller has said. */
  get enabled(): boolean | null {
    return this.#enabled;
  }

  get epoch(): number {
    return this.#epoch;
  }

  /** Ask once. A page that mounts while another is asking waits for the same answer. */
  load(): Promise<void> {
    if (this.#enabled !== null) return Promise.resolve();
    this.#asking ??= getKennelOverview()
      .then((overview) => this.adopt(overview.enabled === true))
      .catch(() => {
        // Not knowing is a state the switch shows, and the next frame fixes it.
      })
      .finally(() => (this.#asking = null));
    return this.#asking;
  }

  /** Ask again, for when a frame may have been missed. A failure leaves what is known as it was. */
  refresh(): Promise<void> {
    return getKennelOverview()
      .then((overview) => this.adopt(overview.enabled === true))
      .catch(() => {
        // The next frame will say.
      });
  }

  /** Take an answer, and mark the change when it is one. */
  adopt(enabled: boolean): void {
    if (this.#enabled === enabled) return;
    const known = this.#enabled !== null;
    this.#enabled = enabled;
    if (known) this.#epoch += 1;
  }

  /**
   * Follow the stream for as long as the caller lives: the summary carries the
   * state, and a stream that lost its place asks again. Returns what stops it.
   */
  follow(): () => void {
    void this.load();
    const stops = [
      events.subscribe('kennel.summary', (overview) => this.adopt(overview.enabled === true)),
      events.subscribe('resync', () => void this.refresh()),
    ];
    return () => stops.forEach((stop) => stop());
  }

  /**
   * Turn it on or off, as an administrator does under Settings, and say what came
   * of it. The API answers with the setting as it now stands, which is the truth:
   * a value the database accepted can still lose to the environment, and a switch
   * that moved anyway would be showing what was asked for and not what is so.
   */
  async set(enabled: boolean): Promise<KennelSwitchResult> {
    try {
      const result = await updateSettings({ [KENNEL_ENABLED_KEY]: enabled } as Record<
        string,
        unknown
      >);
      const now = result.settings?.find((setting) => setting.key === KENNEL_ENABLED_KEY);
      const outcome = kennelSwitchOutcome(enabled, now);
      this.adopt(outcome.holds);
      if (outcome.message) return { ok: false, message: outcome.message };
      return { ok: true };
    } catch (cause) {
      if (cause instanceof ApiError) {
        const field = cause.fieldErrors()[KENNEL_ENABLED_KEY];
        return { ok: false, message: field ? `${KENNEL_ENABLED_KEY} ${field}` : cause.message };
      }
      return { ok: false, message: 'That change could not be made. Try again in a moment.' };
    }
  }
}

export const kennelClub = new KennelClub();
