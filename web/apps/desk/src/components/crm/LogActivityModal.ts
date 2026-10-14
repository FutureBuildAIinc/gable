// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import type { CreateActivityRequest, Contact, ActivityType } from '../../types/crm';
import { crmApi } from '../../services/crmApi';
import { ToastService } from '../../lib/toast-service';
import { todayDay } from '../../lib/dates.ts';

// The current hour and minute, HH:MM, as the time entry's default.
function nowTime(): string {
  const n = new Date();
  return `${String(n.getHours()).padStart(2, '0')}:${String(n.getMinutes()).padStart(2, '0')}`;
}

// The app's own time entry takes HH:MM and nothing else.
function parseTime(text: string): string | null {
  const m = /^(\d{1,2}):(\d{2})$/.exec(text.trim());
  if (!m) return null;
  const h = Number(m[1]);
  const min = Number(m[2]);
  if (h > 23 || min > 59) return null;
  return `${String(h).padStart(2, '0')}:${String(min).padStart(2, '0')}`;
}
import '../ui/gable-date-field.ts';
import type { GableDateField } from '../ui/gable-date-field.ts';

const ACTIVITY_TYPES: ActivityType[] = ['call', 'meeting', 'email', 'note'];

@customElement('gable-log-activity-modal')
export class GableLogActivityModal extends LitElement {
  createRenderRoot() { return this; }

  @property({ type: String, attribute: 'customer-id' }) customerId = '';
  @property({ type: Array }) contacts: Contact[] = [];

  @state() private _loading = false;
  @state() private _activityType: ActivityType = 'call';
  @state() private _description = '';
  @state() private _activityDay = todayDay();
  @state() private _activityTime = nowTime();
  @state() private _contactId = '';

  private async _handleSubmit(e: Event) {
    e.preventDefault();
    try {
      if (!this._activityDay) {
        ToastService.show('Choose a date', 'error');
        return;
      }
      const time = parseTime(this._activityTime);
      if (!time) {
        ToastService.show('Enter the time as HH:MM, for example 09:30', 'error');
        return;
      }
      this._loading = true;
      const payload: CreateActivityRequest = {
        activity_type: this._activityType,
        description: this._description,
        // The day and time the person entered, in their own zone.
        activity_date: new Date(`${this._activityDay}T${time}:00`).toISOString(),
        contact_id: this._contactId === '' ? undefined : this._contactId,
      };
      await crmApi.createActivity(this.customerId, payload);
      this.dispatchEvent(new CustomEvent('success', { bubbles: true, composed: true }));
    } catch (err: unknown) {
      ToastService.show(err instanceof Error ? err.message : 'Failed to log activity', 'error');
    } finally {
      this._loading = false;
    }
  }

  private _close() {
    this.dispatchEvent(new CustomEvent('close', { bubbles: true, composed: true }));
  }

  render() {
    return html`
      <div class="fixed inset-0 bg-gray-600 bg-opacity-75 flex justify-center items-center z-50">
        <div class="bg-white rounded-lg shadow-xl w-full max-w-lg overflow-hidden">
          <div class="px-6 py-4 border-b border-gray-200 flex justify-between items-center bg-gray-50">
            <h3 class="text-lg font-bold text-gray-900">Log Activity</h3>
            <button @click=${this._close} class="text-gray-400 hover:text-gray-600">
              <svg class="w-6 h-6" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"></path></svg>
            </button>
          </div>

          <form @submit=${this._handleSubmit} class="px-6 py-4">
            <div class="mb-4">
              <label class="block text-sm font-medium text-gray-700 mb-1">Activity Type</label>
              <div class="grid grid-cols-4 gap-2">
                ${ACTIVITY_TYPES.map(type => html`
                  <button
                    type="button"
                    @click=${() => this._activityType = type}
                    class="px-3 py-2 text-sm font-medium rounded-md border ${this._activityType === type
                      ? 'bg-blue-50 border-blue-500 text-blue-700'
                      : 'bg-white border-gray-300 text-gray-700 hover:bg-gray-50'
                    }"
                  >
                    ${type.charAt(0).toUpperCase() + type.slice(1)}
                  </button>
                `)}
              </div>
            </div>

            <div class="mb-4">
              <div class="grid grid-cols-2 gap-3">
                <div>
                  <label class="block text-sm font-medium text-gray-700 mb-1">Date</label>
                  <gable-date-field
                    label="Activity date"
                    .value=${this._activityDay}
                    @change=${(e: Event) => this._activityDay = (e.target as GableDateField).value}
                  ></gable-date-field>
                </div>
                <div>
                  <label for="log-activity-time" class="block text-sm font-medium text-gray-700 mb-1">Time</label>
                  <input
                    id="log-activity-time"
                    type="text"
                    inputmode="numeric"
                    aria-label="Activity time"
                    placeholder="HH:MM"
                    .value=${this._activityTime}
                    @input=${(e: InputEvent) => this._activityTime = (e.target as HTMLInputElement).value}
                    class="w-full px-3 py-2 border border-zinc-300 rounded-md text-zinc-900 focus:ring-2 focus:ring-blueprint-blue/50 focus:border-blueprint-blue"
                  />
                </div>
              </div>
            </div>

            <div class="mb-4">
              <label for="log-activity-contact" class="block text-sm font-medium text-gray-700 mb-1">Contact (Optional)</label>
              <select
                id="log-activity-contact"
                .value=${this._contactId}
                @change=${(e: Event) => this._contactId = (e.target as HTMLSelectElement).value}
                class="w-full px-3 py-2 border border-gray-300 rounded-md focus:ring-blue-500 focus:border-blue-500"
              >
                <option value="">-- No Specific Contact --</option>
                ${this.contacts.map(c => html`
                  <option value=${c.id}>${c.first_name} ${c.last_name} (${c.role})</option>
                `)}
              </select>
            </div>

            <div class="mb-6">
              <label for="log-activity-description" class="block text-sm font-medium text-gray-700 mb-1">Description / Notes</label>
              <textarea
                id="log-activity-description"
                required
                rows="4"
                .value=${this._description}
                @input=${(e: InputEvent) => this._description = (e.target as HTMLTextAreaElement).value}
                class="w-full px-3 py-2 border border-gray-300 rounded-md focus:ring-blue-500 focus:border-blue-500"
                placeholder="Details about the ${this._activityType.toLowerCase()}..."
              ></textarea>
            </div>

            <div class="flex justify-end gap-3">
              <button type="button" @click=${this._close}
                class="px-4 py-2 border border-gray-300 rounded-md text-sm font-medium text-gray-700 bg-white hover:bg-gray-50">
                Cancel
              </button>
              <button type="submit" ?disabled=${this._loading}
                class="px-4 py-2 border border-transparent rounded-md shadow-sm text-sm font-medium text-white bg-blue-600 hover:bg-blue-700 disabled:opacity-50">
                ${this._loading ? 'Saving...' : 'Log Activity'}
              </button>
            </div>
          </form>
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-log-activity-modal': GableLogActivityModal;
  }
}
