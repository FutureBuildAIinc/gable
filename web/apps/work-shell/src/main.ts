// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The work shell's entry. A person's light or dark choice applies before the
// first render; then the host mounts. A host that finds itself inside a frame
// without the framed flag is an app that ended its own walk and navigated to
// the origin root: it mounts nothing and asks its parent to switch role (the
// orphaned frame guard in the design system's framed protocol).

import './index.css';
import { initTheme } from '@gable/design-system';
import { authCustody } from '@gable/auth';
import './host-element.ts';

initTheme(authCustody.subject);

const root = document.getElementById('root');
if (root) {
  root.innerHTML = '<gable-work-shell></gable-work-shell>';
}
