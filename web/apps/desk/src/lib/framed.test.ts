// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The desk's framed mode, standalone half: with no shell framing this page
 * (the jsdom window is top level and carries no flag), bootFramedMode listens
 * for nothing and posts nothing, and orphanedAtBoot is false.
 */
import { describe, expect, it, vi } from 'vitest';
import { bootFramedMode, orphanedAtBoot, titleForPath } from './framed.ts';

describe('titleForPath', () => {
  it('names the known route trees and falls back to the first segment in words', () => {
    expect(titleForPath('/home')).toBe('Home');
    expect(titleForPath('/orders')).toBe('Orders');
    expect(titleForPath('/quotes')).toBe('Quotes');
    expect(titleForPath('/yard')).toBe('Yard');
    expect(titleForPath('/accounts')).toBe('Accounts');
    expect(titleForPath('/')).toBe('Home');
  });
});

describe('bootFramedMode standalone', () => {
  it('is a no-op: no message leaves, no sign out listener lands', () => {
    const posted: unknown[] = [];
    const spy = vi.spyOn(window.parent, 'postMessage').mockImplementation((m: unknown) => posted.push(m));
    const added: string[] = [];
    const addSpy = vi.spyOn(window, 'addEventListener');
    try {
      bootFramedMode();
      expect(posted).toEqual([]);
      expect(added).toEqual([]);
      expect(orphanedAtBoot()).toBe(false);
    } finally {
      spy.mockRestore();
      addSpy.mockRestore();
    }
  });
});
