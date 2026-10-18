// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, expect, it } from 'vitest';
import './shell-banner.ts';
import type { GableShellBanner } from './shell-banner.ts';

function mount(mode: string): GableShellBanner {
  const el = document.createElement('gable-shell-banner') as GableShellBanner;
  el.mode = mode;
  document.body.appendChild(el);
  return el;
}

describe('gable-shell-banner', () => {
  it('renders nothing when there is nothing to say', async () => {
    const el = mount('');
    await el.updateComplete;
    expect(el.querySelector('[role="status"]')).toBeNull();
    el.remove();
  });

  it('says the truth when offline', async () => {
    const el = mount('offline');
    await el.updateComplete;
    const strip = el.querySelector('[role="status"]');
    expect(strip?.getAttribute('data-mode')).toBe('offline');
    expect(strip?.textContent).toContain('offline');
    expect(strip?.textContent).toContain('money never queues');
    el.remove();
  });

  it('refuses an unknown mode loudly', () => {
    const el = document.createElement('gable-shell-banner') as GableShellBanner;
    el.mode = 'on-fire';
    expect(() => el.render()).toThrow(/unknown mode/);
  });
});
