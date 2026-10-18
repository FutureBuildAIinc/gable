// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it, vi } from 'vitest';
import './shell-header.ts';
import type { GableShellHeader } from './shell-header.ts';
import type { ShellApp } from './shell-state.ts';

const APPS: ShellApp[] = [
  { id: 'desk', name: 'Desk', path: '/home', icon: 'grid', roles: [], places: [] },
  { id: 'pos', name: 'Point of Sale', path: '/pos', icon: 'receipt', roles: [], places: [] },
];

function mount(attrs?: (el: GableShellHeader) => void): GableShellHeader {
  const el = document.createElement('gable-shell-header') as GableShellHeader;
  el.apps = APPS;
  attrs?.(el);
  document.body.appendChild(el);
  return el;
}

function cleanup() {
  document.body.innerHTML = '';
}

describe('gable-shell-header', () => {
  it('leads with the dealer: the name, and the mark as an image or an initials tile', async () => {
    cleanup();
    const el = mount((e) => {
      e.dealerName = 'Gable Building Supply';
    });
    await el.updateComplete;
    expect(el.querySelector('header')?.textContent).toContain('Gable Building Supply');
    expect(el.querySelector('img')).toBeNull();
    expect(el.querySelector('header span[aria-hidden="true"]')?.textContent).toBe('GB');

    cleanup();
    const withMark = mount((e) => {
      e.dealerName = 'Gable Building Supply';
      e.dealerMarkUrl = '/api/v1/dealer-profile/logo';
    });
    await withMark.updateComplete;
    expect(withMark.querySelector('img')?.getAttribute('src')).toBe('/api/v1/dealer-profile/logo');
    cleanup();
  });

  it('centres the selector on the current app, with the framed title when there is one', async () => {
    const el = mount((e) => {
      e.currentAppId = 'desk';
      e.frameTitle = 'Orders';
    });
    await el.updateComplete;
    const control = el.querySelector<HTMLButtonElement>('button[data-action="selector"]');
    expect(control?.textContent?.replace(/\s+/g, ' ')).toContain('Desk · Orders');
    cleanup();
  });

  it('the selector opens, lists the apps, and a pick emits gable-shell-open-app', async () => {
    const el = mount((e) => {
      e.currentAppId = 'desk';
    });
    await el.updateComplete;
    const seen: { appId: string; path: string }[] = [];
    el.addEventListener('gable-shell-open-app', (e) => seen.push((e as CustomEvent).detail));

    const control = el.querySelector<HTMLButtonElement>('button[data-action="selector"]');
    control?.click();
    await el.updateComplete;
    expect(control?.getAttribute('aria-expanded')).toBe('true');
    const items = el.querySelectorAll<HTMLButtonElement>('[data-selector-panel] [role="menuitem"]');
    expect(items.length).toBe(2);
    // Each row carries the app's catalog icon; the current row's marker is a
    // quiet check, not a word.
    expect(items[0]?.querySelector('svg')).not.toBeNull();
    expect(items[0]?.textContent).not.toContain('here');

    items[1]?.click();
    await el.updateComplete;
    expect(seen).toEqual([{ appId: 'pos', path: '/pos' }]);
    expect(control?.getAttribute('aria-expanded')).toBe('false');
    cleanup();
  });

  it('the selector keyboard: ArrowDown opens, Escape closes and hands focus back', async () => {
    const el = mount((e) => {
      e.currentAppId = 'desk';
    });
    await el.updateComplete;
    const control = el.querySelector<HTMLButtonElement>('button[data-action="selector"]');
    control?.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
    await el.updateComplete;
    expect(control?.getAttribute('aria-expanded')).toBe('true');
    expect(el.querySelector('[data-selector-panel]')).not.toBeNull();

    const item = el.querySelector<HTMLButtonElement>('[data-selector-panel] [role="menuitem"]');
    item?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    await el.updateComplete;
    expect(el.querySelector('[data-selector-panel]')).toBeNull();
    await el.updateComplete;
    expect(document.activeElement).toBe(control);
    cleanup();
  });

  it('emits gable-shell-menu from the rail control and gable-shell-theme from the theme control', async () => {
    const el = mount();
    await el.updateComplete;
    const menu = vi.fn();
    const theme = vi.fn();
    el.addEventListener('gable-shell-menu', menu);
    el.addEventListener('gable-shell-theme', theme);
    el.querySelector<HTMLButtonElement>('button[data-action="menu"]')?.click();
    el.querySelector<HTMLButtonElement>('button[data-action="theme"]')?.click();
    expect(menu).toHaveBeenCalledTimes(1);
    expect(theme).toHaveBeenCalledTimes(1);
    cleanup();
  });

  it('feedback is disabled and silent until the framed app announces it', async () => {
    const el = mount();
    await el.updateComplete;
    const heard = vi.fn();
    el.addEventListener('gable-shell-feedback', heard);
    const button = el.querySelector<HTMLButtonElement>('button[data-action="feedback"]');
    expect(button?.disabled).toBe(true);
    button?.click();
    expect(heard).not.toHaveBeenCalled();

    el.feedbackOn = true;
    await el.updateComplete;
    expect(button?.disabled).toBe(false);
    button?.click();
    expect(heard).toHaveBeenCalledTimes(1);
    cleanup();
  });

  it('renders the framed app actions and emits their ids', async () => {
    const el = mount((e) => {
      e.frameActions = [{ id: 'new', label: 'New order' }];
    });
    await el.updateComplete;
    const heard: { id: string }[] = [];
    el.addEventListener('gable-shell-frame-action', (e) => heard.push((e as CustomEvent).detail));
    const button = el.querySelector<HTMLButtonElement>('button[data-action="frame-action"]');
    expect(button?.textContent).toContain('New order');
    button?.click();
    expect(heard).toEqual([{ id: 'new' }]);
    cleanup();
  });

  it('the yard control lists the yards and emits the pick', async () => {
    const el = mount((e) => {
      e.yards = [
        { id: 'b-1', code: 'KEL-MAIN', name: 'Kelowna Main Yard', is_home: true },
        { id: 'b-2', code: 'WK', name: 'West Kelowna Satellite' },
      ];
      e.currentYardId = 'b-1';
    });
    await el.updateComplete;
    const picks: (string | null)[] = [];
    el.addEventListener('gable-shell-yard', (e) => picks.push((e as CustomEvent).detail.branchId));

    const control = el.querySelector<HTMLButtonElement>('button[data-action="yards"]');
    expect(control?.textContent).toContain('Kelowna Main Yard');
    control?.click();
    await el.updateComplete;
    const options = el.querySelectorAll<HTMLButtonElement>('[role="option"]');
    expect(options.length).toBe(2);
    options[1]?.click();
    expect(picks).toEqual(['b-2']);
    cleanup();
  });

  it('renders no yard control when the person has no yards', async () => {
    const el = mount();
    await el.updateComplete;
    expect(el.querySelector('button[data-action="yards"]')).toBeNull();
    cleanup();
  });

  it('the account menu carries Switch role and Sign out', async () => {
    const el = mount((e) => {
      e.userName = 'Avery Brooks';
      e.userRoleLabel = 'Sales';
    });
    await el.updateComplete;
    const events: string[] = [];
    el.addEventListener('gable-shell-switch-role', () => events.push('switch'));
    el.addEventListener('gable-shell-sign-out', () => events.push('out'));

    el.querySelector<HTMLButtonElement>('button[data-action="account"]')?.click();
    await el.updateComplete;
    const items = el.querySelectorAll<HTMLButtonElement>('[data-account-menu] [role="menuitem"]');
    expect(items.length).toBe(2);
    expect(items[0]?.textContent).toContain('Switch role');
    expect(items[1]?.textContent).toContain('Sign out');
    items[0]?.click();
    items[1]?.click();
    expect(events).toEqual(['switch', 'out']);
    cleanup();
  });
});
