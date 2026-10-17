// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest';
import './shell-rail.ts';
import type { GableShellRail } from './shell-rail.ts';
import type { ShellApp } from './shell-state.ts';

const APPS: ShellApp[] = [
  {
    id: 'desk',
    name: 'Desk',
    path: '/home',
    roles: [],
    places: [
      { id: 'orders', label: 'Orders', path: '/orders' },
      { id: 'quotes', label: 'Quotes', path: '/quotes' },
    ],
  },
  { id: 'pos', name: 'Point of Sale', path: '/pos', roles: [], places: [] },
];

function mount(attrs?: (el: GableShellRail) => void): GableShellRail {
  const el = document.createElement('gable-shell-rail') as GableShellRail;
  el.apps = APPS;
  attrs?.(el);
  document.body.appendChild(el);
  return el;
}

function cleanup() {
  document.body.innerHTML = '';
}

describe('gable-shell-rail', () => {
  it('renders nothing at all in mode none (hidden, and the phone)', async () => {
    const el = mount((e) => {
      e.mode = 'none';
    });
    await el.updateComplete;
    expect(el.querySelector('nav')).toBeNull();
    cleanup();
  });

  it('hidden revealed (a peek) shows icons on a narrow screen', async () => {
    const el = mount((e) => {
      e.mode = 'icons';
      e.pinned = false;
    });
    await el.updateComplete;
    const nav = el.querySelector('nav');
    expect(nav?.getAttribute('data-mode')).toBe('icons');
    const rows = el.querySelectorAll<HTMLButtonElement>('button[data-row]');
    expect(rows.length).toBe(2);
    expect(rows[0]?.getAttribute('aria-label')).toBe('Desk');
    cleanup();
  });

  it('a pick emits gable-shell-rail-open with the app path', async () => {
    const el = mount((e) => {
      e.mode = 'icons';
    });
    await el.updateComplete;
    const seen: { appId: string; path: string }[] = [];
    el.addEventListener('gable-shell-rail-open', (e) => seen.push((e as CustomEvent).detail));
    el.querySelectorAll<HTMLButtonElement>('button[data-row]')[1]?.click();
    expect(seen).toEqual([{ appId: 'pos', path: '/pos' }]);
    cleanup();
  });

  it('pinned wide lists the apps and their few top level places', async () => {
    const el = mount((e) => {
      e.mode = 'wide';
      e.pinned = true;
      e.currentAppId = 'desk';
    });
    await el.updateComplete;
    const rows = [...el.querySelectorAll<HTMLButtonElement>('button[data-row]')];
    expect(rows.map((r) => r.textContent?.trim()).join(' ')).toContain('Orders');
    const places = el.querySelectorAll<HTMLButtonElement>('button[data-place]');
    expect(places.length).toBe(2);

    const seen: { appId: string; path: string }[] = [];
    el.addEventListener('gable-shell-rail-open', (e) => seen.push((e as CustomEvent).detail));
    places[0]?.click();
    expect(seen).toEqual([{ appId: 'desk', path: '/orders' }]);
    cleanup();
  });

  it('the pin controls emit the choice: pin as shown, toggle width, unpin to hidden', async () => {
    const el = mount((e) => {
      e.mode = 'icons';
      e.pinned = false;
    });
    await el.updateComplete;
    const choices: string[] = [];
    el.addEventListener('gable-shell-rail-choice', (e) => choices.push((e as CustomEvent).detail.choice));

    el.querySelector<HTMLButtonElement>('button[data-action="pin"]')?.click();
    expect(choices).toEqual(['icons']);

    el.pinned = true;
    el.mode = 'wide';
    await el.updateComplete;
    el.querySelector<HTMLButtonElement>('button[data-action="toggle-width"]')?.click();
    el.querySelector<HTMLButtonElement>('button[data-action="unpin"]')?.click();
    expect(choices).toEqual(['icons', 'icons', 'hidden']);
    cleanup();
  });

  it('ArrowUp and ArrowDown move between rows', async () => {
    const el = mount((e) => {
      e.mode = 'wide';
      e.pinned = true;
    });
    await el.updateComplete;
    const rows = el.querySelectorAll<HTMLButtonElement>('button[data-row]');
    rows[0]?.focus();
    rows[0]?.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
    expect(document.activeElement).toBe(rows[1]);
    rows[1]?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true }));
    expect(document.activeElement).toBe(rows[0]);
    cleanup();
  });
});
