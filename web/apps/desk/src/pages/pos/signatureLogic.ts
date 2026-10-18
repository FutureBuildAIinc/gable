// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter's signature pad, as pure functions: the strokes a person draws
// (kept as points, whatever the canvas does), whether anything was drawn, and
// the words that say why a signature cannot be sealed yet. The pad element
// draws the same strokes on its canvas and sends the canvas's PNG; the server
// seals the typed name, the image, the time and the sale into one record that
// never changes.

export interface Point {
    x: number;
    y: number;
}

export type Stroke = Point[];

/** A pad is blank until a stroke has at least two points: a tap is no signature. */
export function isBlank(strokes: Stroke[]): boolean {
    return !strokes.some((s) => s.length >= 2);
}

/** Why the signature cannot be sealed yet, in words, or null when it can. */
export function signatureRefusal(strokes: Stroke[], signerName: string): string | null {
    if (signerName.trim() === '') return 'Type the signer’s name';
    if (isBlank(strokes)) return 'Draw the signature in the box';
    return null;
}

/** The payload of a canvas data URL (the base64 after the comma), or null when it is no PNG. */
export function pngPayload(dataUrl: string): string | null {
    const prefix = 'data:image/png;base64,';
    if (!dataUrl.startsWith(prefix)) return null;
    const payload = dataUrl.slice(prefix.length);
    return payload.length > 0 ? payload : null;
}

/** The words that name a sealed signature on the sale's card. */
export function sealedWords(signer: string, seal: string): string {
    return `Signed by ${signer}, sealed ${seal.slice(0, 12)}`;
}
