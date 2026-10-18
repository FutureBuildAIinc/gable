// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest';
import { initialState, openApp, signedIn, switchRole, type HostSession } from './host-state.ts';
import { SHELL_APPS } from './config.ts';

const SESSION: HostSession = { name: 'Avery Brooks', roleLabel: 'Sales', roles: ['sales'] };

describe('the host state', () => {
  it('starts at the door', () => {
    expect(initialState().phase).toBe('door');
  });

  it('sign in lands the role on the first app it holds', () => {
    const state = signedIn(SESSION, SHELL_APPS);
    expect(state.phase).toBe('desk');
    if (state.phase !== 'desk') return;
    expect(state.apps.map((a) => a.id)).toEqual(['desk', 'pos']);
    expect(state.currentAppId).toBe('desk');
    expect(state.currentPath).toBe('/home');
  });

  it('a role that holds nothing lands nowhere and the host says so', () => {
    const state = signedIn({ name: 'N', roleLabel: 'Nobody', roles: ['ghost'] }, []);
    expect(state.phase).toBe('desk');
    if (state.phase !== 'desk') return;
    expect(state.currentAppId).toBeNull();
  });

  it('openApp opens a held app at its own path or a place under it, and refuses the rest', () => {
    const state = signedIn(SESSION, SHELL_APPS);
    const opened = openApp(state, 'pos');
    expect(opened.phase).toBe('desk');
    if (opened.phase !== 'desk') return;
    expect(opened.currentAppId).toBe('pos');
    expect(opened.currentPath).toBe('/pos');

    const place = openApp(state, 'desk', '/orders');
    if (place.phase !== 'desk') return;
    expect(place.currentAppId).toBe('desk');
    expect(place.currentPath).toBe('/orders');

    expect(openApp(state, 'driver')).toBe(state);
    expect(openApp(state, 'desk', '/home')).toBe(state);
    const door = initialState();
    expect(openApp(door, 'desk')).toBe(door);
  });

  it('switch role drops the session and returns to the door, from any app', () => {
    const state = signedIn(SESSION, SHELL_APPS);
    const opened = openApp(state, 'pos');
    expect(switchRole(opened)).toEqual({ phase: 'door' });
  });
});
