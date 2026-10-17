// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The shell chrome's pure state: no DOM, no Lit. The rail's posture over an
// injected storage seam, the rail's display mode by viewport width, the app
// catalog model by role, and the avatar initials. The elements in this
// package render what this module answers; the shell wires the two together.

/** The two operations a keyed storage seam needs, so tests and shells inject doubles. */
export interface KeyValueStorage {
  read(key: string): string | null;
  write(key: string, value: string): void;
}

/** An in-memory seam, for tests and for a hostile storage context. */
export function memoryStorage(): KeyValueStorage {
  const mem = new Map<string, string>();
  return {
    read: (key) => (mem.has(key) ? (mem.get(key) as string) : null),
    write: (key, value) => {
      mem.set(key, value);
    },
  };
}

// -- the rail's posture -----------------------------------------------------

/**
 * What the person chose for the rail: hidden (the default; it reveals on
 * hover or on a tap of the menu control), pinned as icons, or pinned wide.
 */
export type RailChoice = 'hidden' | 'icons' | 'wide';

export const RAIL_STATE_KEY = 'gable-shell.rail.v1';

/**
 * Silence and garbage mean hidden: only an explicit pin, written as one of
 * the two literals, keeps the rail open, and anything else degrades to the
 * default, never an error.
 */
export function railChoiceFromStored(stored: string | null): RailChoice {
  return stored === 'icons' || stored === 'wide' ? stored : 'hidden';
}

/** The literal a choice persists as. */
export function railChoiceToStored(choice: RailChoice): string {
  return choice;
}

// -- the rail's display mode ------------------------------------------------

/**
 * What the rail shows right now: nothing, the icons strip, or the wide rail.
 * The phone takes the rail away entirely; the narrow screen caps it at icons.
 */
export type RailMode = 'none' | 'icons' | 'wide';

/** Under this width the rail is the phone's: no rail at all. */
export const RAIL_PHONE_MAX = 640;

/** Under this width a pinned or revealed rail shows icons only. */
export const RAIL_NARROW_MAX = 1100;

/**
 * The display mode for a posture, a peek (hover or menu tap while hidden) and
 * a viewport width. A hidden rail revealed on a wide screen shows wide; on a
 * narrow screen it shows icons. A pinned wide rail on a narrow screen falls
 * to icons; on a phone every posture is none.
 */
export function railModeFor(input: { choice: RailChoice; peek: boolean; width: number }): RailMode {
  if (input.width < RAIL_PHONE_MAX) return 'none';
  const cap: RailMode = input.width < RAIL_NARROW_MAX ? 'icons' : 'wide';
  if (input.choice === 'hidden') return input.peek ? cap : 'none';
  if (input.choice === 'icons') return 'icons';
  return cap;
}

// -- the app catalog model ---------------------------------------------------

/** One top level place an app offers; the rail lists a few per app, never a nav tree. */
export interface ShellPlace {
  id: string;
  label: string;
  path: string;
}

/** One app the shell can frame: where its bundle's tree starts and who holds it. */
export interface ShellApp {
  id: string;
  name: string;
  /** The path the frame opens for the app itself. */
  path: string;
  /** The roles the app serves; an empty list serves every role. */
  roles: readonly string[];
  /** The few top level places the rail lists under the app. */
  places: readonly ShellPlace[];
}

/** The apps the person's roles hold, in catalog order. No roles, no apps. An app with no roles serves everyone. */
export function appsForRoles(apps: readonly ShellApp[], roles: readonly string[]): ShellApp[] {
  const held = new Set(roles);
  return apps.filter((a) => a.roles.length === 0 || a.roles.some((r) => held.has(r)));
}

/** The app by id, or undefined. */
export function appById(apps: readonly ShellApp[], id: string): ShellApp | undefined {
  return apps.find((a) => a.id === id);
}

/** Initials for the avatar chip and the dealer's fallback tile: the first letters of the first two words. */
export function initialsOf(name: string): string {
  const words = name.trim().split(/[\s@.]+/).filter(Boolean);
  if (words.length === 0) return '';
  return words
    .slice(0, 2)
    .map((w) => w[0]?.toUpperCase() ?? '')
    .join('');
}

// -- the banner's words -------------------------------------------------------

/** The banner's modes: nothing to say, or the connection is gone. */
export const SHELL_BANNER_MODES = ['', 'offline'] as const;

export type ShellBannerMode = (typeof SHELL_BANNER_MODES)[number];

/** The banner's one line for a mode. Gable's apps are online only, so the
 * words say what the screen can do, never that writes wait or send later. */
export function bannerWords(mode: ShellBannerMode): string {
  if (mode === 'offline') return 'You are offline; the shell and its apps need the connection.';
  return '';
}
