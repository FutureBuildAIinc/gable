// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

import './index.css'
import { authCustody } from '@gable/auth'
import { initTheme } from '@gable/design-system'
import './app-shell.ts'

initTheme(authCustody.subject)

const root = document.getElementById('root')
if (root) root.innerHTML = '<gable-pro-shell></gable-pro-shell>'
