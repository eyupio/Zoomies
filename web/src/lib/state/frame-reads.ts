/**
 * Reads of a document that the event stream also sends whole, kept in order
 * against the frames. It holds no runes, so the order is something a unit test
 * can drive: the store that uses it keeps the reactive state.
 *
 * A read that began before a frame landed is older than that frame, and its
 * answer (or its failure) is dropped rather than put over the frame.
 */
export interface FrameReader<T> {
  get: () => Promise<T>;
  answer: (value: T) => void;
  failed: (cause: unknown) => void;
  /** Told when the first read starts and when the last one ends. */
  busy?: (loading: boolean) => void;
}

export class FrameReads<T> {
  #reader: FrameReader<T>;
  #frames = 0;
  #inFlight = 0;
  #asking: Promise<void> | null = null;

  constructor(reader: FrameReader<T>) {
    this.#reader = reader;
  }

  /** How many frames have been taken; a caller compares two readings. */
  get frames(): number {
    return this.#frames;
  }

  /** A read is in flight. */
  get loading(): boolean {
    return this.#inFlight > 0;
  }

  /** A frame was taken: every read that began before it is now older. */
  took(): void {
    this.#frames += 1;
  }

  /** Take a value that is as new as a frame, such as an action's answer, as a frame. */
  take(value: T): void {
    this.took();
    this.#reader.answer(value);
  }

  /**
   * Take the answer of an action that moves the document, as a frame.
   *
   * The controller starts a pass as it answers, and that pass's frame can
   * arrive before the answer does. Taken last, the answer would put the older
   * picture over the newer one until something else moved. So when a frame
   * landed while the call was out, the document is read again as well, and the
   * page ends on whichever is newest. That read is a fresh one: a read already
   * in flight began before the answer was taken, and its answer is dropped. A
   * refusal throws, and nothing is taken.
   */
  async act(call: () => Promise<T>): Promise<void> {
    const before = this.#frames;
    const value = await call();
    const overtaken = before !== this.#frames;
    this.take(value);
    if (overtaken) void this.fresh();
  }

  /**
   * Ask now. A second ask while one is in flight joins it, because the answer
   * it waits for is the same one.
   */
  refresh(): Promise<void> {
    return this.#asking ?? this.fresh();
  }

  /**
   * Ask now with a read of its own. For a caller that has just taken a frame:
   * a read in flight began before it, and joining that read would end on an
   * answer that is dropped.
   */
  fresh(): Promise<void> {
    const asking: Promise<void> = this.#read().finally(() => {
      if (this.#asking === asking) this.#asking = null;
    });
    this.#asking = asking;
    return asking;
  }

  async #read(): Promise<void> {
    const before = this.#frames;
    this.#inFlight += 1;
    if (this.#inFlight === 1) this.#reader.busy?.(true);
    try {
      const value = await this.#reader.get();
      if (before === this.#frames) this.#reader.answer(value);
    } catch (cause) {
      // A frame that landed meanwhile is an answer, and a good one.
      if (before === this.#frames) this.#reader.failed(cause);
    } finally {
      this.#inFlight -= 1;
      if (this.#inFlight === 0) this.#reader.busy?.(false);
    }
  }
}
