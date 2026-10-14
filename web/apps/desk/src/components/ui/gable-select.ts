// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The app's own select, in place of the browser's native one: a button that
 * shows the chosen option's label (or a placeholder in words) and opens a
 * listbox in the app's own popover. Up and Down, Home and End, typeahead on
 * the first letters, Enter or Space to choose, Escape to close without
 * choosing, Tab to close and move on. Disabled options are listed but cannot
 * be chosen; options may carry a `group` that draws a heading row. The list
 * opens upward when there is more room above the button than below. A choice
 * fires a bubbling `change` event with the value on the element itself, so a
 * handler written for a native select keeps its meaning. Clicks inside the
 * list cancel their default action: in a page that wraps the select in a
 * label element, the browser would otherwise answer an option click with a
 * synthetic click at the button, reopening the list after every choice.
 */
import { LitElement, html, nothing, type TemplateResult } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { ChevronDown } from 'lucide';

export interface GableSelectOption {
    value: string;
    label: string;
    disabled?: boolean;
    group?: string;
}

let selectSeq = 0;

@customElement('gable-select')
export class GableSelect extends LitElement {
    createRenderRoot() { return this; }

    @property({ type: Array }) options: GableSelectOption[] = [];
    @property({ type: String }) value = '';
    @property({ type: String }) placeholder = '';
    @property({ type: String }) label = '';
    @property({ type: Boolean }) disabled = false;

    @state() private _open = false;
    @state() private _activeIndex = -1;
    @state() private _openUpward = false;

    private _ids = `gable-select-${++selectSeq}`;
    private _typeahead = '';
    private _typeaheadAt = 0;
    private _onDocClick = (e: Event) => {
        if (!this.contains(e.target as Node)) this._close();
    };

    disconnectedCallback() {
        super.disconnectedCallback();
        document.removeEventListener('click', this._onDocClick, true);
    }

    /** The chosen option, or undefined when the value matches none. */
    get selectedOption(): GableSelectOption | undefined {
        return this.options.find((o) => o.value === this.value);
    }

    private _enabledIndexes(): number[] {
        return this.options.map((o, i) => (o.disabled ? -1 : i)).filter((i) => i >= 0);
    }

    private _openList() {
        if (this.disabled || this._open) return;
        const rect = this.getBoundingClientRect();
        const below = window.innerHeight - rect.bottom;
        const above = rect.top;
        this._openUpward = below < 240 && above > below;
        const selected = this.options.findIndex((o) => o.value === this.value && !o.disabled);
        this._activeIndex = selected >= 0 ? selected : (this._enabledIndexes()[0] ?? -1);
        this._open = true;
        document.addEventListener('click', this._onDocClick, true);
        this.updateComplete.then(() => this._scrollActiveIntoView());
    }

    private _close(restoreFocus = false) {
        if (!this._open) return;
        this._open = false;
        document.removeEventListener('click', this._onDocClick, true);
        if (restoreFocus) {
            // Synchronously: a change handler that opens a dialog must not
            // have a deferred focus steal it back from the dialog.
            this.querySelector<HTMLElement>('button')?.focus();
        }
    }

    private _toggle() {
        if (this._open) {
            this._close(true);
        } else {
            this._openList();
        }
    }

    private _moveActive(step: number) {
        const enabled = this._enabledIndexes();
        if (enabled.length === 0) return;
        const at = enabled.indexOf(this._activeIndex);
        const next = at < 0 ? (step > 0 ? 0 : enabled.length - 1) : (at + step + enabled.length) % enabled.length;
        this._activeIndex = enabled[next];
        this._scrollActiveIntoView();
    }

    private _moveActiveToEnd(last: boolean) {
        const enabled = this._enabledIndexes();
        if (enabled.length === 0) return;
        this._activeIndex = last ? enabled[enabled.length - 1] : enabled[0];
        this._scrollActiveIntoView();
    }

    private _typeaheadTo(char: string) {
        const now = Date.now();
        this._typeahead = now - this._typeaheadAt > 600 ? char : this._typeahead + char;
        this._typeaheadAt = now;
        const needle = this._typeahead.toLowerCase();
        const enabled = this._enabledIndexes();
        const start = enabled.indexOf(this._activeIndex);
        for (let step = 1; step <= enabled.length; step++) {
            const index = enabled[(start + step + enabled.length) % enabled.length];
            if (this.options[index].label.toLowerCase().startsWith(needle)) {
                if (this._open) {
                    this._activeIndex = index;
                    this._scrollActiveIntoView();
                } else {
                    this._choose(index);
                }
                return;
            }
        }
    }

    private _choose(index: number) {
        const option = this.options[index];
        if (!option || option.disabled) return;
        const changed = option.value !== this.value;
        this.value = option.value;
        this._close(true);
        if (changed) {
            this.dispatchEvent(new Event('change', { bubbles: true, composed: true }));
        }
    }

    private _scrollActiveIntoView() {
        const el = this.querySelector<HTMLElement>(`#${this._ids}-opt-${this._activeIndex}`);
        if (el && typeof el.scrollIntoView === 'function') {
            try {
                el.scrollIntoView({ block: 'nearest' });
            } catch {
                // scrollIntoView is best effort; jsdom does not implement layout.
            }
        }
    }

    private _onButtonKeydown(e: KeyboardEvent) {
        if (this.disabled) return;
        if (!this._open) {
            if (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                this._openList();
            } else if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
                this._typeaheadTo(e.key);
            }
            return;
        }
        switch (e.key) {
            case 'ArrowDown':
                e.preventDefault();
                this._moveActive(1);
                break;
            case 'ArrowUp':
                e.preventDefault();
                this._moveActive(-1);
                break;
            case 'Home':
                e.preventDefault();
                this._moveActiveToEnd(false);
                break;
            case 'End':
                e.preventDefault();
                this._moveActiveToEnd(true);
                break;
            case 'Enter':
            case ' ':
                e.preventDefault();
                if (this._activeIndex >= 0) this._choose(this._activeIndex);
                break;
            case 'Escape':
                e.preventDefault();
                e.stopPropagation();
                this._close(true);
                break;
            case 'Tab':
                this._close();
                break;
            default:
                if (e.key.length === 1 && !e.ctrlKey && !e.metaKey && !e.altKey) {
                    e.preventDefault();
                    this._typeaheadTo(e.key);
                }
        }
    }

    private _renderOption(option: GableSelectOption, index: number) {
        const active = index === this._activeIndex;
        const selected = option.value === this.value;
        return html`
            <div
                id="${this._ids}-opt-${index}"
                role="option"
                aria-selected=${selected ? 'true' : 'false'}
                aria-disabled=${option.disabled ? 'true' : 'false'}
                class="px-3 py-2 text-sm cursor-pointer ${option.disabled
                    ? 'text-zinc-600 cursor-not-allowed'
                    : active
                        ? 'bg-gable-green/10 text-white'
                        : selected
                            ? 'text-gable-green'
                            : 'text-zinc-300'}"
                @click=${() => this._choose(index)}
                @mouseenter=${() => { if (!option.disabled) this._activeIndex = index; }}
            >${option.label}</div>
        `;
    }

    private _renderList() {
        const rows: TemplateResult[] = [];
        let lastGroup: string | undefined;
        this.options.forEach((option, index) => {
            if (option.group !== undefined && option.group !== lastGroup) {
                rows.push(html`
                    <div role="presentation" class="px-3 pt-2 pb-1 text-xs font-semibold uppercase tracking-wide text-zinc-500">
                        ${option.group}
                    </div>
                `);
            }
            lastGroup = option.group;
            rows.push(this._renderOption(option, index));
        });
        return html`
            <div
                id="${this._ids}-list"
                role="listbox"
                aria-label=${this.label || nothing}
                @click=${(e: Event) => e.preventDefault()}
                class="absolute z-50 left-0 right-0 max-h-60 overflow-y-auto rounded-md bg-surface-2 border border-white/10 shadow-xl py-1 ${this._openUpward ? 'bottom-full mb-1' : 'top-full mt-1'}"
            >
                ${rows.length > 0
                    ? rows
                    : html`<div role="presentation" class="px-3 py-2 text-sm text-zinc-500">No options</div>`}
            </div>
        `;
    }

    render() {
        const chosen = this.selectedOption;
        const activeDesc = this._open && this._activeIndex >= 0 ? `${this._ids}-opt-${this._activeIndex}` : nothing;
        return html`
            <div class="relative">
                <button
                    type="button"
                    role="combobox"
                    aria-haspopup="listbox"
                    aria-expanded=${this._open ? 'true' : 'false'}
                    aria-controls="${this._ids}-list"
                    aria-activedescendant=${activeDesc}
                    aria-label=${this.label || nothing}
                    ?disabled=${this.disabled}
                    @click=${this._toggle}
                    @keydown=${this._onButtonKeydown}
                    class="w-full flex items-center justify-between gap-2 bg-slate-steel/50 border border-white/10 rounded-md py-2 px-3 text-sm focus:outline-none focus:ring-1 focus:ring-gable-green/50 disabled:opacity-50 disabled:cursor-not-allowed ${chosen ? 'text-white' : 'text-zinc-500'}"
                >
                    <span class="truncate">${chosen ? chosen.label : this.placeholder}</span>
                    ${icon(ChevronDown, 16, 'shrink-0 text-zinc-500')}
                </button>
                ${this._open ? this._renderList() : nothing}
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-select': GableSelect;
    }
}
