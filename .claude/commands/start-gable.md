---
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
description: Start the whole Gable stack locally with demo data (the run-gable-locally skill).
argument-hint: [optional: port overrides, e.g. GABLE_WEB_PORT=8180]
---

Use the **run-gable-locally** skill and follow it end to end: check the prerequisites, start with `make up-mail`, prove the seed through the API, then tell the user the URL, how dev sign-in works, the persona table (clerk, manager, finance, owner), Mailpit's URL and how to stop. Dev mode is local only; say so plainly.

Port overrides the user gives: $ARGUMENTS
