// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import { icon } from '../../lib/icons.ts';
import { QuoteService, QuoteApiError, quoteErrorMessage } from '../../services/QuoteService.ts';
import { SalesTeamService } from '../../services/SalesTeamService.ts';
import { ToastService } from '../../lib/toast-service.ts';
import { formatDay, todayDay } from '../../lib/dates.ts';
import { followUpDay, followUpOverdue } from '../../lib/followUp.ts';
import type { SalesPerson } from '../../types/salesteam.ts';
import type { Quote } from '../../types/quote.ts';
import { CalendarClock, Clock, User, X } from 'lucide';
import '../ui/gable-select.ts';
import '../ui/gable-date-field.ts';
import type { GableDateField } from '../ui/gable-date-field.ts';

/**
 * The follow up on a quote: when to chase it and who owns the chase.
 *
 * The card shows the date, the owner and an overdue mark, and writes both
 * with the quote's revision (ADR 0001): the write is refused as stale when
 * another session moved the quote first. Completing a follow up is clearing
 * it: a null date clears the date and the owner together, and what happened
 * is logged as an activity on the quote rather than kept as a state on it.
 */
@customElement('gable-follow-up-card')
export class GableFollowUpCard extends LitElement {
    createRenderRoot() { return this; }

    /** The quote as the page holds it: the date, the owner and the revision. */
    @property({ attribute: false }) quote: Quote | null = null;

    @state() private team: SalesPerson[] = [];
    @state() private ownerOptions: { value: string; label: string }[] = [{ value: '', label: '-- No Owner --' }];
    @state() private editing = false;
    @state() private saving = false;
    @state() private day = '';
    @state() private ownerId = '';

    connectedCallback() {
        super.connectedCallback();
        this._loadTeam();
    }

    private async _loadTeam() {
        try {
            this.team = await SalesTeamService.listSalesTeam();
        } catch {
            // The owner is optional: a card with no team still sets a date.
            this.team = [];
        }
        this.ownerOptions = [
            { value: '', label: '-- No Owner --' },
            ...this.team.filter(sp => sp.is_active).map(sp => ({ value: sp.id, label: `${sp.name} (${sp.role})` })),
        ];
    }

    /** The date the card shows: the follow up day in the person's own calendar. */
    private _followUpDay(): string | null {
        return followUpDay(this.quote?.follow_up_at);
    }

    /**
     * The overdue mark: a follow up whose day is before today. The queue's
     * "today" is the quote's branch's calendar (the server reads it that
     * way); the mark is drawn in the person's own calendar, which is the same
     * calendar for anyone working in their own branch.
     */
    private _overdue(): boolean {
        return followUpOverdue(this.quote?.follow_up_at);
    }

    private _openEditor() {
        this.day = this._followUpDay() ?? todayDay();
        this.ownerId = this.quote?.follow_up_owner_id ?? '';
        this.editing = true;
    }

    private async _save() {
        if (!this.quote) return;
        if (!this.day) {
            ToastService.show('Choose a date', 'error');
            return;
        }
        this.saving = true;
        try {
            // The day as a moment: local midnight on the chosen day, so the
            // card reads back the day the person chose (a machine west of UTC
            // would read the day before on a UTC midnight).
            const at = new Date(`${this.day}T00:00:00`).toISOString();
            const updated = await QuoteService.setFollowUp(
                this.quote.id,
                { follow_up_at: at, follow_up_owner_id: this.ownerId || null },
                this.quote.revision,
            );
            this.editing = false;
            ToastService.show('Follow up set', 'success');
            this._changed(updated);
        } catch (error) {
            if (error instanceof QuoteApiError && error.isStaleRevision) {
                ToastService.show('This quote changed elsewhere. Reloaded.', 'error');
                this.editing = false;
                this._changed(null);
                return;
            }
            ToastService.show(quoteErrorMessage(error, 'Unknown error'), 'error');
        } finally {
            this.saving = false;
        }
    }

    private async _clear() {
        if (!this.quote) return;
        this.saving = true;
        try {
            // A null date clears the date and the owner together.
            const updated = await QuoteService.setFollowUp(
                this.quote.id,
                { follow_up_at: null, follow_up_owner_id: null },
                this.quote.revision,
            );
            this.editing = false;
            ToastService.show('Follow up cleared', 'success');
            this._changed(updated);
        } catch (error) {
            if (error instanceof QuoteApiError && error.isStaleRevision) {
                ToastService.show('This quote changed elsewhere. Reloaded.', 'error');
                this.editing = false;
                this._changed(null);
                return;
            }
            ToastService.show(quoteErrorMessage(error, 'Unknown error'), 'error');
        } finally {
            this.saving = false;
        }
    }

    /** The page reloads the quote: the card does not hold the quote itself. */
    private _changed(updated: Quote | null) {
        this.dispatchEvent(new CustomEvent('follow-up-changed', {
            detail: { quote: updated },
            bubbles: true,
            composed: true,
        }));
    }

    render() {
        const quote = this.quote;
        if (!quote) return nothing;

        const day = this._followUpDay();
        const overdue = this._overdue();
        const ownerName = quote.follow_up_owner_name;

        return html`
            <div class="bg-slate-steel border border-white/10 rounded-xl p-5" data-testid="follow-up-card">
                <div class="flex items-start justify-between gap-4 flex-wrap">
                    <div class="flex items-start gap-3">
                        ${icon(overdue ? Clock : CalendarClock, 20, `w-5 h-5 shrink-0 mt-0.5 ${overdue ? 'text-rose-400' : 'text-gable-green'}`)}
                        <div>
                            <div class="flex items-center gap-2">
                                <h3 class="text-sm font-semibold text-white uppercase tracking-wider">Follow up</h3>
                                ${overdue ? html`
                                    <span class="px-2 py-0.5 rounded-md text-xs font-medium text-rose-400 bg-rose-500/10 border border-rose-500/20" data-testid="follow-up-overdue">
                                        Overdue
                                    </span>
                                ` : nothing}
                            </div>
                            ${day ? html`
                                <div class="mt-1 font-mono text-sm ${overdue ? 'text-rose-400' : 'text-white'}" data-testid="follow-up-date">
                                    ${formatDay(day)}
                                </div>
                            ` : html`
                                <div class="mt-1 text-sm text-zinc-500" data-testid="follow-up-date">No follow up set</div>
                            `}
                            <div class="mt-1 flex items-center gap-1.5 text-xs text-zinc-400" data-testid="follow-up-owner">
                                ${icon(User, 12, 'w-3 h-3 text-zinc-500')}
                                ${ownerName ? ownerName : 'Nobody owns this follow up'}
                            </div>
                        </div>
                    </div>

                    ${!this.editing ? html`
                        <div class="flex gap-2 shrink-0">
                            <button @click=${() => this._openEditor()} ?disabled=${this.saving}
                                class="px-3 py-1.5 text-xs font-medium rounded-lg bg-gable-green text-black hover:bg-gable-green/90 transition-colors disabled:opacity-50">
                                ${day ? 'Change' : 'Set follow up'}
                            </button>
                            ${day ? html`
                                <button @click=${() => this._clear()} ?disabled=${this.saving}
                                    class="px-3 py-1.5 text-xs font-medium rounded-lg border border-white/20 text-zinc-300 hover:bg-white/5 transition-colors disabled:opacity-50 flex items-center gap-1">
                                    ${icon(X, 12, 'w-3 h-3')} Clear
                                </button>
                            ` : nothing}
                        </div>
                    ` : nothing}
                </div>

                ${this.editing ? html`
                    <div class="mt-4 pt-4 border-t border-white/10 grid grid-cols-1 md:grid-cols-2 gap-4">
                        <div>
                            <label for="follow-up-day" class="block text-sm font-medium text-zinc-300 mb-1">Date</label>
                            <gable-date-field
                                id="follow-up-day"
                                label="Follow up date"
                                required
                                .value=${this.day}
                                @change=${(e: Event) => this.day = (e.target as GableDateField).value}
                            ></gable-date-field>
                        </div>
                        <div>
                            <label for="follow-up-owner-select" class="block text-sm font-medium text-zinc-300 mb-1">Owner (Optional)</label>
                            <gable-select
                                id="follow-up-owner-select"
                                .value=${this.ownerId}
                                .options=${this.ownerOptions}
                                @change=${(e: Event) => this.ownerId = (e.target as HTMLSelectElement).value}
                            ></gable-select>
                        </div>
                        <div class="md:col-span-2 flex justify-end gap-2">
                            <button @click=${() => { this.editing = false; }}
                                class="px-4 py-2 border border-white/20 rounded-md text-sm font-medium text-zinc-300 hover:bg-white/5 transition-colors">
                                Cancel
                            </button>
                            <button @click=${() => this._save()} ?disabled=${this.saving}
                                class="px-4 py-2 bg-gable-green text-black rounded-md text-sm font-bold hover:bg-gable-green/90 transition-colors disabled:opacity-50">
                                ${this.saving ? 'Saving...' : 'Save follow up'}
                            </button>
                        </div>
                    </div>
                ` : nothing}
            </div>
        `;
    }
}

declare global {
    interface HTMLElementTagNameMap {
        'gable-follow-up-card': GableFollowUpCard;
    }
}
