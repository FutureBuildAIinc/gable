// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect } from 'vitest';
import { isBlank, signatureRefusal, pngPayload, sealedWords, type Stroke } from './signatureLogic';

const stroke = (n: number): Stroke => Array.from({ length: n }, (_, i) => ({ x: i, y: i * 2 }));

describe('the signature pad', () => {
    it('is blank until a stroke has at least two points', () => {
        expect(isBlank([])).toBe(true);
        expect(isBlank([stroke(1)])).toBe(true);
        expect(isBlank([stroke(2)])).toBe(false);
        expect(isBlank([stroke(1), stroke(3)])).toBe(false);
    });
    it('cannot be sealed without the signer’s name or a drawn signature', () => {
        expect(signatureRefusal([stroke(5)], '  ')).toBe('Type the signer’s name');
        expect(signatureRefusal([], 'Pat Signer')).toBe('Draw the signature in the box');
        expect(signatureRefusal([stroke(5)], 'Pat Signer')).toBeNull();
    });
    it('reads the payload of a PNG data URL and nothing else', () => {
        expect(pngPayload('data:image/png;base64,AAAA')).toBe('AAAA');
        expect(pngPayload('data:image/png;base64,')).toBeNull();
        expect(pngPayload('data:,')).toBeNull();
        expect(pngPayload('data:image/jpeg;base64,AAAA')).toBeNull();
    });
    it('names a sealed signature by the signer and the first twelve characters of the seal', () => {
        expect(sealedWords('Pat Signer', 'abcdef0123456789abcdef')).toBe('Signed by Pat Signer, sealed abcdef012345');
    });
});
