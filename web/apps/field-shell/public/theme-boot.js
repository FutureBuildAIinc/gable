/* SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0 */
/* SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors */

/* Boot theme: applies the person's stored light or dark choice before the
   first paint, so the loader never flashes the wrong ground. A classic,
   same origin script because the page's content security policy
   (script-src 'self') refuses an inline one. The last used value
   (gable_theme) is always the current person's choice, so it is the only key
   read here; the app's own entry applies the per person value again before
   its first render. */
(function () {
  try {
    if (localStorage.getItem('gable_theme') === 'light') {
      document.documentElement.setAttribute('data-theme', 'light');
    }
  } catch (e) {
    /* a hostile storage context: Industrial Dark, the default */
  }
})();
