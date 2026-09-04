/**
 * Who is signed in, and what the server said about itself before they were.
 *
 * `/meta` is safe to call unauthenticated and decides what the first screen is:
 * the bootstrap form when no account exists yet, otherwise the login form,
 * otherwise the application.
 */
import {
  ApiError,
  bootstrap as bootstrapRequest,
  changeOwnPassword,
  getMeta,
  getSession,
  login as loginRequest,
  logout as logoutRequest,
} from '../api/client';
import type { Identity, Meta, Role } from '../api/types';
import { atLeast } from '../api/types';

export type SessionPhase =
  /** Still asking the server what it is. */
  | 'booting'
  /** No account exists; the first-run form is the only thing to show. */
  | 'bootstrap'
  /** Accounts exist but nobody is signed in. */
  | 'anonymous'
  /** Signed in, or authentication is switched off. */
  | 'ready'
  /** The controller could not be reached at all. */
  | 'failed';

class Session {
  #meta = $state<Meta | null>(null);
  #identity = $state<Identity | null>(null);
  #phase = $state<SessionPhase>('booting');
  #error = $state<ApiError | null>(null);

  get meta(): Meta | null {
    return this.#meta;
  }
  get identity(): Identity | null {
    return this.#identity;
  }
  get phase(): SessionPhase {
    return this.#phase;
  }
  /** Why the boot failed, when it did. */
  get error(): ApiError | null {
    return this.#error;
  }
  get role(): Role | undefined {
    return this.#identity?.role;
  }
  get authDisabled(): boolean {
    return this.#meta?.auth_disabled === true;
  }
  get oidcEnabled(): boolean {
    return this.#meta?.oidc_enabled === true;
  }
  /** The administrator reset this password and it has to change before anything else. */
  get mustChangePassword(): boolean {
    return this.#identity?.must_change_password === true;
  }
  /** A display name for the user menu, falling back to something truthful. */
  get displayName(): string {
    return this.#identity?.name ?? (this.authDisabled ? 'Authentication disabled' : 'Signed in');
  }

  /** True when the caller's role is at least `needed`. Gates mutating actions. */
  can(needed: Role): boolean {
    if (this.authDisabled) return true;
    return atLeast(this.#identity?.role, needed);
  }

  /** The boot sequence: what the server is, then who we are. */
  async boot(): Promise<void> {
    this.#phase = 'booting';
    this.#error = null;
    try {
      this.#meta = await getMeta();
    } catch (cause) {
      this.#error =
        cause instanceof ApiError
          ? cause
          : new ApiError({
              status: 0,
              code: 'internal',
              message: 'Could not reach the Zoomies controller.',
            });
      this.#phase = 'failed';
      return;
    }
    // Authentication being switched off wins over everything else. There is no
    // point creating the first administrator on a controller that will never
    // check who anybody is, and a first-run form on such an instance would be
    // a form that does nothing.
    if (this.#meta.auth_disabled) {
      this.#phase = 'ready';
      return;
    }
    if (this.#meta.bootstrap_required) {
      this.#phase = 'bootstrap';
      return;
    }
    await this.refresh();
  }

  /** Re-read the identity. Used after login, bootstrap and a password change. */
  async refresh(): Promise<void> {
    try {
      this.#identity = await getSession();
      this.#phase = 'ready';
    } catch (cause) {
      this.#identity = null;
      // A 401 is the ordinary "not signed in" answer. Only an unreachable
      // controller is a failure worth its own screen.
      if (cause instanceof ApiError && cause.isOffline) {
        this.#error = cause;
        this.#phase = 'failed';
      } else {
        this.#phase = 'anonymous';
      }
    }
  }

  /** Sign in. Throws an ApiError the form renders inline -- 401 and 429 both matter. */
  async login(username: string, password: string): Promise<void> {
    this.#identity = await loginRequest({ username, password });
    this.#phase = 'ready';
  }

  /** Create the first administrator. Only ever available while none exists. */
  async completeBootstrap(input: {
    username: string;
    password: string;
    email?: string;
  }): Promise<void> {
    this.#identity = await bootstrapRequest(input);
    this.#meta = this.#meta ? { ...this.#meta, bootstrap_required: false } : this.#meta;
    this.#phase = 'ready';
  }

  async changePassword(oldPassword: string | undefined, newPassword: string): Promise<void> {
    await changeOwnPassword({ old_password: oldPassword, new_password: newPassword });
    await this.refresh();
  }

  async logout(): Promise<void> {
    try {
      await logoutRequest();
    } finally {
      this.clear();
    }
  }

  /** Drop the identity without calling the server. The 401 handler uses this. */
  clear(): void {
    this.#identity = null;
    this.#phase = 'anonymous';
  }
}

export const session = new Session();
