// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest';
import { initialState, openApp, signedIn, switchRole, type HostSession } from './host-state.ts';
import { FIELD_APPS } from './config.ts';

const WAREHOUSE_SESSION: HostSession = { name: 'Morgan Lee', roleLabel: 'Warehouse', roles: ['warehouse'] };
const DRIVER_SESSION: HostSession = { name: 'Riley Vance', roleLabel: 'Driver', roles: ['driver'] };
const OWNER_SESSION: HostSession = { name: 'Avery Brooks', roleLabel: 'Owner', roles: ['owner'] };

describe('the field host state (tabs by role)', () => {
  it('starts at the door', () => {
    expect(initialState().phase).toBe('door');
  });

  it('warehouse role holds only the Yard tab and lands on /yard', () => {
    const state = signedIn(WAREHOUSE_SESSION, FIELD_APPS);
    expect(state.phase).toBe('field');
    if (state.phase !== 'field') return;
    expect(state.apps.map((a) => a.id)).toEqual(['yard']);
    expect(state.currentAppId).toBe('yard');
    expect(state.currentPath).toBe('/yard');
  });

  it('driver role holds only the Driver tab and lands on /driver', () => {
    const state = signedIn(DRIVER_SESSION, FIELD_APPS);
    expect(state.phase).toBe('field');
    if (state.phase !== 'field') return;
    expect(state.apps.map((a) => a.id)).toEqual(['driver']);
    expect(state.currentAppId).toBe('driver');
    expect(state.currentPath).toBe('/driver');
  });

  it('owner and dual warehouse+driver roles hold both Yard and Driver tabs in catalog order', () => {
    const owner = signedIn(OWNER_SESSION, FIELD_APPS);
    expect(owner.phase).toBe('field');
    if (owner.phase !== 'field') return;
    expect(owner.apps.map((a) => a.id)).toEqual(['yard', 'driver']);
    expect(owner.currentAppId).toBe('yard');

    const dual = signedIn(
      { name: 'Jordan Cross', roleLabel: 'Warehouse · Driver', roles: ['warehouse', 'driver'] },
      FIELD_APPS,
    );
    if (dual.phase !== 'field') return;
    expect(dual.apps.map((a) => a.id)).toEqual(['yard', 'driver']);
  });

  it('a non-field role holds no tabs and lands nowhere', () => {
    const state = signedIn({ name: 'Sam Taylor', roleLabel: 'Sales', roles: ['sales'] }, FIELD_APPS);
    expect(state.phase).toBe('field');
    if (state.phase !== 'field') return;
    expect(state.apps).toEqual([]);
    expect(state.currentAppId).toBeNull();
  });

  it('openApp opens a held field app at its own path or a place under it, and refuses unheld apps', () => {
    const state = signedIn(OWNER_SESSION, FIELD_APPS);
    const opened = openApp(state, 'driver');
    expect(opened.phase).toBe('field');
    if (opened.phase !== 'field') return;
    expect(opened.currentAppId).toBe('driver');
    expect(opened.currentPath).toBe('/driver');

    const place = openApp(state, 'yard', '/yard/receiving');
    if (place.phase !== 'field') return;
    expect(place.currentAppId).toBe('yard');
    expect(place.currentPath).toBe('/yard/receiving');

    const warehouseOnly = signedIn(WAREHOUSE_SESSION, FIELD_APPS);
    expect(openApp(warehouseOnly, 'driver')).toBe(warehouseOnly);
    expect(openApp(warehouseOnly, 'yard', '/yard')).toBe(warehouseOnly);
    const door = initialState();
    expect(openApp(door, 'yard')).toBe(door);
  });

  it('switch role drops the session and returns to the door, from any app', () => {
    const state = signedIn(OWNER_SESSION, FIELD_APPS);
    const opened = openApp(state, 'driver');
    expect(switchRole(opened)).toEqual({ phase: 'door' });
  });
});
