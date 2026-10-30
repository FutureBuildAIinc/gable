// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package salesdoc

import (
	"fmt"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// OfflineTender is one tender leg of an offline sale (ADR 0011 section 2.2):
// cash, check or account. Card and store credit are never taken offline.
type OfflineTender struct {
	Method      string      `json:"method"`
	AmountCents httpx.Cents `json:"amount_cents"`
}

// OfflineTenderSettlement is the outcome of summing an offline sale's tenders
// against its total (ADR 0011 sections 2.2 and 7): the tendered total, the
// cash and account portions, the change given from cash, and the refusal code
// when the legs are invalid.
type OfflineTenderSettlement struct {
	TenderedCents httpx.Cents
	CashCents     httpx.Cents
	AccountCents  httpx.Cents
	ChangeCents   httpx.Cents
	// RoundingCents is the drawer's difference from the exact cash leg under
	// the branch's cash rounding rule (CP-18): positive on a round up.
	RoundingCents httpx.Cents
	RefusalCode   string
}

// CashRounding rounds a cash leg in cents under a branch's rule and returns
// the rounded amount and its difference from the input: the live
// pos.RoundCashFor bound to the branch's mode. Nil takes the cash exactly.
type CashRounding func(cents int64) (rounded, diff int64)

// SettleOfflineTenders sums an offline sale's tenders and computes the change
// given from cash (ADR 0005 section 14.2 C2-5 and ADR 0011 section 2.2):
// at most one account leg, only cash/check/account allowed offline, tendered
// at or above the sale total, and any change covered by the cash tendered.
// Under a cash rounding rule the cash leg rounds as the live counter rounds
// it (pos.Service completion, CP-18): the change falls out of the rounded
// leg, and where no single cash payment can stand the difference the cash is
// taken exactly.
func SettleOfflineTenders(totalCents httpx.Cents, tenders []OfflineTender, roundCash CashRounding) (OfflineTenderSettlement, error) {
	var out OfflineTenderSettlement
	var accountLegs int
	for i := range tenders {
		t := &tenders[i]
		if t.AmountCents <= 0 {
			out.RefusalCode = "invalid_tender_amount"
			return out, fmt.Errorf("tender %d carries a non positive amount (%d)", i, t.AmountCents)
		}
		out.TenderedCents += t.AmountCents
		switch strings.ToLower(strings.TrimSpace(t.Method)) {
		case "cash":
			out.CashCents += t.AmountCents
		case "check":
		case "account":
			out.AccountCents += t.AmountCents
			accountLegs++
		default:
			out.RefusalCode = "tender_not_offline"
			return out, fmt.Errorf("tender method %q is not available offline", t.Method)
		}
	}
	if accountLegs > 1 {
		out.RefusalCode = "one_account_leg"
		return out, fmt.Errorf("a sale takes at most one account tender: put the whole account amount on one")
	}
	change := out.TenderedCents - totalCents
	if cashLegExact := int64(out.CashCents - change); roundCash != nil && cashLegExact > 0 {
		rounded, diff := roundCash(cashLegExact)
		if diff != 0 && cashLegCarries(tenders, int64(out.CashCents)-rounded, diff) {
			change = out.CashCents - httpx.Cents(rounded)
			out.RoundingCents = httpx.Cents(diff)
			if change < 0 {
				out.RefusalCode = "insufficient_tender"
				return out, fmt.Errorf("the cash due is %d cents once the branch rounds it to the nearest five cents, and the tenders carry %d", rounded, out.CashCents)
			}
		}
	}
	if change < 0 {
		out.RefusalCode = "insufficient_tender"
		return out, fmt.Errorf("the sale needs %d cents and the tenders carry %d", totalCents, out.TenderedCents)
	}
	if change > out.CashCents {
		out.RefusalCode = "change_from_cash"
		return out, fmt.Errorf("only a cash tender can give change: the over tender exceeds the cash taken")
	}
	out.ChangeCents = change
	return out, nil
}

// cashLegCarries reports whether one cash payment can stand a rounding
// difference: the change comes out of the cash tenders in the order they were
// taken, and the largest payment left must hold the difference on its own.
func cashLegCarries(tenders []OfflineTender, change, diff int64) bool {
	var largest int64
	left := change
	for i := range tenders {
		if strings.ToLower(strings.TrimSpace(tenders[i].Method)) != "cash" {
			continue
		}
		amount := int64(tenders[i].AmountCents)
		if left > 0 {
			give := min(amount, left)
			amount -= give
			left -= give
		}
		largest = max(largest, amount)
	}
	return largest > 0 && largest >= diff
}
