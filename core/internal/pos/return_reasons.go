// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"net/http"
	"sort"
	"strings"

	"github.com/gablelbm/gable/internal/platform/httpx"
)

// ReasonOther is the return reason that needs a note: the free text reason of
// the return.
const ReasonOther = "other"

// returnReason is one entry of the closed list of return reasons.
type returnReason struct {
	Code  string
	Label string
}

// returnReasons is the closed lowercase list of reasons a returned line may
// carry. Nothing else is accepted; other asks for the note. A reason's
// restocking fee is a database wide setting (pos.restocking_fee_bps.<code>,
// basis points, none set meaning no fee), so a dealer exempts defective and
// wrong_item goods by leaving them at zero.
var returnReasons = []returnReason{
	{"defective", "Defective"},
	{"damaged", "Damaged"},
	{"wrong_item", "Wrong item given"},
	{"ordered_wrong", "Ordered the wrong item"},
	{"changed_mind", "Changed their mind"},
	{"overbought", "Bought too much"},
	{ReasonOther, "Other (needs a note)"},
}

// settingRestockingFeePrefix prefixes the per reason fee settings.
const settingRestockingFeePrefix = "pos.restocking_fee_bps."

// defaultRestockingFeeBps are the figures the settings screen offers a dealer
// who has set none: the reasons that are the customer's own doing carry the
// house figure, and the ones that are the dealer's (defective goods, the
// wrong item given, damaged goods) and the catch all carry nothing. They are
// suggestions on the screen, never a fee in force: nothing is withheld until
// a dealer writes the map.
var defaultRestockingFeeBps = map[string]int{
	"ordered_wrong": 1500,
	"changed_mind":  1500,
	"overbought":    1500,
}

func isReturnReason(code string) bool {
	for _, r := range returnReasons {
		if r.Code == code {
			return true
		}
	}
	return false
}

func returnReasonCodes() []string {
	out := make([]string, len(returnReasons))
	for i, r := range returnReasons {
		out[i] = r.Code
	}
	return out
}

func returnReasonLabel(code string) string {
	for _, r := range returnReasons {
		if r.Code == code {
			return r.Label
		}
	}
	return code
}

// ReturnReasonView is one reason as the counter lists it, with the fee its
// returns carry in basis points.
type ReturnReasonView struct {
	Code           string `json:"code"`
	Label          string `json:"label"`
	RestockingFeeB int    `json:"restocking_fee_bps"`
	NeedsNote      bool   `json:"needs_note"`
}

// halfEvenDiv divides a non negative numerator by a positive denominator,
// rounding half to even: the rounding a fee is taken at.
func halfEvenDiv(num, den int64) int64 {
	if den <= 0 || num <= 0 {
		return 0
	}
	q, r := num/den, num%den
	switch {
	case 2*r > den:
		q++
	case 2*r == den && q%2 == 1:
		q++
	}
	return q
}

// restockingFee is the fee a returned line's refund carries at the reason's
// basis points, half even to the cent.
func restockingFee(refundCents int64, bps int) int64 {
	if bps <= 0 {
		return 0
	}
	return halfEvenDiv(refundCents*int64(bps), 10000)
}

// ReturnReasons lists the closed list with each reason's fee, by the order
// of the list.
func (s *Service) ReturnReasons(ctx context.Context) ([]ReturnReasonView, error) {
	bps, err := s.repo.RestockingFeeBps(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ReturnReasonView, 0, len(returnReasons))
	for _, r := range returnReasons {
		out = append(out, ReturnReasonView{Code: r.Code, Label: r.Label, RestockingFeeB: bps[r.Code], NeedsNote: r.Code == ReasonOther})
	}
	return out, nil
}

// ListReturnReasons is GET /pos/return-reasons: the closed list of reasons
// with each reason's restocking fee in basis points, in the list envelope.
func (h *Handler) ListReturnReasons(w http.ResponseWriter, r *http.Request) {
	if _, err := httpx.StrictQuery(r); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	found, err := h.service.ReturnReasons(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.WriteList(w, found, "", 50)
}

// reasonNote is the note a return carries when it names none: the labels of
// the distinct reasons its lines carry, so the credit memo is never without
// words.
func reasonNote(lines []ReturnLineIn) string {
	seen := map[string]bool{}
	var codes []string
	for i := range lines {
		c := lines[i].ReasonCode
		if !seen[c] {
			seen[c] = true
			codes = append(codes, c)
		}
	}
	sort.Strings(codes)
	labels := make([]string, len(codes))
	for i, c := range codes {
		labels[i] = returnReasonLabel(c)
	}
	return strings.Join(labels, ", ")
}
