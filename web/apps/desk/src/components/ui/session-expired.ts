// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Rendered over the ERP surfaces when a request comes back 401.
 *
 * The ERP has no sign-in page of its own — authentication is an external
 * identity provider (the backend validates against JWKS_URL) and the bearer
 * token is placed in localStorage out of band. fetchClient used to hard-navigate
 * to `/login`, which is not in routes.ts, so an expired session showed the 404
 * page. This panel says what actually happened and offers the one recovery the
 * app can honestly perform: reload once a fresh token is in place.
 */
import { LitElement, html } from 'lit';
import { customElement } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { LogIn } from 'lucide';

@customElement('gable-session-expired')
export class GableSessionExpired extends LitElement {
  createRenderRoot() {
    return this;
  }

  render() {
    return html`
      <div
        role="alertdialog"
        aria-labelledby="session-expired-title"
        class="fixed inset-0 z-[100] flex items-center justify-center bg-deep-space/90 backdrop-blur-sm p-6"
      >
        <div class="max-w-md w-full rounded-2xl border border-white/10 bg-slate-steel p-8 text-center">
          <div class="mx-auto mb-4 flex h-14 w-14 items-center justify-center rounded-2xl border border-white/10 bg-white/5 text-zinc-400">
            ${icon(LogIn, 28)}
          </div>
          <h1 id="session-expired-title" class="mb-2 text-xl font-semibold text-white">
            Your session has expired
          </h1>
          <p class="text-sm text-zinc-400">
            The server rejected this request as unauthenticated. Sign in again
            with your identity provider, then reload to continue. Any unsaved
            work on this page will be lost.
          </p>
          <button
            @click=${() => window.location.reload()}
            class="mt-6 inline-flex items-center gap-2 rounded-lg bg-gable-green/10 px-4 py-2 text-sm font-medium text-gable-green shadow-[inset_0_0_0_1px_rgba(0,255,163,0.2)] transition-colors hover:bg-gable-green/20"
          >
            Reload
          </button>
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-session-expired': GableSessionExpired;
  }
}
