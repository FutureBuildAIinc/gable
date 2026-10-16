// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import './index.css';
import { initTheme } from '@gable/design-system';
import { authCustody } from '@gable/auth';
import './front-door.ts';

// The last used choice (or the held session's person's) applies before the first render.
initTheme(authCustody.subject);

// Mount the door into #root, replacing the loading spinner from index.html.
const root = document.getElementById('root');
if (root) {
  root.innerHTML = '<gable-front-door></gable-front-door>';
}
