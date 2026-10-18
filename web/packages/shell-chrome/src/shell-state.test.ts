// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest';
import {
  RAIL_NARROW_MAX,
  RAIL_PHONE_MAX,
  appsForRoles,
  bannerWords,
  initialsOf,
  memoryStorage,
  railChoiceFromStored,
  railChoiceToStored,
  railModeFor,
  type ShellApp,
} from './shell-state.ts';

const CATALOG: ShellApp[] = [
  { id: 'desk', name: 'Desk', path: '/home', icon: 'grid', roles: [], places: [{ id: 'orders', label: 'Orders', path: '/orders' }] },
  { id: 'pos', name: 'Point of Sale', path: '/pos', icon: 'receipt', roles: ['cashier', 'sales', 'owner'], places: [] },
  { id: 'driver', name: 'Driver', path: '/driver', icon: 'truck', roles: ['driver'], places: [] },
];

describe('the rail posture', () => {
  it('reads silence and garbage as hidden, and the two literals as themselves', () => {
    expect(railChoiceFromStored(null)).toBe('hidden');
    expect(railChoiceFromStored('')).toBe('hidden');
    expect(railChoiceFromStored('WIDE')).toBe('hidden');
    expect(railChoiceFromStored('icons')).toBe('icons');
    expect(railChoiceFromStored('wide')).toBe('wide');
    expect(railChoiceToStored('hidden')).toBe('hidden');
  });

  it('round trips through a storage seam', () => {
    const seam = memoryStorage();
    seam.write('k', railChoiceToStored('wide'));
    expect(railChoiceFromStored(seam.read('k'))).toBe('wide');
  });
});

describe('railModeFor', () => {
  it('is none on phones whatever the posture', () => {
    for (const choice of ['hidden', 'icons', 'wide'] as const) {
      expect(railModeFor({ choice, peek: true, width: RAIL_PHONE_MAX - 1 })).toBe('none');
    }
  });

  it('is none when hidden and not peeking', () => {
    expect(railModeFor({ choice: 'hidden', peek: false, width: 1440 })).toBe('none');
  });

  it('peeks wide on desktop and icons on narrow', () => {
    expect(railModeFor({ choice: 'hidden', peek: true, width: 1440 })).toBe('wide');
    expect(railModeFor({ choice: 'hidden', peek: true, width: RAIL_NARROW_MAX - 1 })).toBe('icons');
  });

  it('pinned icons stays icons; pinned wide falls to icons on narrow', () => {
    expect(railModeFor({ choice: 'icons', peek: false, width: 1440 })).toBe('icons');
    expect(railModeFor({ choice: 'wide', peek: false, width: 1440 })).toBe('wide');
    expect(railModeFor({ choice: 'wide', peek: false, width: 1024 })).toBe('icons');
  });
});

describe('appsForRoles', () => {
  it('keeps the apps a role holds, in catalog order; role free apps serve everyone', () => {
    expect(appsForRoles(CATALOG, ['driver']).map((a) => a.id)).toEqual(['desk', 'driver']);
    expect(appsForRoles(CATALOG, ['cashier']).map((a) => a.id)).toEqual(['desk', 'pos']);
    expect(appsForRoles(CATALOG, []).map((a) => a.id)).toEqual(['desk']);
  });
});

describe('initialsOf', () => {
  it('takes the first letters of the first two words', () => {
    expect(initialsOf('Point of Sale')).toBe('PO');
    expect(initialsOf('Desk')).toBe('D');
    expect(initialsOf('  ')).toBe('');
  });
});

describe('bannerWords', () => {
  it('says nothing when there is nothing to say, and says money never queues when offline', () => {
    expect(bannerWords('')).toBe('');
    expect(bannerWords('offline')).toContain('offline');
    expect(bannerWords('offline')).toContain('money never queues');
  });
});
