// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026, FutureBuild, Inc. and OpenLBM contributors

// Package largecash is the large cash capture, classification and
// aggregation of CP-17a: the branch's country picks the rule family. A
// United States branch runs the Form 8300 duty (payer capture at the
// counter, the cash classifier, the related transaction and 12 month
// aggregations, central across branches). A Canadian branch runs an
// optional dealer threshold, off by default, that captures the payer and
// flags the sale for the dealer's own records and never produces a FINTRAC
// report: a building materials dealer is not a FINTRAC reporting entity.
package largecash

// The rule families. The two never share one counter: the United States
// comparator is strictly greater than the threshold over a 12 month running
// total, the Canadian one is at or above over a static 24 hour window, and
// a single shared counter would be wrong in one of the two jurisdictions.
const (
	RuleUS = "US_8300"
	RuleCA = "CA_DEALER"
)

// ThresholdCents is 10,000 in the sale's own currency, in cents: the
// comparison happens in the branch's currency, never converted. It is the
// United States threshold (more than 10,000) and the Canadian default (at
// or above 10,000).
const ThresholdCents int64 = 1_000_000

// The instrument spellings a check tender may name. A personal check and an
// unlabelled check never count; the four negotiable instruments count only
// under the United States designated reporting transaction and avoidance
// rules, and only at or below the face cap.
const (
	InstrumentPersonalCheck  = "personal_check"
	InstrumentCashiersCheck  = "cashiers_check"
	InstrumentBankDraft      = "bank_draft"
	InstrumentTravelersCheck = "travelers_check"
	InstrumentMoneyOrder     = "money_order"
)

// instruments counts as cash under the Form 8300 instrument rules when the
// conditions hold; a personal check is not among them.
var instruments = map[string]bool{
	InstrumentCashiersCheck: true, InstrumentBankDraft: true,
	InstrumentTravelersCheck: true, InstrumentMoneyOrder: true,
}

// Instruments lists the negotiable instrument spellings, for the wire's
// validation and the counter's picker.
func Instruments() []string {
	return []string{InstrumentPersonalCheck, InstrumentCashiersCheck, InstrumentBankDraft,
		InstrumentTravelersCheck, InstrumentMoneyOrder}
}

// The recorded reasons. A tender line with a recorded classification and a
// recorded reason is defensible; a tender line with a silent rule is not,
// and the reasons for the tenders that did not count are the ones a dealer
// is most likely to be second guessed on.
const (
	ReasonCurrency           = "currency"
	ReasonConsumerDurable    = "designated reporting transaction of a consumer durable"
	ReasonSuspectedAvoidance = "the manager marked the instrument as suspected avoidance of reporting"
	ReasonPersonalCheck      = "a personal check is never cash"
	ReasonOverFaceCap        = "a negotiable instrument over the threshold face value is not cash"
	ReasonNotDesignated      = "not a designated reporting transaction, so the instrument is not cash"
	ReasonNotCash            = "the tender method is not cash"
	ReasonCanadaCurrencyOnly = "Canada counts currency only"
)

// SaleFacts is what the classifier needs about the sale beyond the tender
// itself: the branch's country, whether a sale line is a product flagged
// consumer durable priced over the threshold, and the manager's suspected
// avoidance mark.
type SaleFacts struct {
	Country                      string
	ConsumerDurableOverThreshold bool
	SuspectedAvoidance           bool
}

// TenderFacts is one tender as the classifier sees it.
type TenderFacts struct {
	// Method is the counter's tender method: cash, check, card, account or
	// store_credit.
	Method string
	// Instrument names the kind of check when the tender is one: a personal
	// check, a cashier's check, a bank draft, a traveler's check or a money
	// order. Empty means an ordinary check.
	Instrument  string
	AmountCents int64
}

// Decision is the classifier's verdict on one tender, recorded with the
// reason so a later dispute or audit can show why a tender counted or did
// not.
type Decision struct {
	RuleFamily   string
	Counts       bool
	CountedCents int64
	Instrument   string
	Reason       string
}

// Classify is the pure classifier (the house rule of severityScore: the
// order a clerk, a manager and a regulator see can be argued with without a
// database). Currency always counts in both rule families. For the United
// States a negotiable instrument of the threshold face value or less counts
// only for a consumer durable sale line priced over the threshold or under
// the suspected avoidance mark; a personal check never counts and an
// instrument over the face cap never counts. For Canada only currency
// counts. A country with no rule family counts nothing.
func Classify(sale SaleFacts, t TenderFacts) Decision {
	switch sale.Country {
	case "US":
		return classifyUS(sale, t)
	case "CA":
		if t.Method == "cash" {
			return Decision{RuleFamily: RuleCA, Counts: true, CountedCents: t.AmountCents,
				Instrument: "currency", Reason: ReasonCanadaCurrencyOnly}
		}
		return Decision{RuleFamily: RuleCA, Reason: ReasonCanadaCurrencyOnly}
	default:
		return Decision{}
	}
}

func classifyUS(sale SaleFacts, t TenderFacts) Decision {
	if t.Method == "cash" {
		return Decision{RuleFamily: RuleUS, Counts: true, CountedCents: t.AmountCents,
			Instrument: "currency", Reason: ReasonCurrency}
	}
	if t.Method != "check" {
		return Decision{RuleFamily: RuleUS, Reason: ReasonNotCash}
	}
	if t.Instrument == "" || t.Instrument == InstrumentPersonalCheck {
		return Decision{RuleFamily: RuleUS, Instrument: InstrumentPersonalCheck, Reason: ReasonPersonalCheck}
	}
	if !instruments[t.Instrument] {
		return Decision{RuleFamily: RuleUS, Instrument: t.Instrument, Reason: ReasonPersonalCheck}
	}
	if t.AmountCents > ThresholdCents {
		return Decision{RuleFamily: RuleUS, Instrument: t.Instrument, Reason: ReasonOverFaceCap}
	}
	if sale.SuspectedAvoidance {
		return Decision{RuleFamily: RuleUS, Counts: true, CountedCents: t.AmountCents,
			Instrument: t.Instrument, Reason: ReasonSuspectedAvoidance}
	}
	if sale.ConsumerDurableOverThreshold {
		return Decision{RuleFamily: RuleUS, Counts: true, CountedCents: t.AmountCents,
			Instrument: t.Instrument, Reason: ReasonConsumerDurable}
	}
	return Decision{RuleFamily: RuleUS, Instrument: t.Instrument, Reason: ReasonNotDesignated}
}

// Crosses is the threshold test of a rule family: the United States
// comparator is strictly greater than, the Canadian one at or above.
func Crosses(ruleFamily string, totalCents, thresholdCents int64) bool {
	if thresholdCents <= 0 {
		thresholdCents = ThresholdCents
	}
	switch ruleFamily {
	case RuleUS:
		return totalCents > thresholdCents
	case RuleCA:
		return totalCents >= thresholdCents
	}
	return false
}
