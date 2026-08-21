// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package governance

import "github.com/gablelbm/gable/pkg/apps"

// App is the governance app manifest: RFC drafting and status tracking for
// the federated contribution model.
//
// Summary is customer-facing — it is what the Apps admin page shows before an
// operator installs this. Keep it to what the code actually does. The only
// AIProvider implementation shipped here is TemplateAIProvider (ai.go), which
// substitutes the submitted title/problem/solution into a fixed Markdown
// skeleton. No model is called, so the summary must not promise one.
var App = apps.Manifest{
	Key:      "governance",
	Name:     "Governance (RFCs)",
	Summary:  "RFC drafting from a structured template, plus review and status tracking for platform governance.",
	Category: "Platform",
	Core:     false,
}
