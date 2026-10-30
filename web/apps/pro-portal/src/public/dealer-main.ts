// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import '../index.css'
import { initCustomerTheme } from './main.ts'
import './dealer-page.ts'

initCustomerTheme()

const root = document.getElementById('root')
if (root) root.innerHTML = '<gable-dealer-quote-page></gable-dealer-quote-page>'
