// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The work shell's entry. A person's light or dark choice applies before the
// first render; then the host mounts. A page that finds itself inside a frame
// without the framed flag is an orphaned frame: it mounts nothing and asks
// its parent to switch role (the orphaned frame guard in the design system's
// framed protocol).

import './index.css';
import { initTheme, orphanedFrame } from '@gable/design-system';
import { authCustody } from '@gable/auth';
import './host-element.ts';

initTheme(authCustody.subject);

// Mount the host into #root. A shell that finds itself inside a frame without
// the framed flag is an orphaned frame (an app navigated to the shell's
// address inside a frame): it asks the host to switch role and mounts nothing.
const root = document.getElementById('root');
if (root) {
  root.innerHTML = orphanedFrame(window)
    ? '<div style="display:flex;min-height:100vh;align-items:center;justify-content:center;color:rgb(var(--gb-zinc-400, 161 161 170));font-family:Outfit,sans-serif">Switching role...</div>'
    : '<gable-work-shell></gable-work-shell>';
}
