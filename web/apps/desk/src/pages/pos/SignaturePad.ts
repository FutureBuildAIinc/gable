// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { LitElement, html } from 'lit';
import { customElement, property } from 'lit/decorators.js';
import { isBlank, pngPayload, type Point, type Stroke } from './signatureLogic';

/**
 * gable-signature-pad: a box a person signs in with a finger, a stylus or the
 * mouse. The strokes are kept as points (the same points draw the canvas), so
 * whether anything was drawn never depends on the canvas. `blank` answers
 * that, `png()` the canvas as the base64 payload of a PNG (null when blank or
 * when the browser gives no PNG), and `clear()` empties the pad. It fires
 * `signature-change` when a stroke ends or the pad is cleared.
 */
@customElement('gable-signature-pad')
export class SignaturePad extends LitElement {
    createRenderRoot() { return this; }

    @property({ type: Number }) width = 360;
    @property({ type: Number }) height = 140;

    private _strokes: Stroke[] = [];
    private _drawing: Stroke | null = null;

    get blank(): boolean {
        return isBlank(this._strokes);
    }

    get strokes(): Stroke[] {
        return this._strokes;
    }

    private get _canvas(): HTMLCanvasElement | null {
        return this.querySelector('canvas');
    }

    private _at(e: PointerEvent): Point {
        const box = this._canvas?.getBoundingClientRect();
        const scaleX = box && box.width > 0 ? this.width / box.width : 1;
        const scaleY = box && box.height > 0 ? this.height / box.height : 1;
        return { x: ((e.clientX - (box?.left ?? 0)) * scaleX), y: ((e.clientY - (box?.top ?? 0)) * scaleY) };
    }

    private _paint() {
        const ctx = this._canvas?.getContext?.('2d') ?? null;
        if (!ctx) return;
        ctx.clearRect(0, 0, this.width, this.height);
        ctx.fillStyle = '#ffffff';
        ctx.fillRect(0, 0, this.width, this.height);
        ctx.strokeStyle = '#111111';
        ctx.lineWidth = 2;
        ctx.lineCap = 'round';
        ctx.lineJoin = 'round';
        for (const s of this._strokes) {
            if (s.length === 0) continue;
            ctx.beginPath();
            ctx.moveTo(s[0].x, s[0].y);
            for (const p of s.slice(1)) ctx.lineTo(p.x, p.y);
            ctx.stroke();
        }
    }

    private _down = (e: PointerEvent) => {
        e.preventDefault();
        this._canvas?.setPointerCapture?.(e.pointerId);
        this._drawing = [this._at(e)];
        this._strokes = [...this._strokes, this._drawing];
        this._paint();
    };

    private _move = (e: PointerEvent) => {
        if (!this._drawing) return;
        e.preventDefault();
        this._drawing.push(this._at(e));
        this._paint();
    };

    private _up = (e: PointerEvent) => {
        if (!this._drawing) return;
        e.preventDefault();
        this._drawing = null;
        this.dispatchEvent(new CustomEvent('signature-change', { bubbles: true }));
    };

    /** Empties the pad. */
    clear() {
        this._strokes = [];
        this._drawing = null;
        this._paint();
        this.dispatchEvent(new CustomEvent('signature-change', { bubbles: true }));
    }

    /** The canvas as the base64 payload of a PNG; null while blank or when no PNG came out. */
    png(): string | null {
        if (this.blank) return null;
        const url = this._canvas?.toDataURL?.('image/png') ?? '';
        return pngPayload(url);
    }

    firstUpdated() {
        this._paint();
    }

    render() {
        return html`
            <canvas width=${this.width} height=${this.height} data-testid="signature-canvas"
                aria-label="Signature box: draw the signature here"
                style="touch-action:none;width:100%;max-width:${this.width}px;height:auto;background:#ffffff;border-radius:6px"
                class="block border border-white/20"
                @pointerdown=${this._down} @pointermove=${this._move} @pointerup=${this._up}
                @pointercancel=${this._up}></canvas>`;
    }
}
