---
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
description: Answer which OpenLBM license governs a file or directory, and what the per-component model means.
argument-hint: [a file path, directory, or licensing question]
---

Use the **licensing-check** skill.

Question: $ARGUMENTS

If it is a path, give the exact SPDX identifier and the header to paste, checking the file header, REUSE.toml, and LICENSE-MAP.md in that order. Remember: most specific path wins — `backend/pkg/apps/` is Connector, not Commons. If it is a "may we use this" question, explain the model in plain language and then point at the canonical Standard and counsel.
