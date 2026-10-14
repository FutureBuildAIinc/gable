// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import { describe, it, expect, afterEach, vi } from 'vitest'
import { ParsingService } from './parsing.service'

afterEach(() => vi.unstubAllGlobals())

describe('ParsingService.uploadMaterialList', () => {
  it('shows the words of a 415, not the raw envelope', async () => {
    const words = 'This picture type (image/bmp) cannot be read. Claude reads JPEG, PNG, GIF or WebP pictures and PDF files, and plain text or CSV.'
    vi.stubGlobal('fetch', vi.fn(async () => new Response(
      JSON.stringify({ error: { code: 'unsupported_type', message: words }, meta: { request_id: 'r' } }),
      { status: 415, headers: { 'Content-Type': 'application/json' } },
    )))
    const err = await ParsingService.uploadMaterialList(new File(['x'], 'a.bmp', { type: 'image/bmp' })).catch((e: Error) => e)
    expect((err as Error).message).toBe(words)
  })
})
