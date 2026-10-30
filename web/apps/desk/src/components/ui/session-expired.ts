// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Rendered over the ERP surfaces when a request comes back 401.
 *
 * The desk has no sign-in page of its own: the front door at / is the one
 * place staff sign in (ADR 0012), and the session is the cookie the core set
 * there. fetchClient fires `gable:session-expired` on a 401 after custody has
 * dropped its display data, and this panel says what happened and offers the
 * recovery: go back to the door and sign in again.
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
            You were signed out, or the session ran out. Sign in again to
            continue. Any unsaved work on this page will be lost.
          </p>
          <button
            @click=${() => window.location.assign('/')}
            class="mt-6 inline-flex items-center gap-2 rounded-lg bg-gable-green/10 px-4 py-2 text-sm font-medium text-gable-green ring-1 ring-inset ring-gable-green/20 transition-colors hover:bg-gable-green/20"
          >
            Sign in again
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
