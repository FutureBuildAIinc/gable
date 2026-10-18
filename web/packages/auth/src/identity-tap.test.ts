// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The identity tap: the display block of the last signed in user is kept
 * under its own key and read back; a token never reaches storage through it.
 */
import { describe, it, expect } from 'vitest';
import { recordIdentity, readIdentity, forgetIdentity, IDENTITY_TAP_KEY } from './identity-tap.ts';
import type { StorageLike } from './custody.ts';

/** A storage double whose whole contents the test can inspect. */
function memoryStorage(): StorageLike & { dump(): Map<string, string> } {
  const mem = new Map<string, string>();
  return {
    getItem: (k) => (mem.has(k) ? (mem.get(k) as string) : null),
    setItem: (k, v) => void mem.set(k, v),
    removeItem: (k) => void mem.delete(k),
    dump: () => mem,
  };
}

describe('identity tap', () => {
  it('records and reads back the display name, role and yard', () => {
    const storage = memoryStorage();
    recordIdentity(storage, { displayName: 'Morgan Hale', role: 'owner', yard: 'KEL-MAIN' });
    expect(readIdentity(storage)).toEqual({ displayName: 'Morgan Hale', role: 'owner', yard: 'KEL-MAIN' });
  });

  it('records and reads back the sign in method when given, and drops an unknown method code', () => {
    const storage = memoryStorage();
    recordIdentity(storage, { displayName: 'Morgan Hale', role: 'owner', yard: 'KEL-MAIN', method: 'google' });
    expect(readIdentity(storage)).toEqual({
      displayName: 'Morgan Hale',
      role: 'owner',
      yard: 'KEL-MAIN',
      method: 'google',
    });

    storage.setItem(
      IDENTITY_TAP_KEY,
      JSON.stringify({ v: 1, displayName: 'Morgan Hale', method: 'not_a_method' }),
    );
    expect(readIdentity(storage)).toEqual({ displayName: 'Morgan Hale', role: null, yard: null });
  });

  it('keeps nulls for a missing role or yard', () => {
    const storage = memoryStorage();
    recordIdentity(storage, { displayName: 'Someone', role: null, yard: null });
    expect(readIdentity(storage)).toEqual({ displayName: 'Someone', role: null, yard: null });
  });

  it('forgets the record', () => {
    const storage = memoryStorage();
    recordIdentity(storage, { displayName: 'Someone', role: null, yard: null });
    forgetIdentity(storage);
    expect(readIdentity(storage)).toBeNull();
  });

  it('never writes a token: after a sign in the storage holds only the display block', () => {
    const storage = memoryStorage();
    // A sign in hands the tap the display block alone; the token lives in
    // custody's memory, never here.
    const token = 'eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJkZW1vLW93bmVyIn0.sig';
    recordIdentity(storage, { displayName: 'Morgan Hale', role: 'owner', yard: 'KEL-MAIN' });
    const stored = JSON.stringify([...storage.dump().entries()]);
    expect(stored).not.toContain(token);
    expect(stored).not.toContain('token');
    expect(stored).not.toContain('eyJ');
    expect(storage.dump().has(IDENTITY_TAP_KEY)).toBe(true);
  });

  it('answers null for an unreadable or foreign record', () => {
    const storage = memoryStorage();
    storage.setItem(IDENTITY_TAP_KEY, 'not json');
    expect(readIdentity(storage)).toBeNull();
    storage.setItem(IDENTITY_TAP_KEY, JSON.stringify({ v: 2, displayName: 'X' }));
    expect(readIdentity(storage)).toBeNull();
    storage.setItem(IDENTITY_TAP_KEY, JSON.stringify({ v: 1 }));
    expect(readIdentity(storage)).toBeNull();
  });
});
