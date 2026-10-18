// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import '../index.css'
import { initTheme } from '@gable/design-system'
import './public-page.ts'

initTheme(null)

const root = document.getElementById('root')
if (root) root.innerHTML = '<gable-customer-quote-page></gable-customer-quote-page>'
