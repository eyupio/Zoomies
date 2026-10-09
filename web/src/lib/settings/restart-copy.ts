/**
 * What `RestartWait` says in each half of a restart.
 *
 * The component is the same wherever a controller is stopped and started again;
 * what differs is why, and what an operator does when it goes wrong. The words
 * live beside the thing they are about (a restore's here, an update's in
 * `$lib/updates/words`) and are handed in, so that one restart is never
 * described in another's terms.
 */
export type RestartPhase = 'stopping' | 'starting' | 'back' | 'stuck-up' | 'stuck-down';

export interface RestartCopy {
  titles: Record<RestartPhase, string>;
  stopping: string;
  starting: string;
  back: string;
  /** Said once the controller has been given `seconds` to stop and still answers. */
  stuckUp: (seconds: number) => string;
  /** Said once it has been given `seconds` to start and has not. */
  stuckDown: (seconds: number) => string;
  /** What to run when it will not come back, shown as a command; none when there is not one. */
  command?: string;
}

/** A staged restore is applied by the next start, so these say what that start does. */
export const RESTORE_RESTART: RestartCopy = {
  titles: {
    stopping: 'Stopping the controller',
    starting: 'Waiting for it to start again',
    back: 'It is back',
    'stuck-up': 'The controller has not stopped',
    'stuck-down': 'The controller has not come back',
  },
  stopping:
    'The restore is applied by the next controller to start, before it opens the database. This page is watching for the process to stop.',
  starting:
    'It has stopped. A service manager starts it again in a few seconds; this page reloads the moment it answers. Everyone is signed out by the restore, so what loads is the sign-in page, and behind it a fleet held for recovery until you lift the fence.',
  back: 'Reloading.',
  stuckUp: (seconds) =>
    `${seconds} seconds on, the controller still answers. It was asked to stop and did not, which usually means a long-running request held it open. The restore stays staged: when the process does stop, the next start applies it.`,
  stuckDown: (seconds) =>
    `It stopped and nothing has started it in ${seconds} seconds. If it runs under systemd or a container with a restart policy, look at that; if you ran it by hand, start it again, the staged restore is applied when it starts, whoever starts it.`,
  command: 'zoomies controller',
};
