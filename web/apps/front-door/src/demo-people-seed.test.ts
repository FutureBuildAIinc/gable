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

  // Item 351: with --core, each person signs in, then the owner links the
  // person to the staff row and grants the yards through the core's routes,
  // and a second run makes no new link. A stand-in curl plays the core and
  // keeps the links it was given in a state file.
  describe('with the core named', () => {
    const password = 'unit-test-password-0123456789';
    const run = (dir: string) => {
      const config = join(dir, 'config.json');
      writeFileSync(
        config,
        JSON.stringify({ endpoint: 'http://127.0.0.1:1', projectId: 'p', teamId: 't', apiKey: 'k' }),
      );
      return spawnSync('bash', [SEED_SCRIPT, '--config', config, '--core', 'http://core.test'], {
        env: { PATH: `${join(dir, 'bin')}:${process.env.PATH}`, DEMO_PEOPLE_PASSWORD: password },
        encoding: 'utf8',
      });
    };
    const setup = () => {
      const dir = mkdtempSync(join(tmpdir(), 'seed-demo-people-'));
      const bin = join(dir, 'bin');
      spawnSync('mkdir', [bin]);
      const staff = seeded.map((p) => ({ id: `staff-${p.id}`, staff_no: p.id }));
      const yardCodes = [...new Set(seeded.flatMap((p) => p.yards))];
      const branches = yardCodes.map((c) => ({ id: `branch-${c}`, code: c }));
      writeFileSync(join(dir, 'staff.json'), JSON.stringify({ items: staff, next_cursor: null }));
      writeFileSync(join(dir, 'branches.json'), JSON.stringify({ items: branches, next_cursor: null }));
      writeFileSync(join(dir, 'links.txt'), '');
      writeFileSync(
        join(bin, 'curl'),
        [
          '#!/usr/bin/env bash',
          `D='${dir}'`,
          'method=GET; out=/dev/null; body=""; url=""',
          'while [ $# -gt 0 ]; do case "$1" in -X) method="$2"; shift;; -o) out="$2"; shift;; --data-binary) body="$(cat)"; shift;; http*) url="$1";; esac; shift; done',
          'printf "%s %s %s\\n" "$method" "$url" "$body" >> "$D/calls.log"',
          'case "$url" in',
          '  http://127.0.0.1:1/*) echo "{}" > "$out"; [ "$method" = PATCH ] && printf 200 || printf 201;;',
          '  */api/v1/auth/signin) id="$(printf %s "$body" | jq -r .email | cut -d@ -f1)"; echo "{\\"sub\\":\\"sub-$id\\"}" > "$out"; printf 200;;',
          '  */api/v1/admin/staff\\?*) cp "$D/staff.json" "$out"; printf 200;;',
          '  */api/v1/branches\\?*) cp "$D/branches.json" "$out"; printf 200;;',
          '  */sign-in-people)',
          '    jq -n --rawfile l "$D/links.txt" --slurpfile s "$D/staff.json" \'($l | split("\\n") | map(select(length>0) | split(" ") | {(.[0]): .[1]}) | add // {}) as $m | {items: [$s[0].items[] | {sub: ("sub-" + .staff_no), linked_staff_id: $m["sub-" + .staff_no]}]}\' > "$out"; printf 200;;',
          '  */sign-in-link) sid="$(printf %s "$url" | sed -E \'s#.*/staff/([^/]*)/sign-in-link#\\1#\')"; printf "%s %s\\n" "$(printf %s "$body" | jq -r .sub)" "$sid" >> "$D/links.txt"; printf 204;;',
          '  */branches) echo "{}" > "$out"; printf 200;;',
          '  *) echo "{}" > "$out"; printf 404;;',
          'esac',
          '',
        ].join('\n'),
      );
      chmodSync(join(bin, 'curl'), 0o755);
      return dir;
    };
    const calls = (dir: string) =>
      readFileSync(join(dir, 'calls.log'), 'utf8')
        .split('\n')
        .filter((l) => l.startsWith('PUT ') || l.includes('/branches '));

    it('links each person to the right staff row and grants the right yards', () => {
      const dir = setup();
      try {
        const r = run(dir);
        expect(r.status, r.stderr).toBe(0);
        const log = readFileSync(join(dir, 'calls.log'), 'utf8');
        for (const p of seeded) {
          expect(log, p.id).toContain(
            `PUT http://core.test/api/v1/admin/staff/staff-${p.id}/sign-in-link {"sub":"sub-${p.id}"}`,
          );
          for (const y of p.yards) {
            const home = y === p.homeYard;
            expect(log, `${p.id} ${y}`).toContain(
              `POST http://core.test/api/v1/users/sub-${p.id}/branches {"branch_id":"branch-${y}","is_home":${home}}`,
            );
          }
          const grants = log.split('\n').filter((l) => l.includes(`/users/sub-${p.id}/branches`));
          expect(grants.length, p.id).toBe(p.yards.length);
        }
        expect(r.stdout + r.stderr).not.toContain(password);
      } finally {
        rmSync(dir, { recursive: true, force: true });
      }
    });

    it('makes no new link on a second run', () => {
      const dir = setup();
      try {
        expect(run(dir).status).toBe(0);
        const first = calls(dir).filter((l) => l.startsWith('PUT '));
        expect(first.length).toBe(seeded.length);
        const second = run(dir);
        expect(second.status, second.stderr).toBe(0);
        const puts = calls(dir).filter((l) => l.startsWith('PUT '));
        expect(puts.length).toBe(seeded.length);
        expect(readFileSync(join(dir, 'links.txt'), 'utf8').trim().split('\n').length).toBe(seeded.length);
      } finally {
        rmSync(dir, { recursive: true, force: true });
      }
    });
  });
});
