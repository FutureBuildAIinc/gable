// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html, nothing } from 'lit';
import { customElement, property, state } from 'lit/decorators.js';
import '../../components/ui/gable-select';
import { posService } from '../../services/POSService';
import { apiErrorMessage } from '../../services/apiError';
import { promptDialog } from '../../components/ui/app-dialog';
import type { Sale, SaleLine, ScaleStatus, ScaleTicket, ManualWeightReason } from '../../types/pos';
import {
  canWeighLine,
  formatLb,
  scaleReadingText,
  buildManualWeightPayload,
} from './scale';

const POLL_MS = 2000;
const muted = 'rgb(var(--gb-till-muted))';
const soft = 'rgb(var(--gb-till-soft))';
const text = 'rgb(var(--gb-till-text))';
const field = 'rgb(var(--gb-till-field))';
const edge = 'rgb(var(--gb-till-edge))';
const bad = 'rgb(var(--gb-till-bad))';
const go = 'rgb(var(--gb-till-go))';
const plate = 'rgb(var(--gb-plate))';

@customElement('gable-pos-scale-panel')
export class ScalePanel extends LitElement {
  createRenderRoot() { return this; }

  @property({ type: String, attribute: 'register-id' }) registerId = '';
  @property({ type: Object }) sale: Sale | null = null;

  @state() private _status: ScaleStatus | null = null;
  @state() private _tickets: ScaleTicket[] = [];
  @state() private _reasons: ManualWeightReason[] = [];
  @state() private _vehicleRef = '';
  @state() private _selectedLineOutId = '';
  @state() private _selectedTicketId = '';
  @state() private _vehicleOut = '';
  @state() private _selectedLineManualId = '';
  @state() private _manualWeight = '';
  @state() private _manualTare = '';
  @state() private _manualReasonCode = '';
  @state() private _manualNote = '';
  @state() private _busy = false;
  @state() private _error: string | null = null;

  private _loadSeq = 0;
  private _timer: number | null = null;

  connectedCallback() {
    super.connectedCallback();
    void this._loadStatus();
    void this._loadTickets();
    void this._loadReasons();
    this._schedulePoll();
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    if (this._timer) window.clearTimeout(this._timer);
  }

  private _schedulePoll() {
    if (this._timer) window.clearTimeout(this._timer);
    this._timer = window.setTimeout(() => {
      void this._loadStatus().finally(() => {
        if (this.isConnected) this._schedulePoll();
      });
    }, POLL_MS);
  }

  private async _loadStatus() {
    if (!this.registerId) return;
    const seq = ++this._loadSeq;
    try {
      const status = await posService.getScaleStatus(this.registerId);
      if (seq !== this._loadSeq) return;
      this._status = status;
    } catch (_err: unknown) {
      if (seq !== this._loadSeq) return;
      this._status = null;
    }
  }

  private async _loadTickets() {
    if (!this.registerId) return;
    try {
      this._tickets = await posService.listScaleTickets(this.registerId, 'open');
    } catch (err: unknown) {
      this._tickets = [];
      this._error = apiErrorMessage(err, 'Failed to load the open weigh-ins');
    }
  }

  private async _loadReasons() {
    try {
      this._reasons = await posService.scaleManualReasons();
    } catch (_err: unknown) {
      this._reasons = [];
    }
  }

  private _weighableLines(): SaleLine[] {
    return (this.sale?.lines ?? []).filter(canWeighLine);
  }

  private _lineOptions() {
    return [{ value: '', label: 'Choose a line' }, ...this._weighableLines().map((l) => ({
      value: l.id,
      label: `${l.description} (${l.quantity ?? ''} ${l.uom ?? ''})`,
    }))];
  }

  private _ticketOptions() {
    return [{ value: '', label: 'Choose an open weigh-in' }, ...this._tickets.map((t) => ({
      value: t.id,
      label: `${t.vehicle_ref} ${formatLb(t.gross_lb)} lb ${t.ticket_number}`,
    }))];
  }

  private _reasonOptions() {
    return [{ value: '', label: 'Pick a reason' }, ...this._reasons.map((r) => ({
      value: r.code,
      label: r.label,
    }))];
  }

  private _selectedReason(): ManualWeightReason | null {
    return this._reasons.find((r) => r.code === this._manualReasonCode) ?? null;
  }

  private _emitSale(sale: Sale) {
    this.dispatchEvent(new CustomEvent('scale-sale', { detail: sale, bubbles: true, composed: true }));
  }

  private _emitError(message: string) {
    this.dispatchEvent(new CustomEvent('scale-error', { detail: message, bubbles: true, composed: true }));
  }

  private _emitClose() {
    this.dispatchEvent(new CustomEvent('scale-close', { bubbles: true, composed: true }));
  }

  private async _weighIn() {
    const vehicle = this._vehicleRef.trim();
    if (!vehicle || !this.registerId) return;
    this._busy = true;
    this._error = null;
    try {
      await posService.weighIn(this.registerId, vehicle);
      this._vehicleRef = '';
      await this._loadTickets();
    } catch (err: unknown) {
      this._emitError(apiErrorMessage(err, 'Failed to weigh in'));
    } finally {
      this._busy = false;
    }
  }

  private async _weighOut() {
    const sale = this.sale;
    if (!sale || sale.status !== 'open') {
      this._error = 'Open a sale with a product line first.';
      return;
    }
    const lineId = this._selectedLineOutId;
    if (!lineId) {
      this._error = 'Choose a cart line to weigh out.';
      return;
    }
    const ticketId = this._selectedTicketId;
    const vehicle = this._vehicleOut.trim();
    if (!ticketId && !vehicle) {
      this._error = 'Choose an open weigh-in or type a vehicle.';
      return;
    }
    this._busy = true;
    this._error = null;
    try {
      const input: { weigh_in_ticket_id?: string; vehicle_ref?: string; revision: number } = { revision: sale.revision };
      if (ticketId) input.weigh_in_ticket_id = ticketId;
      else input.vehicle_ref = vehicle;
      const result = await posService.weighOut(sale.id, lineId, input);
      this._emitSale(result.sale);
      this._selectedLineOutId = '';
      this._selectedTicketId = '';
      this._vehicleOut = '';
      await this._loadTickets();
    } catch (err: unknown) {
      this._error = apiErrorMessage(err, 'Failed to weigh out');
      this._emitError(this._error);
    } finally {
      this._busy = false;
    }
  }

  private async _voidTicket(ticket: ScaleTicket) {
    const reason = await promptDialog({
      title: `Void ${ticket.ticket_number}?`,
      label: 'Reason',
      required: true,
      confirmLabel: 'Void ticket',
      tone: 'danger',
    });
    if (!reason) return;
    this._busy = true;
    this._error = null;
    try {
      await posService.voidScaleTicket(ticket.id, reason, ticket.revision);
      await this._loadTickets();
    } catch (err: unknown) {
      this._error = apiErrorMessage(err, 'Failed to void the ticket');
      this._emitError(this._error);
    } finally {
      this._busy = false;
    }
  }

  private async _submitManual() {
    const sale = this.sale;
    if (!sale || sale.status !== 'open') {
      this._error = 'Open a sale with a product line first.';
      return;
    }
    const lineId = this._selectedLineManualId;
    if (!lineId) {
      this._error = 'Choose a cart line for the manual weight.';
      return;
    }
    const configured = this._status?.configured ?? false;
    const reading = this._status?.reading ?? null;
    const built = buildManualWeightPayload(
      this._manualWeight,
      this._manualTare,
      this._selectedReason(),
      this._manualNote,
      sale.revision,
      reading,
      configured,
    );
    if (!built.ok) {
      this._error = built.error;
      this._emitError(this._error);
      return;
    }
    this._busy = true;
    this._error = null;
    try {
      const updated = await posService.setManualWeight(sale.id, lineId, built.payload);
      this._emitSale(updated);
      this._manualWeight = '';
      this._manualTare = '';
      this._manualReasonCode = '';
      this._manualNote = '';
    } catch (err: unknown) {
      this._error = apiErrorMessage(err, 'Failed to set the manual weight');
      this._emitError(this._error);
    } finally {
      this._busy = false;
    }
  }

  private _inputStyle() {
    return `width:100%;box-sizing:border-box;padding:10px;background:${field};border:1px solid ${edge};border-radius:8px;color:${text};font-size:14px;outline:none`;
  }

  private _section(title: string, body: unknown) {
    return html`
      <div style="margin-top:18px;padding-top:16px;border-top:1px solid ${edge}">
        <h3 style="margin:0 0 10px;font-size:14px;font-weight:600;color:${soft}">${title}</h3>
        ${body}
      </div>`;
  }

  render() {
    const configured = this._status?.configured ?? false;
    const reading = this._status?.reading ?? null;
    const detail = this._status?.detail;
    const deviceName = this._status?.device?.name ?? 'Scale';
    const needsNote = this._selectedReason()?.needs_note ?? false;
    const lineOptions = this._lineOptions();
    const canWeighOut = this.sale?.status === 'open' && lineOptions.length > 1;
    const canManual = this.sale?.status === 'open' && lineOptions.length > 1;
    return html`
      <div data-testid="scale-panel" style="color:${text};font-family:'Outfit',-apple-system,sans-serif">
        <div style="display:flex;align-items:center;justify-content:space-between;gap:12px">
          <h2 style="margin:0;font-size:18px;font-weight:700">Scale</h2>
          <span data-testid="scale-device" style="font-size:12px;color:${muted}">
            ${configured ? deviceName : 'No scale configured'}
          </span>
        </div>

        <div data-testid="scale-reading" style="margin-top:14px;padding:14px;background:${field};border:1px solid ${edge};border-radius:10px;text-align:center;font-family:'JetBrains Mono',monospace">
          <div style="font-size:32px;font-weight:800;color:${text}">${scaleReadingText(reading, configured, detail)}</div>
          ${configured && reading && reading.weight_lb !== null && !reading.motion ? html`
            <div style="margin-top:4px;font-size:12px;color:${muted}">
              ${reading.mode === 'gross' ? 'Gross' : reading.mode === 'tare' ? 'Tare' : 'Net'}
              ${reading.at_zero ? ' at zero' : ' not at zero'}
            </div>` : nothing}
        </div>

        ${this._error ? html`<p role="alert" data-testid="scale-error" style="margin:10px 0 0;font-size:13px;color:${bad}">${this._error}</p>` : nothing}

        ${this._section('Weigh in', html`
          <label style="display:block;font-size:12px;color:${muted};margin-bottom:6px" for="scale-vehicle-in">Vehicle</label>
          <input id="scale-vehicle-in" data-testid="scale-vehicle-in" type="text" autocomplete="off" .value=${this._vehicleRef}
            @input=${(e: Event) => { this._vehicleRef = (e.target as HTMLInputElement).value; }}
            style=${this._inputStyle()} placeholder="Truck or trailer number" />
          <button data-testid="scale-weigh-in" @click=${() => void this._weighIn()} ?disabled=${this._busy || !this._vehicleRef.trim() || !configured}
            style="width:100%;margin-top:10px;padding:10px;background:${go};border:none;border-radius:8px;color:${plate};font-size:14px;font-weight:700;cursor:pointer;opacity:${this._busy || !this._vehicleRef.trim() || !configured ? '0.5' : '1'}">
            ${this._busy ? 'Weighing...' : 'Weigh in'}
          </button>
          ${!configured ? html`<p style="margin:6px 0 0;font-size:12px;color:${muted}">A manual weight can still be entered below.</p>` : nothing}
        `)}

        ${this._section('Open weigh-ins', html`
          ${this._tickets.length === 0 ? html`<p data-testid="scale-no-tickets" style="margin:0;font-size:13px;color:${muted}">No open weigh-ins</p>` : html`
            <div style="display:flex;flex-direction:column;gap:8px">
              ${this._tickets.map((t) => html`
                <div data-testid="scale-ticket" style="display:flex;align-items:center;justify-content:space-between;gap:8px;padding:8px 10px;background:${field};border:1px solid ${edge};border-radius:8px">
                  <div style="min-width:0">
                    <div style="font-size:13px;font-weight:600;color:${text}">${t.vehicle_ref}</div>
                    <div style="font-size:12px;color:${muted};font-family:'JetBrains Mono',monospace">
                      ${formatLb(t.gross_lb)} lb &middot; ${t.ticket_number}
                    </div>
                  </div>
                  <button data-testid="scale-void-ticket" @click=${() => void this._voidTicket(t)} ?disabled=${this._busy}
                    style="padding:6px 10px;background:transparent;border:1px solid ${edge};border-radius:6px;color:${bad};font-size:12px;cursor:pointer">
                    Void
                  </button>
                </div>`)}
            </div>`}
        `)}

        ${this._section('Weigh out', html`
          <label style="display:block;font-size:12px;color:${muted};margin-bottom:6px" for="scale-line-out">Cart line</label>
          <gable-select id="scale-line-out" data-testid="scale-line-out" .value=${this._selectedLineOutId}
            .options=${lineOptions}
            @change=${(e: Event) => { this._selectedLineOutId = (e.target as HTMLSelectElement).value; }}
          ></gable-select>
          <label style="display:block;margin-top:10px;font-size:12px;color:${muted};margin-bottom:6px" for="scale-ticket-out">Open weigh-in</label>
          <gable-select id="scale-ticket-out" data-testid="scale-ticket-out" .value=${this._selectedTicketId}
            .options=${this._ticketOptions()}
            @change=${(e: Event) => { this._selectedTicketId = (e.target as HTMLSelectElement).value; }}
          ></gable-select>
          <label style="display:block;margin-top:10px;font-size:12px;color:${muted};margin-bottom:6px" for="scale-vehicle-out">Or vehicle</label>
          <input id="scale-vehicle-out" data-testid="scale-vehicle-out" type="text" autocomplete="off" .value=${this._vehicleOut}
            @input=${(e: Event) => { this._vehicleOut = (e.target as HTMLInputElement).value; }}
            style=${this._inputStyle()} placeholder="Truck or trailer number" />
          <button data-testid="scale-weigh-out" @click=${() => void this._weighOut()} ?disabled=${this._busy || !canWeighOut}
            style="width:100%;margin-top:10px;padding:10px;background:${go};border:none;border-radius:8px;color:${plate};font-size:14px;font-weight:700;cursor:pointer;opacity:${this._busy || !canWeighOut ? '0.5' : '1'}">
            ${this._busy ? 'Weighing...' : 'Weigh out'}
          </button>
          ${!canWeighOut ? html`<p style="margin:6px 0 0;font-size:12px;color:${muted}">Add a product line to weigh out.</p>` : nothing}
        `)}

        ${this._section('Manual weight', html`
          <label style="display:block;font-size:12px;color:${muted};margin-bottom:6px" for="scale-line-manual">Cart line</label>
          <gable-select id="scale-line-manual" data-testid="scale-line-manual" .value=${this._selectedLineManualId}
            .options=${lineOptions}
            @change=${(e: Event) => { this._selectedLineManualId = (e.target as HTMLSelectElement).value; }}
          ></gable-select>
          <div style="display:grid;grid-template-columns:1fr 1fr;gap:10px;margin-top:10px">
            <div>
              <label style="display:block;font-size:12px;color:${muted};margin-bottom:6px" for="scale-weight-manual">Weight (lb)</label>
              <input id="scale-weight-manual" data-testid="scale-weight-manual" type="text" inputmode="decimal" autocomplete="off" .value=${this._manualWeight}
                @input=${(e: Event) => { this._manualWeight = (e.target as HTMLInputElement).value; }}
                style=${this._inputStyle()} placeholder="0.00" />
            </div>
            <div>
              <label style="display:block;font-size:12px;color:${muted};margin-bottom:6px" for="scale-tare-manual">Tare (lb)</label>
              <input id="scale-tare-manual" data-testid="scale-tare-manual" type="text" inputmode="decimal" autocomplete="off" .value=${this._manualTare}
                @input=${(e: Event) => { this._manualTare = (e.target as HTMLInputElement).value; }}
                style=${this._inputStyle()} placeholder="0.00" />
            </div>
          </div>
          <label style="display:block;margin-top:10px;font-size:12px;color:${muted};margin-bottom:6px" for="scale-reason">Reason</label>
          <gable-select id="scale-reason" data-testid="scale-reason" .value=${this._manualReasonCode}
            .options=${this._reasonOptions()}
            @change=${(e: Event) => { this._manualReasonCode = (e.target as HTMLSelectElement).value; }}
          ></gable-select>
          ${needsNote ? html`
            <label style="display:block;margin-top:10px;font-size:12px;color:${muted};margin-bottom:6px" for="scale-note">Note</label>
            <input id="scale-note" data-testid="scale-note" type="text" autocomplete="off" .value=${this._manualNote}
              @input=${(e: Event) => { this._manualNote = (e.target as HTMLInputElement).value; }}
              style=${this._inputStyle()} placeholder="Why is this weight manual?" />
          ` : nothing}
          <button data-testid="scale-manual-submit" @click=${() => void this._submitManual()} ?disabled=${this._busy || !canManual}
            style="width:100%;margin-top:10px;padding:10px;background:${go};border:none;border-radius:8px;color:${plate};font-size:14px;font-weight:700;cursor:pointer;opacity:${this._busy || !canManual ? '0.5' : '1'}">
            ${this._busy ? 'Saving...' : 'Set manual weight'}
          </button>
          ${!canManual ? html`<p style="margin:6px 0 0;font-size:12px;color:${muted}">Add a product line to set a manual weight.</p>` : nothing}
        `)}

        <div style="display:flex;justify-content:flex-end;margin-top:20px">
          <button data-testid="scale-close" @click=${() => this._emitClose()}
            style="padding:10px 16px;background:transparent;border:1px solid ${edge};border-radius:8px;color:${muted};font-size:14px;cursor:pointer">
            Close
          </button>
        </div>
      </div>
    `;
  }
}

declare global {
  interface HTMLElementTagNameMap {
    'gable-pos-scale-panel': ScalePanel;
  }
}
