// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The demo people the local Appwrite is seeded with (scripts/appwrite/
 * demo-people.json, read by seed-demo-people.sh) are the persona cards on the
 * door: same subject, name, role and yards. A card the seed does not know
 * could not sign in for real, so the two lists must not drift apart.
 */
import { readFileSync } from 'node:fs';
import { describe, it, expect } from 'vitest';
import { DEMO_PERSONAS } from './demo-personas.ts';

interface SeededPerson {
  id: string;
  name: string;
  role: string;
  homeYard: string;
  yards: string[];
}

const seeded: SeededPerson[] = JSON.parse(
  readFileSync(new URL('../../../../scripts/appwrite/demo-people.json', import.meta.url), 'utf8'),
);

describe('the seeded demo people', () => {
  it('are the door persona cards, one for one', () => {
    expect(seeded.map((p) => p.id)).toEqual(DEMO_PERSONAS.map((p) => p.subject));
  });

  it('carry each persona name, role and yards', () => {
    for (const persona of DEMO_PERSONAS) {
      const person = seeded.find((p) => p.id === persona.subject);
      expect(person, persona.subject).toBeDefined();
      expect(person?.name).toBe(persona.name);
      expect(person?.role).toBe(persona.role);
      expect(person?.homeYard).toBe(persona.homeYardCode);
      expect(person?.yards).toEqual([...persona.grantedYardCodes]);
    }
  });

  it('use ids Appwrite accepts', () => {
    for (const p of seeded) expect(p.id).toMatch(/^[a-z0-9][a-z0-9._-]{0,35}$/);
  });
});
