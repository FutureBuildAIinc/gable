// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pim

import (
	"strings"
	"testing"
)

// FROM-PRINCIPAL 118 a: a drawn product carries no words, labels, brand names
// or logos. The house style asks for none, and no longer allows an invented
// brand word on a box, a bag or a can.
func TestIllustrationPromptAsksForNoWordsOrBrands(t *testing.T) {
	p := strings.ToLower(illustrationSystemPrompt)
	if !strings.Contains(p, "no words, labels, brand names or logos") {
		t.Error("the illustration prompt must ask for no words, labels, brand names or logos on the product")
	}
	if strings.Contains(p, "invented brand") {
		t.Error("the illustration prompt still allows an invented brand word")
	}
	if strings.Contains(p, "tspan") || strings.Contains(p, "text and") {
		t.Error("the illustration prompt still lists the text elements as allowed")
	}
}
