/**
 * What an operator does to a host from more than one page.
 *
 * One function, so the Hosts card menu and the host page's Next step panel word
 * a cordon the same way and put it back the same way. The command palette still
 * cordons through its own copy: it is shell code, so importing this would move
 * these lines into the app shell, and its toast copy is different.
 */
import { cordonHost, requestHostUpdate } from '../api/client';
import type { Host } from '../api/types';
import { fleet } from '../state/fleet.svelte';
import { toasts } from '../state/toasts.svelte';

/**
 * Cordon or uncordon a host. Optimistic: the cache shows the new state at once
 * and `fleet.optimistic` puts it back, and raises the error toast, if the
 * controller refuses -- so a caller never has to catch.
 */
export async function cordon(host: Pick<Host, 'id' | 'name'>, cordoned: boolean): Promise<void> {
  if (!host.id) return;
  const id = host.id;
  const name = host.name || id;
  const result = await fleet.optimistic(
    id,
    { cordoned },
    () => cordonHost(id, { cordoned }),
    cordoned ? `${name} was not cordoned` : `${name} was not uncordoned`,
  );
  if (result === undefined) return;
  if (cordoned) {
    toasts.info(
      `${name} cordoned`,
      'Its runners keep going and finish their jobs. No new runner will be placed here.',
    );
  } else {
    toasts.success(`${name} uncordoned`, 'The scheduler may place runners here again.');
  }
}

/**
 * Ask a host's agent to update itself. Not optimistic: the card says "Updating"
 * only once the controller has recorded the attempt, because showing it first
 * would claim a request the controller may still refuse (the mode is off, the
 * host is not behind, another administrator got there first). The answer is the
 * host with the attempt on it, and goes straight into the cache.
 *
 * Returns whether the controller took the request. A refusal is reported here,
 * in the controller's words, so a caller never has to catch.
 */
export async function requestUpdate(host: Pick<Host, 'id' | 'name'>): Promise<boolean> {
  if (!host.id) return false;
  try {
    fleet.ingestHosts([await requestHostUpdate(host.id)]);
    return true;
  } catch (cause) {
    toasts.fromError(cause, `${host.name || host.id} was not asked to update`);
    return false;
  }
}
