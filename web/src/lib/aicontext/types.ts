import type { AIContextRepository } from '../api/types';

/** An installation this person may enable repositories for, by the name it is shown under. */
export type KnownInstallation = { id: string; target: string };

/**
 * What the card draws. An administrator or an installation's owner is given the
 * whole record; somebody who has only been made a reader of the source is given the
 * little that lets them use it, so the card has to cope with both.
 */
export type AiContextItem =
  | AIContextRepository
  | { id: string; full_name: string; instructions?: string; badge_markdown?: string };
