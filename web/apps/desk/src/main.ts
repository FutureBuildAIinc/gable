// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import './index.css';
import { router } from './lib/router.ts';
import { routes } from './routes.ts';
import { branchContext } from './services/BranchContext.ts';
import { initDates } from './lib/dates.ts';
import './app.ts';
import { initTheme } from '@gable/design-system';
import { authCustody } from '@gable/auth';

// A person's light or dark choice applies before the first render.
initTheme(authCustody.subject);

// Mount the app into #root (replacing the loading spinner from index.html)
const root = document.getElementById('root');
if (root) {
  root.innerHTML = '<gable-app></gable-app>';
}

// Load the user's branch grants and pick a default, and read the dealer's
// locale for the date formats, BEFORE the router dispatches its first route.
// This guarantees X-Branch-Id is set on the page's initial data fetches and
// the first render already shows dates in the dealer's locale. Errors are
// swallowed so the app still boots when the user is unauthenticated (login
// screen) or the API is unreachable — pages will surface their own errors,
// and the dates fall back to en-US.
Promise.allSettled([branchContext.init(), initDates()]).then(() => {
  router.init(routes);
});
