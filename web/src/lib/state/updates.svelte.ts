/**
 * What the update mode would take, for the Settings page that shows it.
 *
 * The controller works the status out when asked and stores none of it: a soak
 * ends when the clock passes it and no row is written, so the only thing that
 * can tell an open page is the `updates.updated` event, which carries the same
 * document `GET /updates` returns. This follows the stream for as long as a page
 * asks it to, and reads the document again when the stream may have missed a
 * frame -- it said it lost its place, or it has just come back.
 *
 * The page reads mode and soak from here rather than from the settings: an
 * administrator is not sent the `updates.*` rows, and the status carries them
 * beside the sentence they explain.
 */
import { getUpdates, updateController } from '../api/client';
import { events } from '../api/sse';
import type { SseStatus } from '../api/sse';
import type { UpdatesStatus } from '../api/types';

class Updates {
  #status = $state<UpdatesStatus | null>(null);
  #error = $state<unknown>(null);
  #loading = $state(false);
  #stream = $state<SseStatus>(events.status);
  #asking: Promise<void> | null = null;
  /**
   * How many frames have been taken. A read that began before one landed is
   * older than it, and must not put its answer over it.
   */
  #frames = 0;

  /** `null` until the controller has answered once. */
  get status(): UpdatesStatus | null {
    return this.#status;
  }

  /** Why the last read failed, or `null` when the last answer was good. */
  get error(): unknown {
    return this.#error;
  }

  /** A read is in flight. */
  get loading(): boolean {
    return this.#loading;
  }

  /**
   * The event stream's state while a page follows it. An update restarts the
   * controller, and this is how the page learns of that: it drops, and comes
   * back when the new process answers. It is the connection and not a probe of
   * `/healthz`, because a restart quicker than a probe's interval falls between
   * two probes and is never seen.
   */
  get stream(): SseStatus {
    return this.#stream;
  }

  /**
   * Ask the update helper to take the controller to `tag`. The answer is the
   * status with the attempt in it, and is taken as a frame would be: the
   * attempt stays open until the controller closes it, and that arrives as a
   * frame of its own. A refusal throws, with the controller's sentence in it.
   */
  async updateController(tag: string): Promise<void> {
    this.adopt(await updateController(tag));
  }

  /**
   * Ask now. A second ask while one is in flight joins it, and a failure leaves
   * what is known as it was and says so through `error`.
   */
  refresh(): Promise<void> {
    this.#asking ??= this.#read().finally(() => (this.#asking = null));
    return this.#asking;
  }

  async #read(): Promise<void> {
    const before = this.#frames;
    this.#loading = true;
    try {
      const status = await getUpdates();
      if (before === this.#frames) this.#status = status;
      this.#error = null;
    } catch (cause) {
      // A frame that landed meanwhile is an answer, and a good one.
      if (before === this.#frames) this.#error = cause;
    } finally {
      this.#loading = false;
    }
  }

  /** Take a frame from the stream, which is the newest thing there is. */
  adopt(status: UpdatesStatus): void {
    this.#frames += 1;
    this.#status = status;
    this.#error = null;
  }

  /**
   * Follow the stream for as long as the caller lives, and read the document
   * now. The stream opens after the page has loaded and replays nothing to it,
   * so a change in between would never be heard; asking again whenever it comes
   * up is the answer the other live pages give. Returns what stops it.
   */
  follow(): () => void {
    void this.refresh();
    let before: SseStatus | null = null;
    const stops = [
      events.subscribe('updates.updated', (status) => this.adopt(status)),
      events.subscribe('resync', () => void this.refresh()),
      events.onStatus((next) => {
        this.#stream = next;
        if (next === 'live' && before !== null && before !== 'live') void this.refresh();
        before = next;
      }),
    ];
    return () => stops.forEach((stop) => stop());
  }
}

export const updates = new Updates();
