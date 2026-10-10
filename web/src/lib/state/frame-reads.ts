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
