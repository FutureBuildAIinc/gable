// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The identity tap: the last signed in user's display name, role and yard,
 * kept for a "Continue as" card on the next visit. The display block only:
 * a token is never copied here, and the record lives under its own key, so
 * the tap never touches custody's handoff or any credential.
 */

import type { StorageLike } from './custody.ts';

/** The tap's storage key. */
export const IDENTITY_TAP_KEY = 'gable.identity-tap.v1';

/** The display block the tap keeps: who, as what, where. */
export interface TappedIdentity {
  displayName: string;
  role: string | null;
  yard: string | null;
}

interface TapRecord {
  v: 1;
  displayName: string;
  role?: string;
  yard?: string;
}

/** Remember the last signed in user's display block. Never a token. */
export function recordIdentity(storage: StorageLike, identity: TappedIdentity): void {
  const record: TapRecord = { v: 1, displayName: identity.displayName };
  if (identity.role !== null) record.role = identity.role;
  if (identity.yard !== null) record.yard = identity.yard;
  try {
    storage.setItem(IDENTITY_TAP_KEY, JSON.stringify(record));
  } catch {
    // A hostile context (storage full or blocked) degrades to remembering
    // nothing; the card simply does not show.
  }
}

/** The remembered identity, or null when none (or an unreadable record). */
export function readIdentity(storage: StorageLike): TappedIdentity | null {
  let raw: string | null;
  try {
    raw = storage.getItem(IDENTITY_TAP_KEY);
  } catch {
    return null;
  }
  if (raw === null) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return null;
  const record = parsed as Record<string, unknown>;
  if (record['v'] !== 1) return null;
  if (typeof record['displayName'] !== 'string' || record['displayName'] === '') return null;
  const role = typeof record['role'] === 'string' ? record['role'] : null;
  const yard = typeof record['yard'] === 'string' ? record['yard'] : null;
  return { displayName: record['displayName'] as string, role, yard };
}

/** Forget the remembered identity (sign out calls this). */
export function forgetIdentity(storage: StorageLike): void {
  try {
    storage.removeItem(IDENTITY_TAP_KEY);
  } catch {
    // Nothing to do: a hostile context already remembers nothing.
  }
}
