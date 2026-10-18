// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest';
import './shell-tabs.ts';
import type { GableShellTabs } from './shell-tabs.ts';
import type { ShellApp } from './shell-state.ts';

const APPS: ShellApp[] = [
  { id: 'yard', name: 'Yard', path: '/yard', icon: 'warehouse', roles: ['warehouse', 'admin', 'owner'], places: [] },
  { id: 'driver', name: 'Driver', path: '/driver', icon: 'truck', roles: ['driver', 'admin', 'owner'], places: [] },
];

function mount(apps = APPS, current = 'yard'): GableShellTabs {
  const el = document.createElement('gable-shell-tabs') as GableShellTabs;
  el.apps = apps;
  el.currentAppId = current;
  document.body.appendChild(el);
  return el;
}

function cleanup() {
  document.body.innerHTML = '';
}

describe('gable-shell-tabs', () => {
  it('renders nothing when there are no apps', async () => {
    const el = mount([]);
    await el.updateComplete;
    expect(el.querySelector('nav')).toBeNull();
    cleanup();
  });

  it('renders a tab per app with at least 44 px touch targets and emits gable-shell-open-app on pick', async () => {
    const el = mount(APPS, 'yard');
    await el.updateComplete;
    const seen: { appId: string; path: string }[] = [];
    el.addEventListener('gable-shell-open-app', (e) => seen.push((e as CustomEvent).detail));

    const tabs = el.querySelectorAll<HTMLButtonElement>('button[data-tab]');
    expect(tabs.length).toBe(2);
    expect(tabs[0]?.getAttribute('aria-current')).toBe('page');
    expect(tabs[1]?.getAttribute('aria-current')).toBe('false');
    expect(tabs[0]?.className).toContain('min-h-[48px]');
    expect(tabs[0]?.className).toContain('min-w-[44px]');

    tabs[1]?.click();
    expect(seen).toEqual([{ appId: 'driver', path: '/driver' }]);
    cleanup();
  });

  it('moves focus across tabs with arrow, Home and End keys', async () => {
    const el = mount(APPS, 'yard');
    await el.updateComplete;
    const tabs = el.querySelectorAll<HTMLButtonElement>('button[data-tab]');
    tabs[0]?.focus();
    tabs[0]?.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true }));
    expect(document.activeElement).toBe(tabs[1]);
    tabs[1]?.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft', bubbles: true }));
    expect(document.activeElement).toBe(tabs[0]);
    tabs[0]?.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true }));
    expect(document.activeElement).toBe(tabs[1]);
    tabs[1]?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Home', bubbles: true }));
    expect(document.activeElement).toBe(tabs[0]);
    cleanup();
  });
});
