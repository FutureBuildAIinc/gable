// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest';
import { ACTIONS, HOTKEYS, chordFromEvent, keycap, legendKeys, resolveHotkey } from './hotkeys';

const press = (key: string, mods: { shift?: boolean; ctrl?: boolean; alt?: boolean; meta?: boolean } = {}) =>
  chordFromEvent({ key, shiftKey: !!mods.shift, ctrlKey: !!mods.ctrl, altKey: !!mods.alt, metaKey: !!mods.meta });

describe('the counter hotkey roster', () => {
  it('names every action the counter has, once each', () => {
    expect([...ACTIONS].sort()).toEqual([
      'add', 'back', 'clear-sale', 'customer', 'new-sale', 'next', 'pay', 'quantity', 'search',
      'tender-account', 'tender-card', 'tender-cash', 'tender-check', 'void',
    ]);
    const bound = HOTKEYS.map((h) => h.action);
    expect(new Set(bound).size).toBe(bound.length);
    expect([...bound].sort()).toEqual([...ACTIONS].sort());
  });

  it('binds no key twice', () => {
    const keys = HOTKEYS.map((h) => h.key);
    expect(new Set(keys).size).toBe(keys.length);
  });

  it('binds only keys a press can produce, and each one resolves to its own action', () => {
    for (const h of HOTKEYS) {
      expect(resolveHotkey(h.key)).toBe(h.action);
    }
  });

  it('shows every bound key in the legend, with its action, at every state', () => {
    for (const live of [() => true, () => false]) {
      const shown = legendKeys(live);
      expect(shown.map((k) => k.key).sort()).toEqual(HOTKEYS.map((h) => h.key).sort());
      for (const k of shown) {
        expect(HOTKEYS.find((h) => h.key === k.key)?.action).toBe(k.action);
        expect(k.label.length).toBeGreaterThan(0);
        expect(k.keycap.length).toBeGreaterThan(0);
      }
    }
  });

  it('dims what is not available and never moves it', () => {
    const all = legendKeys(() => true);
    const some = legendKeys((a) => a === 'pay');
    expect(some.map((k) => k.action)).toEqual(all.map((k) => k.action));
    expect(some.filter((k) => k.live).map((k) => k.action)).toEqual(['pay']);
  });
});

describe('the chord rules', () => {
  it('takes a function key alone or with shift', () => {
    expect(press('F2')).toBe('f2');
    expect(press('F4', { shift: true })).toBe('shift-f4');
  });

  it('takes a letter or digit only with Ctrl or Alt', () => {
    expect(press('a')).toBe('');
    expect(press('A', { shift: true })).toBe('');
    expect(press('7')).toBe('');
    expect(press('q', { alt: true })).toBe('alt-q');
    expect(press('k', { ctrl: true })).toBe('ctrl-k');
    expect(press('x', { ctrl: true, alt: true, shift: true })).toBe('shift-ctrl-alt-x');
  });

  it('never takes the meta key, a named key or a function key past F12', () => {
    expect(press('F2', { meta: true })).toBe('');
    expect(press('Enter')).toBe('');
    expect(press('Escape')).toBe('');
    expect(press('F13')).toBe('');
  });

  it('matches the exact chord only, so an extra modifier falls through to the browser', () => {
    expect(resolveHotkey('f12')).toBe('pay');
    expect(resolveHotkey('shift-f12')).toBeNull();
    expect(resolveHotkey('ctrl-f2')).toBeNull();
    expect(resolveHotkey('')).toBeNull();
  });

  it('spells a keycap for a person', () => {
    expect(keycap('f2')).toBe('F2');
    expect(keycap('shift-f4')).toBe('Shift+F4');
    expect(keycap('alt-q')).toBe('Alt+Q');
  });
});
