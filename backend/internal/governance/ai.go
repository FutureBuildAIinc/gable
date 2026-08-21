// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package governance

import (
	"context"
	"fmt"
)

// AIProvider is the seam where a real model would generate RFC prose. Nothing
// in this repository implements it with a model; the only implementation is
// TemplateAIProvider below.
type AIProvider interface {
	GenerateRFC(ctx context.Context, title, problem, solution string) (string, error)
}

// TemplateAIProvider is a stub, not a model client. It performs no network
// call, reads no API key, and cannot fail — GenerateRFC substitutes the
// caller's three inputs into a fixed Markdown skeleton and returns it. It
// exists so the RFC workflow is exercisable end to end without a provider;
// swap in a real AIProvider to get generated prose.
type TemplateAIProvider struct{}

func NewTemplateAIProvider() *TemplateAIProvider {
	return &TemplateAIProvider{}
}

// GenerateRFC fills the skeleton. ctx is unused: there is nothing to cancel.
func (p *TemplateAIProvider) GenerateRFC(ctx context.Context, title, problem, solution string) (string, error) {
	template := `# RFC: %s

## Status: Draft
## Author: *Fill in — this draft was expanded from a template, not written by a model*

### 1. Problem Statement
%s

### 2. Proposed Solution
%s

### 3. Technical Implementation
*To be filled by Engineering*

### 4. Drawbacks & Alternatives
*To be filled by Engineering*
`
	return fmt.Sprintf(template, title, problem, solution), nil
}
