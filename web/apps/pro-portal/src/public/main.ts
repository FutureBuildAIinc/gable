// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import '../index.css'
import { initTheme } from '@gable/design-system'

initTheme(null)

const root = document.getElementById('root')
if (root) root.innerHTML = '<main class="pro-main"><p class="pro-page-kicker">Customer quote</p></main>'
