/**
 * The update buttons' calls, each taken through the reads of the status so an
 * answer overtaken by the stream's own frame never puts an older picture back.
 * It holds no runes, so the order is something a unit test can drive.
 */
import type { FrameReads } from './frame-reads';

export interface UpdateCalls<T> {
  updateController: (tag: string) => Promise<T>;
  startRollout: () => Promise<T>;
  resumeRollout: () => Promise<T>;
  cancelRollout: () => Promise<T>;
}

/** Every button goes through `act`, the controller's Update as much as the rollout's. */
export function updateActions<T>(reads: FrameReads<T>, calls: UpdateCalls<T>) {
  return {
    updateController: (tag: string) => reads.act(() => calls.updateController(tag)),
    startRollout: () => reads.act(calls.startRollout),
    resumeRollout: () => reads.act(calls.resumeRollout),
    cancelRollout: () => reads.act(calls.cancelRollout),
  };
}
