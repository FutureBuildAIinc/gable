// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * The app's own date field (G1 walk note N15): a text box that shows the day
 * in the date module's style and accepts typing in it or as YYYY-MM-DD, plus
 * a calendar in the app's own popover (a month grid whose week starts as the
 * locale starts it; arrows move a day or a week, Page Up and Page Down a
 * month, Home and End the week, Enter picks, Escape closes; today is marked;
 * min and max are honoured). The value is always YYYY-MM-DD (or empty) for
 * the API, and commits fire `input` and `change`. An empty field says what to
 * do in words ("Choose a date"), never "mm/dd/yyyy".
 *
 * Light DOM, so Tailwind classes apply as in the neighbouring components.
 */
import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { Calendar, ChevronLeft, ChevronRight } from 'lucide';
import { formatDay, parseDay, todayDay, weekStart, weekdayNames, monthName } from '../../lib/dates.ts';

let nextId = 0;

// The popover's box: w-64 is 256px; the grid with its header is about 320px.
const POPOVER_W = 256;
const POPOVER_H = 320;
const POPOVER_GAP = 4;
const VIEWPORT_MARGIN = 8;

/**
 * Where the calendar pops, from the field's box and the viewport: under the
 * field, clamped sideways into the screen, and flipped above the field when
 * the space below is short and the space above is longer. Fixed positioning,
 * so a modal's scroll or hidden overflow never clips it.
 */
export function placePopover(
    rect: { left: number; bottom: number; top: number },
    viewportW: number,
    viewportH: number,
): { top: number; left: number } {
    const left = Math.max(VIEWPORT_MARGIN, Math.min(rect.left, viewportW - POPOVER_W - VIEWPORT_MARGIN));
    const below = viewportH - rect.bottom;
    if (below < POPOVER_H + POPOVER_GAP && rect.top > below) {
        return { top: Math.max(VIEWPORT_MARGIN, rect.top - POPOVER_H - POPOVER_GAP), left };
    }
    const top = rect.bottom + POPOVER_GAP;
    return { top: Math.min(top, viewportH - POPOVER_H - VIEWPORT_MARGIN), left };
}

@customElement('gable-date-field')
export class GableDateField extends LitElement {
    createRenderRoot() { return this; }

    /** The day for the API: YYYY-MM-DD, or empty. */
    @property({ type: String }) value = '';
    /** Bounds, YYYY-MM-DD. */
    @property({ type: String }) min = '';
    @property({ type: String }) max = '';
    /** The accessible name of the text box. */
    @property({ type: String }) label = '';
    @property({ type: Boolean }) disabled = false;
    /** The host's surface: the dark app by default, "light" on a light card. */
    @property({ type: String }) tone = 'dark';

    @state() private _text = '';
    @state() private _open = false;
    @state() private _invalid = false;
    @state() private _viewYear = 0;
    @state() private _viewMonth = 0;
    @state() private _focusDay = '';
    @state() private _popTop = 0;
    @state() private _popLeft = 0;

    private _id = `gable-date-field-${++nextId}`;
    // True while the grid holds the keyboard focus, so a re-render that
    // replaces the focused cell's node (the month turned) knows to give the
    // focus to the day the move landed on.
    private _gridFocus = false;
    // Set by the grid's own key handler and the month buttons: a key or a
    // click there proves the calendar had the interaction, independent of
    // what focus events the engine fires while the old cell's node goes away
    // (Chromium sends a focusout on removal, jsdom none). updated() consumes
    // it to land the focus on the moved-to day.
    private _refocus = false;
    private _outside = (e: MouseEvent) => {
        if (this._open && !this.contains(e.target as Node)) this._close();
    };
    // The calendar is placed from the field's box on the screen; a scroll or
    // resize moves that box, so an open calendar closes rather than floats
    // free of its field.
    private _repositioning = () => this._close();

    connectedCallback() {
        super.connectedCallback();
        document.addEventListener('mousedown', this._outside);
    }

    disconnectedCallback() {
        document.removeEventListener('mousedown', this._outside);
        this._unwatchViewport();
        super.disconnectedCallback();
    }

    private _watchViewport() {
        window.addEventListener('scroll', this._repositioning, true);
        window.addEventListener('resize', this._repositioning);
    }

    private _unwatchViewport() {
        window.removeEventListener('scroll', this._repositioning, true);
        window.removeEventListener('resize', this._repositioning);
    }

    willUpdate(changed: Map<string, unknown>) {
        if (changed.has('value')) {
            // An external set repaints the box; the box's own typing is left
            // alone while the popover is closed over it.
            const shown = formatDay(this.value);
            if (this.value === '' || shown !== this.value) {
                if (this._text !== shown && (this.value === '' || parseDay(this._text) !== this.value)) {
                    this._text = this.value ? shown : '';
                    this._invalid = false;
                }
            }
        }
    }

    /** Commit a business day (or empty), repaint the box, fire the events. */
    private _commit(day: string) {
        this.value = day;
        this._text = day ? formatDay(day) : '';
        this._invalid = false;
        this.dispatchEvent(new Event('input', { bubbles: true, composed: true }));
        this.dispatchEvent(new Event('change', { bubbles: true, composed: true }));
    }

    private _inBounds(day: string): boolean {
        if (!day) return false;
        if (this.min && day < this.min) return false;
        if (this.max && day > this.max) return false;
        return true;
    }

    private _onTextInput(e: Event) {
        this._text = (e.target as HTMLInputElement).value;
        this._invalid = false;
    }

    private _onTextCommit() {
        const t = this._text.trim();
        if (t === '') {
            if (this.value !== '') this._commit('');
            this._invalid = false;
            return;
        }
        const day = parseDay(t);
        if (day && this._inBounds(day)) {
            if (day !== this.value || this._invalid) this._commit(day);
            else this._text = formatDay(day);
            this._invalid = false;
        } else {
            this._invalid = true;
        }
    }

    private _onTextKeydown(e: KeyboardEvent) {
        if (e.key === 'Enter') {
            e.preventDefault();
            this._onTextCommit();
        } else if (e.key === 'ArrowDown' && !this._open) {
            e.preventDefault();
            this._openCalendar();
        } else if (e.key === 'Escape' && this._open) {
            e.preventDefault();
            this._close();
        }
    }

    private _openCalendar() {
        if (this.disabled) return;
        const base = this.value && this._inBounds(this.value) ? this.value : todayDay();
        const [y, m] = base.split('-').map(Number);
        this._viewYear = y;
        this._viewMonth = m;
        this._focusDay = base;
        const rect = this.querySelector('input')?.getBoundingClientRect();
        if (rect) {
            const at = placePopover(rect, window.innerWidth, window.innerHeight);
            this._popTop = at.top;
            this._popLeft = at.left;
        }
        this._open = true;
        this._watchViewport();
    }

    private _close() {
        if (this._open) this._unwatchViewport();
        this._open = false;
    }

    private _pick(day: string) {
        if (!this._inBounds(day)) return;
        this._commit(day);
        this._close();
        this.querySelector('input')?.focus();
    }

    /** Move the grid's focus by days or months; the view follows. */
    private _move(day: string, deltaDays: number, deltaMonths: number) {
        const [y, m, d] = day.split('-').map(Number);
        let date: Date;
        if (deltaMonths !== 0) {
            // A month step keeps the day of the month when it can (the 31st
            // of a short month lands on its last day).
            const target = new Date(y, m - 1 + deltaMonths, 1);
            const last = new Date(target.getFullYear(), target.getMonth() + 1, 0).getDate();
            date = new Date(target.getFullYear(), target.getMonth(), Math.min(d, last));
        } else {
            date = new Date(y, m - 1, d + deltaDays);
        }
        let next = `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')}`;
        // The focus never sits on a disabled day: clamp to the bounds.
        if (this.min && next < this.min) next = this.min;
        if (this.max && next > this.max) next = this.max;
        this._focusDay = next;
        this._viewYear = Number(next.slice(0, 4));
        this._viewMonth = Number(next.slice(5, 7));
    }

    private _onGridKeydown(e: KeyboardEvent) {
        const day = this._focusDay || todayDay();
        const start = weekStart();
        const weekday = (new Date(Number(day.slice(0, 4)), Number(day.slice(5, 7)) - 1, Number(day.slice(8, 10))).getDay() - start + 7) % 7;
        switch (e.key) {
            case 'ArrowLeft': e.preventDefault(); this._refocus = true; this._move(day, -1, 0); break;
            case 'ArrowRight': e.preventDefault(); this._refocus = true; this._move(day, 1, 0); break;
            case 'ArrowUp': e.preventDefault(); this._refocus = true; this._move(day, -7, 0); break;
            case 'ArrowDown': e.preventDefault(); this._refocus = true; this._move(day, 7, 0); break;
            case 'PageUp': e.preventDefault(); this._refocus = true; this._move(day, 0, e.shiftKey ? -12 : -1); break;
            case 'PageDown': e.preventDefault(); this._refocus = true; this._move(day, 0, e.shiftKey ? 12 : 1); break;
            case 'Home': e.preventDefault(); this._refocus = true; this._move(day, -weekday, 0); break;
            case 'End': e.preventDefault(); this._refocus = true; this._move(day, 6 - weekday, 0); break;
            case 'Enter':
            case ' ':
                e.preventDefault();
                this._pick(day);
                break;
            case 'Escape':
                e.preventDefault();
                this._close();
                this.querySelector('input')?.focus();
                break;
            case 'Tab':
                this._close();
                break;
        }
    }

    private _shiftMonth(delta: number) {
        this._refocus = true;
        const d = new Date(this._viewYear, this._viewMonth - 1 + delta, 1);
        this._viewYear = d.getFullYear();
        this._viewMonth = d.getMonth() + 1;
        const day = Number(this._focusDay.slice(8, 10));
        const last = new Date(this._viewYear, this._viewMonth, 0).getDate();
        let next = `${this._viewYear}-${String(this._viewMonth).padStart(2, '0')}-${String(Math.min(day, last)).padStart(2, '0')}`;
        if (this.min && next < this.min) next = this.min;
        if (this.max && next > this.max) next = this.max;
        this._focusDay = next;
        this._viewYear = Number(next.slice(0, 4));
        this._viewMonth = Number(next.slice(5, 7));
    }

    /** The visible month's days: leading blanks, then each day. */
    private _grid(): Array<string | null> {
        const start = weekStart();
        const first = new Date(this._viewYear, this._viewMonth - 1, 1);
        const blanks = (first.getDay() - start + 7) % 7;
        const days = new Date(this._viewYear, this._viewMonth, 0).getDate();
        const out: Array<string | null> = Array.from({ length: blanks }, () => null);
        for (let d = 1; d <= days; d++) {
            out.push(`${this._viewYear}-${String(this._viewMonth).padStart(2, '0')}-${String(d).padStart(2, '0')}`);
        }
        return out;
    }

    render() {
        const light = this.tone === 'light';
        const inputClasses = light
            ? `w-full bg-black/5 border rounded-lg px-3 py-2 pr-9 text-sm text-zinc-900 placeholder-zinc-500 focus:outline-none focus:ring-1 focus:ring-blueprint-blue/50 ${this._invalid ? 'border-safety-red' : 'border-zinc-300'}`
            : `w-full bg-white/5 border rounded-lg px-3 py-2 pr-9 text-sm text-white placeholder-zinc-500 focus:outline-none focus:ring-1 focus:ring-gable-green/50 ${this._invalid ? 'border-safety-red' : 'border-white/10'}`;
        return html`
            <div class="relative">
                <input
                    id=${this._id}
                    type="text"
                    role="combobox"
                    aria-expanded=${this._open ? 'true' : 'false'}
                    aria-haspopup="dialog"
                    aria-controls="${this._id}-grid"
                    aria-label=${this.label || 'Date'}
                    aria-invalid=${this._invalid ? 'true' : 'false'}
                    placeholder="Choose a date"
                    .value=${this._text}
                    ?disabled=${this.disabled}
                    @input=${this._onTextInput}
                    @blur=${this._onTextCommit}
                    @keydown=${this._onTextKeydown}
                    class=${inputClasses}
                />
                <button
                    type="button"
                    tabindex="-1"
                    aria-label=${this._open ? 'Close the calendar' : 'Open the calendar'}
                    ?disabled=${this.disabled}
                    @click=${() => (this._open ? this._close() : this._openCalendar())}
                    @mousedown=${(e: Event) => e.preventDefault()}
                    class="absolute right-2 top-1/2 -translate-y-1/2 text-zinc-400 hover:text-white transition-colors"
                >${icon(Calendar, 16)}</button>
                ${this._invalid ? html`
                    <p class="mt-1 text-xs text-safety-red">Use a day like ${formatDay(todayDay())} or ${todayDay()}</p>
                ` : nothing}
                ${this._open ? this._renderPopover() : nothing}
            </div>
        `;
    }

    private _renderPopover() {
        const names = weekdayNames();
        const today = todayDay();
        return html`
            <div
                role="dialog"
                aria-label="Choose a date"
                popover="manual"
                style="position: fixed; inset: auto; margin: 0; top: ${this._popTop}px; left: ${this._popLeft}px;"
                class="z-50 w-64 rounded-xl bg-slate-steel border border-white/10 p-3 shadow-xl"
            >
                <div class="flex items-center justify-between mb-2">
                    <button
                        type="button"
                        aria-label="Previous month"
                        @click=${() => this._shiftMonth(-1)}
                        class="p-1 rounded text-zinc-400 hover:text-white hover:bg-white/10 transition-colors"
                    >${icon(ChevronLeft, 16)}</button>
                    <span class="text-sm font-medium text-white">${monthName(this._viewMonth, this._viewYear)}</span>
                    <button
                        type="button"
                        aria-label="Next month"
                        @click=${() => this._shiftMonth(1)}
                        class="p-1 rounded text-zinc-400 hover:text-white hover:bg-white/10 transition-colors"
                    >${icon(ChevronRight, 16)}</button>
                </div>
                <div
                    id="${this._id}-grid"
                    role="grid"
                    aria-label=${monthName(this._viewMonth, this._viewYear)}
                    @keydown=${this._onGridKeydown}
                    @focusin=${() => { this._gridFocus = true; }}
                    @focusout=${(e: FocusEvent) => {
                        // A focusout whose target is already gone is the
                        // render removing the old cell, not the person leaving.
                        if (!(e.target as Node).isConnected) return;
                        const to = e.relatedTarget as Node | null;
                        if (!to || !this.querySelector('[role="grid"]')?.contains(to)) this._gridFocus = false;
                    }}
                    class="grid grid-cols-7 gap-1"
                >
                    ${names.map((n) => html`
                        <span role="columnheader" class="text-center text-xs text-zinc-500 py-1">${n}</span>
                    `)}
                    ${this._grid().map((day) => {
                        if (day === null) return html`<span></span>`;
                        const d = Number(day.slice(8, 10));
                        const selected = day === this.value;
                        const isToday = day === today;
                        const focused = day === this._focusDay;
                        const out = !this._inBounds(day);
                        const classes = [
                            'w-8 h-8 mx-auto rounded text-sm transition-colors',
                            selected ? 'bg-gable-green/20 text-gable-green font-semibold' : 'text-zinc-300',
                            isToday ? 'ring-1 ring-blueprint-blue' : '',
                            focused ? 'bg-white/10 outline-none' : '',
                            out ? 'opacity-30 cursor-not-allowed' : 'hover:bg-white/10 cursor-pointer',
                        ].join(' ');
                        return html`
                            <span role="gridcell">
                                <button
                                    type="button"
                                    aria-selected=${selected ? 'true' : 'false'}
                                    aria-current=${isToday ? 'date' : nothing}
                                    aria-label=${formatDay(day)}
                                    tabindex=${focused ? '0' : '-1'}
                                    ?disabled=${out}
                                    @click=${() => this._pick(day)}
                                    @focus=${() => { this._focusDay = day; }}
                                    class=${classes}
                                >${d}</button>
                            </span>
                        `;
                    })}
                </div>
            </div>
        `;
    }

    /** After the grid re-renders, keep the focused day's button focused. */
    updated() {
        if (this._open) {
            // The dialog carries popover="manual": on a real engine this puts
            // it in the top layer, which no ancestor's transform, filter or
            // overflow can offset or clip. Manual keeps the dismissal ours
            // (the outside click and the Escape handlers). A popover not yet
            // shown is display:none, so show it on every render while open.
            const pop = this.querySelector('[popover]') as (HTMLElement & { showPopover?: () => void }) | null;
            if (pop && typeof pop.showPopover === 'function') {
                try {
                    pop.showPopover();
                } catch {
                    // already showing
                }
            }
            const btn = this.querySelector(`#${this._id}-grid button[tabindex="0"]`) as HTMLElement | null;
            // Pull focus to the landed day when a grid key or month button
            // moved (the engine's focus events during removal are beside the
            // point), when the grid had the focus before the render, or when
            // the calendar just opened from the field.
            if (btn && document.activeElement !== btn && (this._refocus || this._gridFocus || this.contains(document.activeElement))) {
                btn.focus();
            }
            this._refocus = false;
        } else {
            this._gridFocus = false;
            this._refocus = false;
        }
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-date-field': GableDateField;
    }
}
