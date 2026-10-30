// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"fmt"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// CheckTillCheckDeposit holds a check taken at the counter as a deposit to
// the rules a sale's check tender meets: the customer's check limit and the
// dealer's ID rule. The payment route calls it inside the payment's
// transaction (serve wires it as the payment service's TillCheckGuard). The
// deposit is one check of amountCents. A deposit has no sale to hang a manager
// override on, so a check over the limit is refused 409 check_over_limit and
// the clerk takes cash or a smaller check.
func (s *Service) CheckTillCheckDeposit(ctx context.Context, customerID uuid.UUID, amountCents int64, idChecked bool) error {
	facts, err := s.repo.CustomerFacts(ctx, customerID)
	if err != nil {
		return err
	}
	limit, err := s.checkLimitOf(ctx, facts)
	if err != nil {
		return err
	}
	if limit != nil && amountCents > *limit {
		return conflict("check_over_limit", fmt.Sprintf(
			"a check of %s is over this customer's check limit of %s: take cash or a smaller check",
			moneyWords(facts.Currency, amountCents), moneyWords(facts.Currency, *limit)))
	}
	return s.checkTenderIDRule(ctx, customerID, []TenderIn{{
		Method: TenderCheck, AmountCents: httpx.Cents(amountCents), IDChecked: idChecked}})
}
