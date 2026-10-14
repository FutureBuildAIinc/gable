// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package seed

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/gablelbm/gable/internal/dealerprofile"
)

// The demo logo is a fixed byte literal: the printed forms embed it as it is,
// so their goldens must not move with the Go toolchain's PNG encoder.
func TestDemoLogoIsFixedAndEmbeddable(t *testing.T) {
	raw := demoLogo()
	sum := sha256.Sum256(raw)
	const want = "4628ef159e184fb2d120c1329876bbcd330dc081532a4e79241a4d421a6440d0"
	if got := hex.EncodeToString(sum[:]); got != want {
		t.Errorf("the demo logo bytes changed: sha256 %s, want %s", got, want)
	}
	if err := dealerprofile.CheckLogoImage(raw, "png"); err != nil {
		t.Errorf("the demo logo is not an acceptable logo: %v", err)
	}
}
