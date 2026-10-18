// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest';
import { AuthCustody } from '@gable/auth';
import { roleInWords, sessionFromCustody } from './session.ts';

function memoryStorage() {
  const mem = new Map<string, string>();
  return {
    getItem: (k: string) => (mem.has(k) ? (mem.get(k) as string) : null),
    setItem: (k: string, v: string) => void mem.set(k, v),
    removeItem: (k: string) => void mem.delete(k),
  };
}

/** A structurally valid JWT the custody will adopt (never a real credential: invented claims, no signature). */
function fakeToken(claims: Record<string, unknown>): string {
  const seg = (v: unknown) => btoa(JSON.stringify(v)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
  return `${seg({ alg: 'none', typ: 'JWT' })}.${seg(claims)}.${seg({})}`;
}

describe('sessionFromCustody', () => {
  it('is null when nobody is signed in', () => {
    expect(sessionFromCustody(new AuthCustody(memoryStorage()))).toBeNull();
  });

  it('maps a token session: the display name and the roles in words', () => {
    const custody = new AuthCustody(memoryStorage());
    custody.signInWithToken(
      fakeToken({ sub: 'u-1', email: 'avery@example.com', roles: ['sales', 'cashier'], exp: 4102444800 }),
    );
    const session = sessionFromCustody(custody);
    expect(session).not.toBeNull();
    expect(session?.name).toBe('avery@example.com');
    expect(session?.roles).toEqual(['sales', 'cashier']);
    expect(session?.roleLabel).toBe('Sales · Cashier');
  });

  it('roleInWords capitalises a slug', () => {
    expect(roleInWords('cashier')).toBe('Cashier');
    expect(roleInWords('')).toBe('');
  });
});
