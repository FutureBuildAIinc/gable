// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest';
import {
  KNOWN_STAFF_ROLES,
  ROLE_GROUPS,
  SHELL_PROFILES,
  isConsoleLauncher,
  profileForStaffRole,
  roleEntryFor,
  shellForRole,
} from './roles.ts';

describe('front door shell routing and five-group role catalog', () => {
  it('routes office and counter roles to work-shell at /shell/, matching tauri/core/src/profiles.rs', () => {
    for (const role of ['owner', 'admin', 'finance', 'sales', 'cashier', 'purchasing'] as const) {
      const profile = profileForStaffRole(role);
      expect(profile).toBe(SHELL_PROFILES.work);
      expect(profile?.identifier).toBe('ai.futurebuild.gable.work');
      expect(profile?.productName).toBe('Gable Work');
      expect(profile?.startApp).toBe('work-shell');
      expect(profile?.path).toBe('/shell/');
      expect(shellForRole(role)).toBe(SHELL_PROFILES.work);
    }
  });

  it('routes warehouse and driver roles to field-shell at /field/, matching tauri/core/src/profiles.rs', () => {
    for (const role of ['warehouse', 'driver'] as const) {
      const profile = profileForStaffRole(role);
      expect(profile).toBe(SHELL_PROFILES.field);
      expect(profile?.identifier).toBe('ai.futurebuild.gable.field');
      expect(profile?.productName).toBe('Gable Field');
      expect(profile?.startApp).toBe('field-shell');
      expect(profile?.path).toBe('/field/');
      expect(shellForRole(role)).toBe(SHELL_PROFILES.field);
    }
  });

  it('gives pro-shell no staff roles in profileForStaffRole and routes contractor pro entry to /portal', () => {
    expect(SHELL_PROFILES.pro.roles).toEqual([]);
    expect(SHELL_PROFILES.pro.identifier).toBe('ai.futurebuild.gable.pro');
    expect(SHELL_PROFILES.pro.productName).toBe('Gable Pro');
    expect(SHELL_PROFILES.pro.startApp).toBe('pro-shell');
    expect(SHELL_PROFILES.pro.path).toBe('/portal');

    for (const staffRole of KNOWN_STAFF_ROLES) {
      expect(profileForStaffRole(staffRole)?.identifier).not.toBe(SHELL_PROFILES.pro.identifier);
    }
    expect(profileForStaffRole('pro')).toBeNull();
    expect(shellForRole('pro')).toBe(SHELL_PROFILES.pro);
    expect(profileForStaffRole('stranger')).toBeNull();
    expect(shellForRole('stranger')).toBeNull();
    expect(shellForRole('')).toBeNull();
  });

  it('organises all roles into five groups with every staff role and the contractor portal represented once', () => {
    expect(ROLE_GROUPS).toHaveLength(5);
    expect(ROLE_GROUPS.map((g) => g.title)).toEqual([
      'Counter and Sales',
      'Yard and Delivery',
      'Purchasing and Finance',
      'Leadership and Admin',
      'Contractor Portal',
    ]);

    const roles = ROLE_GROUPS.flatMap((g) => g.roles);
    const codes = roles.map((r) => r.role);
    expect(new Set(codes).size).toBe(codes.length);
    for (const staffRole of KNOWN_STAFF_ROLES) {
      expect(codes).toContain(staffRole);
    }
    expect(codes).toContain('pro');

    for (const entry of roles) {
      expect(entry.shell).toBe(shellForRole(entry.role));
      expect(roleEntryFor(entry.role)).toBe(entry);
      expect(entry.surfaces.length).toBeGreaterThan(0);
    }
  });

  it('contains zero em or en dashes in group titles, summaries, role labels, lines, or surface chips', () => {
    const text = ROLE_GROUPS.flatMap((g) => [
      g.title,
      g.summary,
      ...g.roles.flatMap((r) => [r.label, r.displayName, r.homeYardName, r.line, ...r.surfaces]),
    ]).join(' ');
    expect(text).not.toMatch(/[\u2013\u2014]/);
  });

  it('detects ?console=1 in the query string', () => {
    expect(isConsoleLauncher('?console=1')).toBe(true);
    expect(isConsoleLauncher('?theme=light&console=1')).toBe(true);
    expect(isConsoleLauncher('')).toBe(false);
    expect(isConsoleLauncher('?console=0')).toBe(false);
    expect(isConsoleLauncher('?console=true')).toBe(false);
  });
});
