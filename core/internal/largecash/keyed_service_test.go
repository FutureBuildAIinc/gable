// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

package largecash_test

import (
	"testing"

	"github.com/gablelbm/gable/internal/largecash"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/pkg/database"
)

// testVaultKey seals the payer's sensitive values in these tests: an obvious
// pattern, never a real key shape.
const testVaultKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// keyedService is the large cash service with the test vault key, as a
// configured stack runs it.
func keyedService(t *testing.T, db *database.DB) *largecash.Service {
	t.Helper()
	v, err := payment.NewVault(testVaultKey)
	if err != nil {
		t.Fatal(err)
	}
	return largecash.NewService(db, v, testVaultKey)
}
