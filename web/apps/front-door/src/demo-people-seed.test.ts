// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The demo people the local Appwrite is seeded with (scripts/appwrite/
 * demo-people.json, read by seed-demo-people.sh) are the persona cards on the
 * door: same subject, name, role and yards. A card the seed does not know
 * could not sign in for real, so the two lists must not drift apart.
 *
 * @vitest-environment node
 */
import { spawnSync } from 'node:child_process';
import { chmodSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, it, expect } from 'vitest';
import { DEMO_PERSONAS } from './demo-personas.ts';

interface SeededPerson {
  id: string;
  name: string;
  role: string;
  homeYard: string;
  yards: string[];
}

const SEED_SCRIPT = fileURLToPath(new URL('../../../../scripts/appwrite/seed-demo-people.sh', import.meta.url));

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

  // Item 329: the password comes from the environment only, never a default.
  it('take their password from the environment only', () => {
    expect(readFileSync(SEED_SCRIPT, 'utf8')).not.toMatch(/PASSWORD="[^$]/);
    const dir = mkdtempSync(join(tmpdir(), 'seed-demo-people-'));
    try {
      const config = join(dir, 'config.json');
      writeFileSync(config, '{}');
      for (const pw of [undefined, '', 'short']) {
        const env: NodeJS.ProcessEnv = { PATH: process.env.PATH };
        if (pw !== undefined) env.DEMO_PEOPLE_PASSWORD = pw;
        const run = spawnSync('bash', [SEED_SCRIPT, '--config', config], { env, encoding: 'utf8' });
        expect(run.status, String(pw)).toBe(64);
        expect(run.stderr).toContain('DEMO_PEOPLE_PASSWORD');
      }
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });

  // P3-1: the password never sits on argv (/proc/<pid>/cmdline). A stand-in
  // curl records its arguments and its stdin; the password must be only in
  // the latter.
  it('keep the password off every command line', () => {
    const password = 'unit-test-password-0123456789';
    const dir = mkdtempSync(join(tmpdir(), 'seed-demo-people-'));
    try {
      const bin = join(dir, 'bin');
      spawnSync('mkdir', [bin]);
      const argvLog = join(dir, 'argv.log');
      const stdinLog = join(dir, 'stdin.log');
      writeFileSync(
        join(bin, 'curl'),
        [
          '#!/usr/bin/env bash',
          `printf '%s\\n' "$@" >> '${argvLog}'`,
          `cat >> '${stdinLog}'`,
          'method=POST; out=/dev/null',
          'while [ $# -gt 0 ]; do case "$1" in -X) method="$2";; -o) out="$2";; esac; shift; done',
          'echo "{}" > "$out"',
          '[ "$method" = PATCH ] && printf 200 || printf 201',
          '',
        ].join('\n'),
      );
      chmodSync(join(bin, 'curl'), 0o755);
      const config = join(dir, 'config.json');
      writeFileSync(
        config,
        JSON.stringify({ endpoint: 'http://127.0.0.1:1', projectId: 'p', teamId: 't', apiKey: 'k' }),
      );
      const run = spawnSync('bash', [SEED_SCRIPT, '--config', config], {
        env: { PATH: `${bin}:${process.env.PATH}`, DEMO_PEOPLE_PASSWORD: password },
        encoding: 'utf8',
      });
      expect(run.status, run.stderr).toBe(0);
      expect(readFileSync(argvLog, 'utf8')).not.toContain(password);
      expect(readFileSync(stdinLog, 'utf8')).toContain(password);
      expect(run.stdout + run.stderr).not.toContain(password);
    } finally {
      rmSync(dir, { recursive: true, force: true });
    }
  });
});
