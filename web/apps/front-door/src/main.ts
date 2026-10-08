// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import './index.css';
import '@gable/design-system';
import './front-door.ts';

// Mount the door into #root, replacing the loading spinner from index.html.
const root = document.getElementById('root');
if (root) {
  root.innerHTML = '<gable-front-door></gable-front-door>';
}
