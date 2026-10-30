// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Item 349: the local proof console admin's password and the server key id
 * (scripts/appwrite/setup.sh) come from the environment, or are generated per
 * run when unset. The script holds no literal for either, and never prints
 * them; a generated value reaches only the --out file the script writes.
 *
 * @vitest-environment node
 */
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, it, expect } from 'vitest';

const SETUP_SCRIPT = fileURLToPath(new URL('../../../../scripts/appwrite/setup.sh', import.meta.url));
const script = readFileSync(SETUP_SCRIPT, 'utf8');

/** The line that assigns a shell variable, or undefined. */
function assignment(name: string): string | undefined {
  return script.split('\n').find((l) => new RegExp(`^${name}=`).test(l));
}

describe('setup.sh credentials', () => {
  it('hold no literal password or key id', () => {
    for (const name of ['PASSWORD', 'KEY_ID']) {
      const line = assignment(name);
      expect(line, name).toBeDefined();
      expect(line, name).not.toMatch(new RegExp(`^${name}="[^$]`));
      expect(line, name).not.toMatch(new RegExp(`^${name}='`));
    }
  });

  it('read both from the environment', () => {
    expect(assignment('PASSWORD')).toContain('APPWRITE_PROOF_CONSOLE_PASSWORD');
    expect(assignment('KEY_ID')).toContain('APPWRITE_PROOF_KEY_ID');
  });

  it('generate a value per run when unset', () => {
    expect(assignment('PASSWORD')).toMatch(/openssl rand -hex/);
    expect(assignment('KEY_ID')).toMatch(/openssl rand -hex/);
  });

  it('keep the generated key id a valid Appwrite id', () => {
    // At most 36 of a-z A-Z 0-9 _ - ., not starting with a special character.
    const m = /^KEY_ID=.*?\$\{APPWRITE_PROOF_KEY_ID:-([^$}]*)\$\(openssl rand -hex (\d+)\)\}/.exec(assignment('KEY_ID') ?? '');
    expect(m, 'KEY_ID default has a prefix and a hex tail').not.toBeNull();
    const id = `${m![1]}${'a'.repeat(Number(m![2]) * 2)}`;
    expect(id).toMatch(/^[a-zA-Z0-9][a-zA-Z0-9._-]{0,35}$/);
  });

  it('never print either value', () => {
    const printing = script.split('\n').filter((l) => /\b(echo|printf)\b/.test(l) && /\$\{?(PASSWORD|KEY_ID)\b/.test(l));
    expect(printing).toEqual([]);
  });

  it('write the generated values to the --out file', () => {
    expect(script).toMatch(/--arg password "\$PASSWORD"/);
    expect(script).toMatch(/--arg keyId "\$KEY_ID"/);
  });
});
