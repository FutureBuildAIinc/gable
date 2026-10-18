// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The yard the field shell's top bar carries while it frames an app. The list
// is the same endpoint the desk's BranchContext reads; the choice is the same
// localStorage key the desk's fetchClient stamps onto every request, so a
// yard picked in the shell scopes the frame's next call without any app
// change, and the gable-work-shell:yard-changed message lets the frame's own
// branch context follow for its display.

import { BRANCH_STORAGE_KEY, fetchWithAuth } from '@gable/auth';
import type { ShellYard } from '@gable/shell-chrome';

/** The wire shape of one row of GET /api/v1/me/branches (a bare array of these). */
interface BranchRow {
  id: string;
  code: string;
  name: string;
  is_home?: boolean;
}

/** The yards the signed in person can stand in. A failure answers none: the
 * header renders no yard control rather than a wrong one. */
export async function loadYards(fetcher: typeof fetchWithAuth = fetchWithAuth): Promise<ShellYard[]> {
  try {
    const response = await fetcher('/api/v1/me/branches');
    if (!response.ok) return [];
    const rows = (await response.json()) as unknown;
    if (!Array.isArray(rows)) return [];
    const yards: ShellYard[] = [];
    for (const row of rows as BranchRow[]) {
      if (row === null || typeof row !== 'object') continue;
      if (typeof row.id !== 'string' || typeof row.code !== 'string') continue;
      yards.push({
        id: row.id,
        code: row.code,
        name: typeof row.name === 'string' ? row.name : row.code,
        is_home: row.is_home === true,
      });
    }
    return yards;
  } catch {
    return [];
  }
}

/** The yard the person last picked, or null for all yards. Never throws. */
export function currentYardId(): string | null {
  try {
    return globalThis.localStorage?.getItem(BRANCH_STORAGE_KEY) ?? null;
  } catch {
    return null;
  }
}

/** Persist the pick where the frame's fetch client reads it. */
export function storeYard(branchId: string | null): void {
  try {
    if (branchId === null) globalThis.localStorage?.removeItem(BRANCH_STORAGE_KEY);
    else globalThis.localStorage?.setItem(BRANCH_STORAGE_KEY, branchId);
  } catch {
    // A hostile storage context: the pick stands for this document only.
  }
}
