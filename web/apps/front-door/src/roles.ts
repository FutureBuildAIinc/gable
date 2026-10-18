// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The front door launcher's role catalog and shell routing table.
 *
 * Mirrors tauri/core/src/profiles.rs:
 * - Gable Work (work-shell, /shell/): owner, admin, finance, sales, cashier, purchasing
 * - Gable Field (field-shell, /field/): warehouse, driver
 * - Gable Pro (pro-shell, /portal): contractor portal accounts (no staff roles)
 *
 * The ?console=1 launcher groups these into five role groups so a person can
 * pick any role (or continue as the remembered identity from identity-tap)
 * and open the target shell without storing or showing a bearer token.
 */
import {
  BookOpen,
  CreditCard,
  FileText,
  Globe,
  LayoutDashboard,
  Package,
  Settings,
  ShoppingBag,
  Truck,
} from 'lucide';
import type { icon } from '@gable/design-system';

export type ShellChannel = 'work' | 'field' | 'pro';
export type ShellStartApp = 'work-shell' | 'field-shell' | 'pro-shell';
export type ShellPath = '/shell/' | '/field/' | '/portal';

export type StaffRoleCode =
  | 'owner'
  | 'admin'
  | 'finance'
  | 'sales'
  | 'cashier'
  | 'purchasing'
  | 'warehouse'
  | 'driver';

export type LauncherRoleCode = StaffRoleCode | 'pro';

/** The eight staff role words the core and tauri/core/src/profiles.rs know. */
export const KNOWN_STAFF_ROLES: readonly StaffRoleCode[] = Object.freeze([
  'owner',
  'admin',
  'finance',
  'sales',
  'cashier',
  'purchasing',
  'warehouse',
  'driver',
]);

/** One shell profile, matching tauri/core/src/profiles.rs. */
export interface ShellProfile {
  readonly identifier: string;
  readonly productName: 'Gable Work' | 'Gable Field' | 'Gable Pro';
  readonly channel: ShellChannel;
  readonly roles: readonly StaffRoleCode[];
  readonly startApp: ShellStartApp;
  readonly path: ShellPath;
}

export const SHELL_PROFILES: Readonly<Record<ShellChannel, ShellProfile>> = Object.freeze({
  work: Object.freeze({
    identifier: 'ai.futurebuild.gable.work',
    productName: 'Gable Work',
    channel: 'work',
    roles: Object.freeze(['owner', 'admin', 'finance', 'sales', 'cashier', 'purchasing'] as const),
    startApp: 'work-shell',
    path: '/shell/',
  }),
  field: Object.freeze({
    identifier: 'ai.futurebuild.gable.field',
    productName: 'Gable Field',
    channel: 'field',
    roles: Object.freeze(['warehouse', 'driver'] as const),
    startApp: 'field-shell',
    path: '/field/',
  }),
  pro: Object.freeze({
    identifier: 'ai.futurebuild.gable.pro',
    productName: 'Gable Pro',
    channel: 'pro',
    roles: Object.freeze([] as const),
    startApp: 'pro-shell',
    path: '/portal',
  }),
});

/**
 * The profile a staff role opens, matching tauri/core/src/profiles.rs::profile_for_role.
 * Returns null for non-staff roles (including 'pro' and unknown strings).
 */
export function profileForStaffRole(role: string): ShellProfile | null {
  for (const profile of [SHELL_PROFILES.work, SHELL_PROFILES.field, SHELL_PROFILES.pro]) {
    if ((profile.roles as readonly string[]).includes(role)) {
      return profile;
    }
  }
  return null;
}

/**
 * The shell profile any launcher role code opens: staff roles route via
 * profileForStaffRole, and the contractor entry ('pro') routes to Gable Pro (/portal).
 */
export function shellForRole(role: string): ShellProfile | null {
  if (role === 'pro') return SHELL_PROFILES.pro;
  return profileForStaffRole(role);
}

/** One role card in the five-group ?console=1 launcher. */
export interface LauncherRole {
  readonly role: LauncherRoleCode;
  readonly subject: string;
  readonly label: string;
  readonly displayName: string;
  readonly homeYardCode: string;
  readonly homeYardName: string;
  readonly line: string;
  readonly icon: Parameters<typeof icon>[0];
  readonly shell: ShellProfile;
  readonly surfaces: readonly string[];
}

/** One of the five groups on the ?console=1 role picker. */
export interface RoleGroup {
  readonly id:
    | 'counter-sales'
    | 'yard-delivery'
    | 'purchasing-finance'
    | 'leadership-admin'
    | 'contractor-portal';
  readonly title: string;
  readonly summary: string;
  readonly roles: readonly LauncherRole[];
}

export const ROLE_GROUPS: readonly RoleGroup[] = Object.freeze([
  Object.freeze({
    id: 'counter-sales',
    title: 'Counter and Sales',
    summary: 'Quotes, customer orders and counter checkout at the branch.',
    roles: Object.freeze([
      Object.freeze({
        role: 'sales',
        subject: 'demo-sales-kel',
        label: 'Sales',
        displayName: 'Avery Brooks',
        homeYardCode: 'KEL-MAIN',
        homeYardName: 'Kelowna Main Yard',
        line: 'Build quotes, price material lists and convert customer orders.',
        icon: FileText,
        shell: SHELL_PROFILES.work,
        surfaces: Object.freeze(['Quotes', 'Orders', 'Customers']),
      }),
      Object.freeze({
        role: 'cashier',
        subject: 'demo-cashier-kel',
        label: 'Cashier',
        displayName: 'Jordan Pike',
        homeYardCode: 'KEL-MAIN',
        homeYardName: 'Kelowna Main Yard',
        line: 'Open the till, ring up counter sales and take account or card payments.',
        icon: CreditCard,
        shell: SHELL_PROFILES.work,
        surfaces: Object.freeze(['Counter', 'Orders', 'Customers']),
      }),
    ]),
  }),
  Object.freeze({
    id: 'yard-delivery',
    title: 'Yard and Delivery',
    summary: 'Staging, yard picks and delivery runs in the phone-first field frame.',
    roles: Object.freeze([
      Object.freeze({
        role: 'warehouse',
        subject: 'demo-warehouse-kel',
        label: 'Warehouse',
        displayName: 'Sam Ortiz',
        homeYardCode: 'KEL-MAIN',
        homeYardName: 'Kelowna Main Yard',
        line: 'Pick and stage dispatched orders, count bins and receive incoming stock.',
        icon: Package,
        shell: SHELL_PROFILES.field,
        surfaces: Object.freeze(['Yard Pick', 'Inventory', 'Locations']),
      }),
      Object.freeze({
        role: 'driver',
        subject: 'demo-driver-kel',
        label: 'Driver',
        displayName: 'Alex Moreno',
        homeYardCode: 'KEL-MAIN',
        homeYardName: 'Kelowna Main Yard',
        line: 'Work the delivery run from load check to drop-off and proof of delivery.',
        icon: Truck,
        shell: SHELL_PROFILES.field,
        surfaces: Object.freeze(['Driver Run', 'Deliveries', 'Locations']),
      }),
    ]),
  }),
  Object.freeze({
    id: 'purchasing-finance',
    title: 'Purchasing and Finance',
    summary: 'Vendor replenishment, receiving, customer statements and the general ledger.',
    roles: Object.freeze([
      Object.freeze({
        role: 'purchasing',
        subject: 'demo-purchasing',
        label: 'Purchasing',
        displayName: 'Casey Lin',
        homeYardCode: 'KEL-MAIN',
        homeYardName: 'Kelowna Main Yard',
        line: 'Buy branch stock, manage vendor catalogues and track incoming purchase orders.',
        icon: ShoppingBag,
        shell: SHELL_PROFILES.work,
        surfaces: Object.freeze(['Purchasing', 'Vendors', 'Inventory']),
      }),
      Object.freeze({
        role: 'finance',
        subject: 'demo-finance',
        label: 'Finance',
        displayName: 'Robin Diaz',
        homeYardCode: 'KEL-MAIN',
        homeYardName: 'Kelowna Main Yard',
        line: 'Issue statements, reconcile receivables and keep the general ledger.',
        icon: BookOpen,
        shell: SHELL_PROFILES.work,
        surfaces: Object.freeze(['Invoices', 'General Ledger', 'Reporting']),
      }),
    ]),
  }),
  Object.freeze({
    id: 'leadership-admin',
    title: 'Leadership and Admin',
    summary: 'Multi-yard oversight, margin approvals, roles and dealer configuration.',
    roles: Object.freeze([
      Object.freeze({
        role: 'owner',
        subject: 'demo-owner',
        label: 'Owner',
        displayName: 'Morgan Hale',
        homeYardCode: 'KEL-MAIN',
        homeYardName: 'Kelowna Main Yard',
        line: 'Review performance across every yard, approve margins and inspect stock.',
        icon: LayoutDashboard,
        shell: SHELL_PROFILES.work,
        surfaces: Object.freeze(['Dashboard', 'Reporting', 'Governance']),
      }),
      Object.freeze({
        role: 'admin',
        subject: 'demo-admin',
        label: 'Admin',
        displayName: 'Riley Vance',
        homeYardCode: 'KEL-MAIN',
        homeYardName: 'Kelowna Main Yard',
        line: 'Configure branches, staff permissions, integrations and dealer settings.',
        icon: Settings,
        shell: SHELL_PROFILES.work,
        surfaces: Object.freeze(['Tech Admin', 'Locations', 'Governance']),
      }),
    ]),
  }),
  Object.freeze({
    id: 'contractor-portal',
    title: 'Contractor Portal',
    summary: 'Trade account self-service for builders, estimators and jobsite crews.',
    roles: Object.freeze([
      Object.freeze({
        role: 'pro',
        subject: 'demo-pro-tarnwick',
        label: 'Contractor Account',
        displayName: 'Tarnwick Construction',
        homeYardCode: 'KEL-MAIN',
        homeYardName: 'Kelowna Main Yard',
        line: 'Review job quotes, place trade orders, track deliveries and pay invoices.',
        icon: Globe,
        shell: SHELL_PROFILES.pro,
        surfaces: Object.freeze(['Quotes', 'Orders', 'Deliveries', 'Invoices']),
      }),
    ]),
  }),
]);

const ALL_LAUNCHER_ROLES: readonly LauncherRole[] = Object.freeze(
  ROLE_GROUPS.flatMap((group) => group.roles),
);

/** Look up a launcher role entry by its role code. */
export function roleEntryFor(role: string): LauncherRole | null {
  return ALL_LAUNCHER_ROLES.find((entry) => entry.role === role) ?? null;
}

/** True when the URL query string activates the ?console=1 role launcher. */
export function isConsoleLauncher(search: string): boolean {
  try {
    return new URLSearchParams(search).get('console') === '1';
  } catch {
    return false;
  }
}
