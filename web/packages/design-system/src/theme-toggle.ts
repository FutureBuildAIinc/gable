// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The sun or moon control: one button that switches Industrial Dark and Gable
// light. It names itself for the mode it will switch to ("Light mode" while
// dark, "Dark mode" while light) and is a native button, so the keyboard
// reaches it. Until the shell puts it in the person's menu it sits in the
// desk's header and on the front door.

import { LitElement, html } from 'lit';
import { customElement, state } from 'lit/decorators.js';
import { Sun, Moon } from 'lucide';
import { icon } from './icons.ts';
import { currentTheme, toggleTheme, onThemeChange, type Theme } from './theme.ts';

@customElement('gable-theme-toggle')
export class GableThemeToggle extends LitElement {
  createRenderRoot() { return this; }

  @state() private _theme: Theme = currentTheme();
  private _off: (() => void) | null = null;

  connectedCallback() {
    super.connectedCallback();
    this._theme = currentTheme();
    this._off = onThemeChange((t) => { this._theme = t; });
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this._off?.();
    this._off = null;
  }

  render() {
    const toLight = this._theme === 'dark';
    const label = toLight ? 'Light mode' : 'Dark mode';
    return html`
      <button
        type="button"
        data-testid="theme-toggle"
        @click=${() => { toggleTheme(); }}
        aria-label="${label}"
        title="${label}"
        class="h-9 w-9 shrink-0 rounded-lg flex items-center justify-center text-zinc-400 hover:text-white hover:bg-white/5 border border-white/5 transition-colors"
      >
        ${toLight ? icon(Sun, 16) : icon(Moon, 16)}
      </button>
    `;
  }
}
