// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

/**
 * Global test setup. Keeps the jsdom document, storage and history clean
 * between tests so suites that mount custom elements or touch the router
 * singleton cannot leak state into each other.
 */
import { afterEach } from 'vitest'

// jsdom implements no canvas and logs "not implemented" each time one is asked
// for a context: answer none, quietly, for a page that draws on a canvas.
HTMLCanvasElement.prototype.getContext = (() => null) as unknown as HTMLCanvasElement['getContext']

afterEach(() => {
  document.body.innerHTML = ''
  localStorage.clear()
  sessionStorage.clear()
})
