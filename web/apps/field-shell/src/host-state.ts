// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The field host's pure state: the door (sign in) or the field frame (the
// compact phone top bar and bottom tab bar around one framed field app). Sign
// in lands the role on the first field app it holds (Yard for warehouse,
// Driver for driver); Switch role and Sign out drop the session and return to
// the door. The yard and the theme are never touched here: they are the
// person's own stores, shared with the frame on this origin, so an app switch
// keeps both.

import { appsForRoles, type ShellApp } from '@gable/shell-chrome';

/** The signed in person as the host holds it, in memory only. */
export interface HostSession {
  /** The display name (the token's, or the dev session's). */
  readonly name: string;
  /** The role in words, for the account chip. */
  readonly roleLabel: string;
  /** The roles the catalog filters with. */
  readonly roles: readonly string[];
}

export type HostState =
  | { readonly phase: 'door' }
  | {
      readonly phase: 'field';
      readonly session: HostSession;
      /** What the role's bottom tab bar holds, in the catalog's order. */
      readonly apps: readonly ShellApp[];
      /** The framed app, or null when the role holds none. */
      readonly currentAppId: string | null;
      /** The path the frame shows (the app's, or a place's under it). */
      readonly currentPath: string;
    };

export function initialState(): HostState {
  return { phase: 'door' };
}

/** Sign in: the role's field apps from the catalog, landed on the first. */
export function signedIn(session: HostSession, catalog: readonly ShellApp[]): HostState {
  const held = appsForRoles(catalog, session.roles);
  const first = held[0];
  return {
    phase: 'field',
    session,
    apps: held,
    currentAppId: first?.id ?? null,
    currentPath: first?.path ?? '',
  };
}

/** Open an app the role holds, at its own path or at a place under it. Anything else changes nothing. */
export function openApp(state: HostState, appId: string, path?: string): HostState {
  if (state.phase !== 'field') return state;
  const app = state.apps.find((a) => a.id === appId);
  if (app === undefined) return state;
  const nextPath = path ?? app.path;
  if (state.currentAppId === appId && state.currentPath === nextPath) return state;
  return { ...state, currentAppId: appId, currentPath: nextPath };
}

/** Switch role and Sign out: the session is dropped and the door is back, from any app. */
export function switchRole(_state: HostState): HostState {
  return { phase: 'door' };
}
