// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect, afterEach } from 'vitest';
import './SignaturePad';

describe('the signature pad box', () => {
  afterEach(() => { document.body.innerHTML = ''; });

  it('takes the theme field surface on screen, not a fixed white', async () => {
    const el = document.createElement('gable-signature-pad') as HTMLElement & { updateComplete: Promise<boolean> };
    document.body.append(el);
    await el.updateComplete;
    const style = el.querySelector('canvas')?.getAttribute('style') ?? '';
    expect(style).toContain('background:rgb(var(--gb-till-field))');
    expect(style).not.toContain('#ffffff');
  });
});
