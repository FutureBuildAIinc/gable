// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import './index.css';
import { initTheme, orphanedFrame } from '@gable/design-system';
import { authCustody } from '@gable/auth';
import './front-door.ts';

// The last used choice (or the held session's person's) applies before the first render.
initTheme(authCustody.subject);

// Mount the door into #root, replacing the loading spinner from index.html.
// Production nginx serves the DOOR at the origin root, so a framed app's own
// sign out (it assigns '/') lands the door inside the shell's frame: an
// orphaned frame. It asks the host to switch role and mounts nothing.
const root = document.getElementById('root');
if (root) {
  root.innerHTML = orphanedFrame(window)
    ? '<div style="display:flex;min-height:100vh;align-items:center;justify-content:center;color:rgb(var(--gb-zinc-400, 161 161 170));font-family:Outfit,sans-serif">Switching role...</div>'
    : '<gable-front-door></gable-front-door>';
}
