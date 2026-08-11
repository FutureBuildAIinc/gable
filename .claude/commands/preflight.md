---
# SPDX-License-Identifier: LicenseRef-OpenLBM-Docs-1.0
# SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors
description: Run the real pre-flight gates before opening a PR (the exact commands CI runs) plus SPDX, secrets, and PR-target checks.
argument-hint: [optional: a path or area to focus on]
---

Use the **check-my-contribution** skill.

Run the pre-flight gates for the current working tree. Focus: $ARGUMENTS

Actually execute every command — do not report a gate as passing unless you ran it. Finish with the verdict table from the skill, then the smallest fix for each failure.
