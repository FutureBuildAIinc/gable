// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The staff sign in against the core (ADR 0012 section 3). The core is the
 * only caller of Appwrite: these calls go to the core's own routes, the
 * session is the HttpOnly cookie the sign in answer sets, and the only thing
 * kept in script is display data (see custody.ts). Every call is made with
 * plain `fetch`, not `fetchWithAuth`, so a 401 from the sign in itself is an
 * answer to show, not a session expiring.
 */

import { authCustody } from './custody.ts';
import { authConfig } from './config.ts';

/** The custom header the core requires on a cookie authenticated write. */
export const CSRF_HEADER = 'X-Gable-CSRF';

export const SIGN_IN_PATH = '/api/v1/auth/signin';
export const SIGN_OUT_PATH = '/api/v1/auth/signout';
export const SESSION_PATH = '/api/v1/auth/session';

export type StaffSignInResult =
  | { ok: true }
  | { ok: false; message: string };

function apiBase(): string {
  const env = (import.meta as ImportMeta & { env?: Record<string, string | undefined> }).env;
  return env?.VITE_API_URL ?? '';
}

function isStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((v) => typeof v === 'string');
}

/** Read the core's {sub, roles} answer field by field; null when it is not one. */
function parseIdentity(raw: unknown): { sub: string; roles: string[] } | null {
  if (raw === null || typeof raw !== 'object') return null;
  const rec = raw as Record<string, unknown>;
  if (typeof rec['sub'] !== 'string' || rec['sub'] === '') return null;
  if (!isStringArray(rec['roles'])) return null;
  return { sub: rec['sub'], roles: rec['roles'] };
}

/** The plain words for a refusal; the core's fixed answer never says why. */
function refusalMessage(resp: Response): string {
  if (resp.status === 429) {
    const secs = Number.parseInt(resp.headers.get('Retry-After') ?? '', 10);
    return Number.isFinite(secs) && secs > 0
      ? `Too many attempts. Try again in ${secs} seconds.`
      : 'Too many attempts. Try again in a few minutes.';
  }
  if (resp.status === 503) return 'Sign in is unavailable right now. Try again shortly.';
  if (resp.status === 401) {
    return 'That email and password were not accepted, or this account cannot sign in here. Ask an administrator.';
  }
  return 'Sign in failed.';
}

/**
 * Sign in with email and password. On success the cookie is set by the
 * answer and custody holds the display data; nothing credential shaped is
 * returned or kept.
 */
export async function signInStaff(email: string, password: string): Promise<StaffSignInResult> {
  let resp: Response;
  try {
    resp = await fetch(`${apiBase()}${SIGN_IN_PATH}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', Accept: 'application/json', [CSRF_HEADER]: '1' },
      credentials: 'same-origin',
      body: JSON.stringify({ email, password }),
    });
  } catch {
    return { ok: false, message: 'The server could not be reached.' };
  }
  if (!resp.ok) return { ok: false, message: refusalMessage(resp) };
  let identity: { sub: string; roles: string[] } | null = null;
  try {
    identity = parseIdentity(await resp.json());
  } catch {
    identity = null;
  }
  if (identity === null) return { ok: false, message: 'Sign in failed.' };
  authCustody.signInStaff({ ...identity, name: email });
  return { ok: true };
}

/**
 * Sign out: revoke the session at the core, then clear what custody holds.
 * Custody is cleared even when the call fails, so the screen never shows a
 * person as signed in after they asked to leave; the core's session then
 * ends at its own idle limit.
 */
export async function signOutStaff(): Promise<void> {
  try {
    await fetch(`${apiBase()}${SIGN_OUT_PATH}`, {
      method: 'POST',
      headers: { [CSRF_HEADER]: '1' },
      credentials: 'same-origin',
    });
  } catch {
    // Unreachable core: the local copy still goes.
  } finally {
    authCustody.signOut();
  }
}

/**
 * A fresh tab has no display data but may hold a live cookie: ask the core
 * who it is. Returns true when custody now holds a staff session. A 401 or
 * any failure leaves custody as it was.
 */
export async function restoreStaffSession(): Promise<boolean> {
  if (authCustody.session !== null) return true;
  try {
    const resp = await fetch(`${apiBase()}${SESSION_PATH}`, {
      headers: { Accept: 'application/json' },
      credentials: 'same-origin',
    });
    if (!resp.ok) return false;
    const identity = parseIdentity(await resp.json());
    if (identity === null) return false;
    authCustody.signInStaff(identity);
    return true;
  } catch {
    return false;
  }
}

/**
 * Every document boot (the desk and both shells; the door does the same in
 * its own element) asks the core who the cookie belongs to before it mounts,
 * because custody holds a staff session in memory only. A dev build has no
 * cookie to ask about. Never throws: a failure leaves custody signed out.
 */
export async function bootStaffSession(): Promise<void> {
  if (authConfig.devMode) return;
  await restoreStaffSession();
}
