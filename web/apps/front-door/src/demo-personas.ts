// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The nine demo personas on the front door's dev sign in.
 *
 * Each card is one of the invented staff from DEMO-PERSONAS.md: the subject
 * the M1 seed uses for the staff row, the name and role the door renders,
 * the home yard code the desk's BranchContext opens on, and the yards the
 * persona was granted (the seed's `user_locations`). The local stack runs
 * `AUTH_MODE=dev`, so the core enforces no role: a persona shapes what the
 * screens offer (the door filters tiles with the persona's role, the desk
 * opens on the persona's yard), not what the server allows.
 *
 * Frozen, so a card cannot drift from the seed; the only mutator is the
 * lookup helpers, which return the same reference for the same subject.
 */

/** A staff role the seed may carry. */
export type PersonaRole =
  | 'owner'
  | 'finance'
  | 'sales'
  | 'cashier'
  | 'warehouse'
  | 'purchasing'
  | 'driver';

/** One persona card on the dev sign in. */
export interface DemoPersona {
  /** The subject the M1 seed's `staff.subject` carries; the unique key. */
  readonly subject: string;
  /** The display name the door renders on the card and the header. */
  readonly name: string;
  /** The role the door and the desk use to filter tiles and to name the user. */
  readonly role: PersonaRole;
  /** The home yard's code (the seed's `yards.code`); the desk opens here. */
  readonly homeYardCode: string;
  /** The codes the persona was granted in `user_locations`. */
  readonly grantedYardCodes: readonly string[];
  /** The yard name shown on the card; same source the desk renders. */
  readonly homeYardName: string;
}

/** The yard name the persona's home code maps to. Same shape for every card. */
const YARD_NAMES: Readonly<Record<string, string>> = {
  'KEL-MAIN': 'Kelowna Main Yard',
  WK: 'West Kelowna Satellite',
  LK: 'Lake Country Outpost',
};

/** A card per the table in DEMO-PERSONAS.md. */
export const DEMO_PERSONAS: readonly DemoPersona[] = Object.freeze([
  {
    subject: 'demo-owner',
    name: 'Morgan Hale',
    role: 'owner',
    homeYardCode: 'KEL-MAIN',
    grantedYardCodes: ['KEL-MAIN', 'WK', 'LK'],
    homeYardName: YARD_NAMES['KEL-MAIN'],
  },
  {
    subject: 'demo-finance',
    name: 'Robin Diaz',
    role: 'finance',
    homeYardCode: 'KEL-MAIN',
    grantedYardCodes: ['KEL-MAIN', 'WK', 'LK'],
    homeYardName: YARD_NAMES['KEL-MAIN'],
  },
  {
    subject: 'demo-sales-kel',
    name: 'Avery Brooks',
    role: 'sales',
    homeYardCode: 'KEL-MAIN',
    grantedYardCodes: ['KEL-MAIN'],
    homeYardName: YARD_NAMES['KEL-MAIN'],
  },
  {
    subject: 'demo-cashier-kel',
    name: 'Jordan Pike',
    role: 'cashier',
    homeYardCode: 'KEL-MAIN',
    grantedYardCodes: ['KEL-MAIN'],
    homeYardName: YARD_NAMES['KEL-MAIN'],
  },
  {
    subject: 'demo-warehouse-kel',
    name: 'Sam Ortiz',
    role: 'warehouse',
    homeYardCode: 'KEL-MAIN',
    grantedYardCodes: ['KEL-MAIN'],
    homeYardName: YARD_NAMES['KEL-MAIN'],
  },
  {
    subject: 'demo-purchasing',
    name: 'Casey Lin',
    role: 'purchasing',
    homeYardCode: 'KEL-MAIN',
    grantedYardCodes: ['KEL-MAIN', 'WK', 'LK'],
    homeYardName: YARD_NAMES['KEL-MAIN'],
  },
  {
    subject: 'demo-sales-wk',
    name: 'Taylor Reed',
    role: 'sales',
    homeYardCode: 'WK',
    grantedYardCodes: ['WK'],
    homeYardName: YARD_NAMES['WK'],
  },
  {
    subject: 'demo-cashier-lk',
    name: 'Drew Patel',
    role: 'cashier',
    homeYardCode: 'LK',
    grantedYardCodes: ['LK'],
    homeYardName: YARD_NAMES['LK'],
  },
  {
    subject: 'demo-driver-kel',
    name: 'Alex Moreno',
    role: 'driver',
    homeYardCode: 'KEL-MAIN',
    grantedYardCodes: ['KEL-MAIN'],
    homeYardName: YARD_NAMES['KEL-MAIN'],
  },
] as DemoPersona[]);

/** Look a card up by its subject (the seed's staff.subject). */
export function findPersona(subject: string): DemoPersona | null {
  return DEMO_PERSONAS.find((p) => p.subject === subject) ?? null;
}

/** The role in human words, the same words the seed's role column uses. */
export const ROLE_WORDS: Readonly<Record<PersonaRole, string>> = {
  owner: 'Owner',
  finance: 'Finance',
  sales: 'Sales',
  cashier: 'Cashier',
  warehouse: 'Warehouse',
  purchasing: 'Purchasing',
  driver: 'Driver',
};