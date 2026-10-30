// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The Run Payments card present driver (CP-05): a TerminalProvider behind
// the intent ledger's seam, built on Run's documented Terminal API contract
// (docs.runpayments.io, the Terminal API and Payments API reference) and
// proven in the tests against a scripted fake HTTP server of that contract.
// No test ever touches a live Run host; the driver is selected by
// configuration beside the simulator, never by default.
//
// The mapping, as the research (CP-05-RUN-CARD-PRESENT) documents it:
//
//   a sale leg:     POST /api/terminal/{device_id}/charge-card with the
//                   amount as a dollars string and our idempotency key as
//                   order_id (Run documents no idempotency key of its own).
//                   The 202 answers an interaction id, which becomes the
//                   intent's provider reference. A poller goroutine reads
//                   GET .../charge-card/{interaction_id} until the
//                   interaction stands succeeded, failed or cancelled, and
//                   lands the verdict through the answer sink:
//                   result A authorized when the card was charged exactly
//                   the leg's amount with no fee, C declined (a decline is a
//                   succeeded interaction), B declined too (a transient
//                   processor retry: no money moved, the cashier starts a
//                   new leg), a failed or cancelled interaction declined
//                   only when its error is at the card read. Any other end
//                   with no verdict asks the transaction lookup, and without
//                   one lands nothing; a surcharged or partial approval lands
//                   nothing either: the sweep files the manager item. The
//                   answer carries Run's trans_id as
//                   its provider reference, so a refund or void leg can name
//                   it as its reference.
//
//   a refund or a   POST /api/v1/void-or-refund with the dispatch's
//   void leg:       reference as trans_id, no device involved. The call is
//                   synchronous, so the driver delivers its own answer off
//                   the caller's goroutine once the call answered, exactly
//                   as the simulator does.
//
//   a lost answer:  an interaction is kept only a few minutes; a 404 on the
//                   status call is indistinguishable from a wrong device.
//                   The only documented recovery is the transaction lookup:
//                   the Reporting API's transaction list filtered by
//                   order_id (our idempotency key). The same lookup runs
//                   BEFORE every charge-card POST, so a dispatch is never
//                   re sent without looking the interaction up first
//                   (FROM-PRINCIPAL 222): a charge that already exists at
//                   Run is recovered from the report, never prompted again.
//                   The lookup answers three things, found, not found and
//                   could not ask: a report that could not answer (a 500, a
//                   timeout, a refused connection, an expired token) is not a
//                   report that holds no charge, so the dispatch is refused,
//                   never charged blind, and the leg is left for
//                   reconciliation. Without the reporting credential set the
//                   lookup cannot run; the driver still refuses a blind re
//                   send of an order id its own process already posted.
//
//   settlement:     SettlementBatch reads the Reporting API's transaction
//                   list filtered by terminal id, the documented source of
//                   what a device settled.
//
// The reporting credential set is Run's own (client id and secret, an
// OAuth client credentials token); it is optional and resolved at
// construction: without it the lookup and the settlement batch answer an
// error, and the ledger's sweep and manager items resolve what the poller
// could not.

// runTerminalDefaults: the poll cadence, the interaction window and the
// longest one call the driver makes. Run keeps an interaction about three
// minutes; the window bounds how long the poller keeps asking before it
// falls to the transaction lookup.
//
// The window fits inside the ledger's lease (TerminalAnswerLease), with room
// left for the charge-card call that precedes the poller: a poller that keeps
// working past the lease is closed by the sweep while the device is still
// answering, and the leg is filed as a manager item for an answer that is
// about to land.
//
// The room covers the longest charge-card call the driver allows
// (runTerminalCallTimeout), and everything the driver does before the poller
// starts runs inside that call's budget, so the poller is always done before
// the lease runs out.
const (
	runTerminalPollDefault   = 500 * time.Millisecond
	runTerminalWindowDefault = TerminalAnswerLease - runTerminalDispatchRoom
	// runTerminalCallTimeout is the longest one HTTP call the driver may
	// take: the charge-card call, the reporting lookup and the reporting
	// token. It is also the budget of everything the driver does before the
	// poller starts.
	runTerminalCallTimeout = 30 * time.Second
	// runTerminalDispatchMargin is the room the poller's last answer needs:
	// the poller is done before the lease runs out, not at the same instant.
	runTerminalDispatchMargin = 5 * time.Second
	// runTerminalDispatchRoom is the room the poller's window leaves inside
	// the lease for the charge-card call that precedes it.
	runTerminalDispatchRoom   = runTerminalCallTimeout + runTerminalDispatchMargin
	runReportingTokenLifetime = 25 * time.Minute
)

// errRunReportingUnset says the reporting credential set is absent: the
// transaction lookup cannot run at all. It is not the same as a report that
// could not answer, and every caller keeps the two apart.
var errRunReportingUnset = errors.New("the run reporting credential set is not configured")

// RunTerminal is the Run Terminal API driver of the TerminalProvider seam.
// Safe for concurrent use.
type RunTerminal struct {
	configProvider  func() GatewayConfig
	onRotate        func(apiKey, refreshToken string)
	answers         AnswerSink
	reportingBase   string
	reportingID     string
	reportingSecret string
	pollInterval    time.Duration
	window          time.Duration
	client          *http.Client
	logger          *slog.Logger

	// guards holds the driver's own no double charge guard, one per order
	// id (a reversal's under its own prefixed name, see reverse): the check and the charge-card POST that may charge it happen
	// under one lock, so four concurrent dispatches of one leg make one
	// charge.
	guardMu sync.Mutex
	guards  map[string]*runOrderGuard

	// the reporting access token and when it expires.
	tokenMu     sync.Mutex
	token       string
	tokenExpiry time.Time
}

// runOrderGuard is the driver's own no double charge guard for one order id.
// The transaction lookup, the in process refusal and the charge-card POST
// happen under it, so the check and the act it guards are one thing.
// attempted says this process already posted the order id, or may have: a
// lost answer (a timeout, a dropped 202) may still have charged, so the
// order id is never posted twice.
type runOrderGuard struct {
	mu        sync.Mutex
	attempted bool
}

// NewRunTerminal builds the driver on a configuration provider (the Run
// credentials resolve at call time, so Tech Admin settings take effect
// without a restart) and the sink its asynchronous answers land in.
func NewRunTerminal(provider func() GatewayConfig, answers AnswerSink, logger *slog.Logger) *RunTerminal {
	if logger == nil {
		logger = slog.Default()
	}
	return &RunTerminal{
		configProvider: provider, answers: answers, logger: logger,
		pollInterval: runTerminalPollDefault, window: runTerminalWindowDefault,
		client: &http.Client{Timeout: runTerminalCallTimeout}, guards: map[string]*runOrderGuard{},
	}
}

// OnKeyRotated registers the callback for a refreshed api_key (the Run
// api_key is an expiring credential; the rotation is persisted by the
// caller, exactly as the keyed gateway's is).
func (t *RunTerminal) OnKeyRotated(fn func(apiKey, refreshToken string)) *RunTerminal {
	t.onRotate = fn
	return t
}

// WithReporting sets the Reporting API credential set that the transaction
// lookup and the settlement batch need. An empty base disables both.
func (t *RunTerminal) WithReporting(base, clientID, clientSecret string) *RunTerminal {
	t.reportingBase, t.reportingID, t.reportingSecret = base, clientID, clientSecret
	return t
}

// WithPollInterval sets the poll cadence of an interaction's status.
func (t *RunTerminal) WithPollInterval(d time.Duration) *RunTerminal {
	if d > 0 {
		t.pollInterval = d
	}
	return t
}

// WithInteractionWindow bounds how long the poller asks one interaction. A
// window longer than the ledger's lease is clamped to it: the sweep closes a
// leg whose lease ran out, and the poller has to be done before that.
func (t *RunTerminal) WithInteractionWindow(d time.Duration) *RunTerminal {
	if d > 0 {
		t.window = d
		if t.window > TerminalAnswerLease {
			t.window = TerminalAnswerLease
		}
	}
	return t
}

// InteractionWindow reports how long the poller asks one interaction. It is
// never longer than the ledger's lease (TerminalAnswerLease): the sweep
// closes a leg whose lease ran out, and the poller has to be done before
// that.
func (t *RunTerminal) InteractionWindow() time.Duration { return t.window }

// CallTimeout reports the longest one HTTP call the driver may take: the
// charge-card call, the reporting lookup and the reporting token.
//
// The poller's window has to fit inside the lease with this much room left:
// the lease clock starts before the dispatch and the poller's window starts
// after the charge-card call answers, so a call that outlasted the room
// would leave the poller asking Run after the sweep closed the leg.
func (t *RunTerminal) CallTimeout() time.Duration { return t.client.Timeout }

// guardFor returns the no double charge guard of one order id, making it on
// first use.
func (t *RunTerminal) guardFor(orderID string) *runOrderGuard {
	t.guardMu.Lock()
	defer t.guardMu.Unlock()
	g, ok := t.guards[orderID]
	if !ok {
		g = &runOrderGuard{}
		t.guards[orderID] = g
	}
	return g
}

// ----- the documented wire shapes -----

type runChargeCardRequest struct {
	Amount  string `json:"amount"`   // decimal dollars, as a string
	OrderID string `json:"order_id"` // our idempotency key; Run documents no other
}

type runInteractionAck struct {
	InteractionID string `json:"interaction_id"`
}

type runChargeCardStatus struct {
	InteractionID string            `json:"interaction_id"`
	Status        string            `json:"status"`
	Result        *runChargeCardRes `json:"result"`
	UpdatedAt     string            `json:"updated_at"`
}

type runChargeCardRes struct {
	Result   string     `json:"result"` // A approved, B retry, C declined
	TransID  string     `json:"trans_id"`
	RespCode string     `json:"resp_code"`
	RespText string     `json:"resp_text"`
	CardType string     `json:"card_type"`
	Amount   runDollars `json:"amount"` // what the card was charged, fee included
	// FeeAmount is the surcharge the gateway added (a merchant enrolled in
	// surcharging, no request field): the card is charged the subtotal plus
	// this fee.
	FeeAmount runDollars `json:"fee_amount"`
	// StageErrors names the stage of the workflow that failed (read,
	// signature, charge, print, other). A read stage error means the card
	// was never read, so no charge ran.
	StageErrors map[string]json.RawMessage `json:"stage_errors"`
}

// runDollars is a dollars figure Run answers as a string ("25.00") or a
// JSON number; it keeps the text as sent.
type runDollars string

func (d *runDollars) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*d = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*d = runDollars(s)
		return nil
	}
	*d = runDollars(b)
	return nil
}

// cents reads the dollars figure as integer cents, never through a float:
// "25", "25.5", "25.50" and "25.500" are 2500, 2550, 2550 and 2550. ok is
// false for an empty, negative or malformed figure, or one finer than a cent.
func (d runDollars) cents() (int64, bool) {
	s := string(d)
	if s == "" || s[0] == '-' {
		return 0, false
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	for len(frac) > 2 && frac[len(frac)-1] == '0' {
		frac = frac[:len(frac)-1]
	}
	if len(frac) > 2 {
		return 0, false
	}
	for len(frac) < 2 {
		frac += "0"
	}
	if !allDigits(whole) || !allDigits(frac) {
		return 0, false
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return 0, false
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, false
	}
	return w*100 + f, true
}

// allDigits says whether s is ASCII digits only: ParseInt takes a sign, a
// dollars figure has none in either part.
func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// stageFailed says whether the named stage carries an error.
func (r *runChargeCardRes) stageFailed(stage string) bool {
	raw, ok := r.StageErrors[stage]
	if !ok {
		return false
	}
	switch strings.TrimSpace(string(raw)) {
	case "", "null", `""`, "false", "{}", "[]":
		return false
	}
	return true
}

type runVoidOrRefundTerminalRequest struct {
	TransID        string `json:"trans_id"`
	MID            string `json:"mid"`
	Amount         int64  `json:"amount,omitempty"` // cents on a refund; absent on a void
	UserIdentifier string `json:"user_identifier,omitempty"`
}

type runReportingTransaction struct {
	OrderID    string  `json:"order_id"`
	TransID    string  `json:"trans_id"`
	Result     string  `json:"result"`
	RespText   string  `json:"resp_text"`
	AuthCode   string  `json:"auth_code"`
	CardNumber string  `json:"card_number"` // masked
	Amount     float64 `json:"amount"`      // cents
	TerminalID string  `json:"terminal_id"`
	TransDate  string  `json:"trans_date"`
}

type runReportingTransactions struct {
	Items []runReportingTransaction `json:"items"`
}

// Dispatch hands one leg to Run: a sale as a terminal interaction whose
// verdict the poller lands, a refund or void as a synchronous
// void-or-refund call whose answer the driver delivers itself.
func (t *RunTerminal) Dispatch(ctx context.Context, d TerminalDispatch) (TerminalReceipt, error) {
	switch d.Kind {
	case TerminalKindSale:
		return t.dispatchSale(ctx, d)
	case TerminalKindRefund, TerminalKindVoid:
		return t.dispatchReversal(ctx, d)
	}
	return TerminalReceipt{}, fmt.Errorf("the Run terminal takes no legs of kind %q", d.Kind)
}

// dollarsOf renders cents as the dollars string the Terminal API reads.
func dollarsOf(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// dispatchSale posts one charge-card interaction and starts its poller.
func (t *RunTerminal) dispatchSale(ctx context.Context, d TerminalDispatch) (TerminalReceipt, error) {
	cfg, err := t.resolve()
	if err != nil {
		return TerminalReceipt{}, err
	}
	// Everything before the poller starts runs inside one budget: the lease
	// clock started before this dispatch, so the work that precedes the
	// poller (the reporting token, the transaction lookup, the charge-card
	// call) may not outlast the longest call the driver allows, or the
	// poller's window would end past the lease.
	dctx, cancel := context.WithTimeout(ctx, runTerminalCallTimeout)
	defer cancel()
	// The guard is held across the check and the act it guards: the
	// transaction lookup, the in process refusal and the charge-card POST.
	// Four concurrent dispatches of one leg make one charge.
	guard := t.guardFor(d.IdempotencyKey)
	guard.mu.Lock()
	defer guard.mu.Unlock()
	// Never re send a dispatch without looking the interaction up first:
	// a charge that already exists at Run under this order id is recovered
	// from the transaction lookup, never prompted again. The lookup runs only
	// when the reporting credential set is configured; without that set there
	// is no lookup to run, and the guard below is the whole protection: it
	// refuses a re dispatch of an order id this process already posted.
	if t.reportingConfigured() {
		found, lerr := t.lookupByOrder(dctx, d.IdempotencyKey)
		if lerr != nil {
			// The report could not answer. That is not a report that holds no
			// charge: the dispatch is refused, never sent blind, and the leg
			// is left for reconciliation.
			t.logger.Error("the Run transaction lookup could not answer: the dispatch is refused, never sent blind",
				"order_id", d.IdempotencyKey, "error", lerr)
			return TerminalReceipt{}, fmt.Errorf("the run transaction lookup could not answer for order %s, so the dispatch is not sent and the leg is left for reconciliation: %w", d.IdempotencyKey, lerr)
		}
		if found != nil {
			t.logger.Info("the Run charge already exists: recovered from the transaction lookup, the device is not prompted again",
				"order_id", d.IdempotencyKey, "trans_id", found.TransID)
			t.land(d, answerOfTransaction(d, found))
			return TerminalReceipt{ProviderRef: found.TransID, DeviceID: d.DeviceID}, nil
		}
	}
	if guard.attempted {
		// The guard is the whole protection when no reporting credential set
		// is configured: there is no lookup to ask, so the refusal says so
		// instead of naming a lookup that never ran.
		why := "the transaction lookup found no charge for it"
		if !t.reportingConfigured() {
			why = "no transaction lookup is configured to ask"
		}
		return TerminalReceipt{}, fmt.Errorf("the Run terminal already took order %s in this process and %s: the dispatch is not re sent", d.IdempotencyKey, why)
	}
	body, _ := json.Marshal(runChargeCardRequest{Amount: dollarsOf(d.AmountCents), OrderID: d.IdempotencyKey})
	// The order id is marked under the guard before the POST: a lost answer
	// (a timeout, a dropped 202) may still have charged, so this process
	// never posts the same order id twice.
	guard.attempted = true
	raw, status, err := t.authed(dctx, cfg, http.MethodPost,
		"/api/terminal/"+d.DeviceID+"/charge-card", body, 0)
	if err != nil {
		return TerminalReceipt{}, fmt.Errorf("the run charge-card dispatch failed: %w", err)
	}
	if status >= 400 {
		return TerminalReceipt{}, fmt.Errorf("run charge-card answered %d: %s", status, string(raw))
	}
	var ack runInteractionAck
	if err := json.Unmarshal(raw, &ack); err != nil || ack.InteractionID == "" {
		return TerminalReceipt{}, fmt.Errorf("run charge-card answered no interaction id")
	}
	// the poller runs on its own context: it outlives the caller's request,
	// never inside the dispatch call.
	go t.poll(context.WithoutCancel(ctx), cfg, d, ack.InteractionID)
	return TerminalReceipt{ProviderRef: ack.InteractionID, DeviceID: d.DeviceID}, nil
}

// poll reads one interaction's status until it stands or the window runs
// out, then lands the verdict through the sink. A vanished interaction
// (404: expired, wrong device, or never existed) falls to the transaction
// lookup, the only documented recovery of a lost answer.
func (t *RunTerminal) poll(ctx context.Context, cfg GatewayConfig, d TerminalDispatch, interaction string) {
	pctx, cancel := context.WithTimeout(ctx, t.window+30*time.Second)
	defer cancel()
	deadline := time.Now().Add(t.window)
	for {
		raw, status, err := t.authed(pctx, cfg, http.MethodGet,
			"/api/terminal/"+d.DeviceID+"/charge-card/"+interaction, nil, 0)
		switch {
		case err == nil && status == http.StatusOK:
			var st runChargeCardStatus
			if jerr := json.Unmarshal(raw, &st); jerr == nil {
				switch v := answerOfInteraction(d, interaction, &st); v.end {
				case runEndFinal, runEndHeld:
					t.land(d, v)
					return
				case runEndUncertain:
					// An uncertain end (no verdict the interaction can speak
					// for) consults the transaction lookup before it declines:
					// a charge the report shows is booked by its own trans id,
					// never declined with the money moved.
					found, lerr := t.lookupByOrder(pctx, d.IdempotencyKey)
					switch {
					case errors.Is(lerr, errRunReportingUnset):
						// no lookup can run, and the charge may have moved: the
						// leg stays at the device and the sweep files the
						// manager item, never a decline the cashier would take
						// the card again on
						t.logger.Warn("the Run interaction ended with no verdict and no transaction lookup is configured: the sweep files the manager item",
							"order_id", d.IdempotencyKey, "interaction_id", interaction, "status", st.Status)
					case lerr != nil:
						// the report could not answer: the leg is left for
						// reconciliation, never booked declined with the
						// money moved
						t.logger.Error("the Run transaction lookup could not answer: the leg is left for reconciliation",
							"order_id", d.IdempotencyKey, "error", lerr)
					case found != nil:
						t.logger.Info("the Run interaction ended with no verdict and the transaction lookup found the charge",
							"order_id", d.IdempotencyKey, "trans_id", found.TransID)
						t.land(d, answerOfTransaction(d, found))
					default:
						// the report answered and holds no charge for the order
						t.land(d, v)
					}
					return
				}
			}
		case err == nil && status == http.StatusNotFound:
			// the interaction vanished: the transaction lookup resolves it
			found, lerr := t.lookupByOrder(pctx, d.IdempotencyKey)
			switch {
			case lerr != nil && !errors.Is(lerr, errRunReportingUnset):
				t.logger.Error("the Run interaction expired and the transaction lookup could not answer: the leg is left for reconciliation",
					"order_id", d.IdempotencyKey, "interaction_id", interaction, "error", lerr)
			case found != nil:
				t.logger.Info("the Run interaction expired and the transaction lookup resolved the leg",
					"order_id", d.IdempotencyKey, "trans_id", found.TransID)
				t.land(d, answerOfTransaction(d, found))
			default:
				t.logger.Warn("the Run interaction expired and the transaction lookup found no charge: the sweep files the manager item",
					"order_id", d.IdempotencyKey, "interaction_id", interaction)
			}
			return
		}
		if time.Now().After(deadline) {
			found, lerr := t.lookupByOrder(pctx, d.IdempotencyKey)
			if lerr == nil && found != nil {
				t.land(d, answerOfTransaction(d, found))
				return
			}
			if lerr != nil && !errors.Is(lerr, errRunReportingUnset) {
				t.logger.Error("the Run interaction window ran out and the transaction lookup could not answer: the leg is left for reconciliation",
					"order_id", d.IdempotencyKey, "interaction_id", interaction, "error", lerr)
				return
			}
			t.logger.Warn("the Run interaction window ran out with no verdict: the sweep files the manager item",
				"order_id", d.IdempotencyKey, "interaction_id", interaction)
			return
		}
		select {
		case <-pctx.Done():
			return
		case <-time.After(t.pollInterval):
		}
	}
}

// runEnd says what one interaction status or report row lets the driver do.
type runEnd int

const (
	// runEndPending: the interaction is still at the device.
	runEndPending runEnd = iota
	// runEndFinal: a verdict the driver books.
	runEndFinal
	// runEndUncertain: no verdict the interaction can speak for, and the
	// charge may have moved; the transaction lookup decides.
	runEndUncertain
	// runEndHeld: an approval the driver may not book as the leg (the card
	// was charged another figure): nothing is landed, the leg stays at the
	// device and the sweep carries it to a person.
	runEndHeld
)

// runVerdict is one mapped answer with what the driver may do with it.
type runVerdict struct {
	TerminalAnswer
	end runEnd
	// held says why an approval is not booked, with both figures.
	held string
	// heldFee is what a held approval charged above the leg (CP-15), kept
	// on the leg for the manager's resolution.
	heldFee int64
}

// answerOfInteraction maps one interaction status. A succeeded interaction
// is the workflow's end, not the card's: result A authorized, C declined, B
// declined too (no money moved; the cashier starts a new leg), unless the
// result names a charge stage error, which is uncertain; an empty or unknown
// result code is uncertain too. An approval is booked when the card was
// charged exactly the leg's amount plus the fee the answer names (stored on
// the answer as cents, CP-15); any other figure, a partial approval among
// them, is held for a person. A failed or
// cancelled interaction whose error is at the card read ran no charge and
// declines. Any other failed or cancelled interaction, or a succeeded one
// that carries no result object, is uncertain: a gateway fault is not a
// decline and the charge may have moved.
func answerOfInteraction(d TerminalDispatch, interaction string, st *runChargeCardStatus) runVerdict {
	v := runVerdict{TerminalAnswer: TerminalAnswer{IntentID: d.IntentID, ProviderRef: interaction, Verdict: "declined"}}
	switch st.Status {
	case "succeeded":
		if st.Result == nil {
			v.end = runEndUncertain
			return v
		}
		v.Brand = st.Result.CardType
		v.AuthCode = st.Result.RespCode
		if st.Result.TransID != "" {
			v.ProviderRef = st.Result.TransID
		}
		switch st.Result.Result {
		case "A":
		case "B", "C":
			// no money moved, unless the charge stage itself failed: a
			// gateway fault is not a decline
			v.end = runEndFinal
			if st.Result.stageFailed("charge") {
				v.end = runEndUncertain
			}
			return v
		default:
			// an empty or unknown code is no verdict the driver can book
			v.end = runEndUncertain
			return v
		}
		v.end = runEndFinal
		v.Verdict = "authorized"
		// The processor decides the surcharge (CP-15): the card is charged the
		// leg's amount plus the fee its answer names. The fee is kept as cents
		// from the dollars string; the figure charged must equal the two, or
		// the answer is held for a person.
		var fee int64
		if st.Result.FeeAmount != "" {
			f, ok := st.Result.FeeAmount.cents()
			if !ok {
				v.end, v.held = runEndHeld, fmt.Sprintf("the card was surcharged %q, a figure the driver cannot read, on a leg of %d cents", st.Result.FeeAmount, d.AmountCents)
				if charged, ok := st.Result.Amount.cents(); ok && charged > d.AmountCents {
					v.heldFee = charged - d.AmountCents
				}
				return v
			}
			fee = f
		}
		if charged, ok := st.Result.Amount.cents(); !ok || charged != d.AmountCents+fee {
			v.end, v.held = runEndHeld, fmt.Sprintf("the card was charged %q on a leg of %d cents with a fee of %d cents", st.Result.Amount, d.AmountCents, fee)
			if ok && charged > d.AmountCents {
				v.heldFee = charged - d.AmountCents
			}
			return v
		}
		v.FeeCents = fee
		return v
	case "failed", "cancelled":
		if st.Result != nil && st.Result.stageFailed("read") && !st.Result.stageFailed("charge") {
			v.end = runEndFinal
			return v
		}
		v.end = runEndUncertain
		return v
	}
	v.end = runEndPending
	return v
}

// answerOfTransaction maps one reporting transaction row: the masked card
// number's last four are the only card shape the report carries, and the
// result code is the same A/B/C envelope. An approval for a figure other than
// the leg's is held, as on the interaction.
func answerOfTransaction(d TerminalDispatch, tx *runReportingTransaction) runVerdict {
	v := runVerdict{end: runEndFinal, TerminalAnswer: TerminalAnswer{IntentID: d.IntentID, ProviderRef: tx.TransID,
		Verdict: "declined", AuthCode: tx.AuthCode, Last4: lastFourOf(tx.CardNumber)}}
	if tx.Result == "A" {
		v.Verdict = "authorized"
		if tx.Amount != float64(d.AmountCents) {
			v.end, v.held = runEndHeld, fmt.Sprintf("the report shows %v cents charged on a leg of %d cents", tx.Amount, d.AmountCents)
			if charged := int64(tx.Amount); float64(charged) == tx.Amount && charged > d.AmountCents {
				v.heldFee = charged - d.AmountCents
			}
		}
	}
	return v
}

// lastFourOf reads the trailing four digits of a masked card number.
func lastFourOf(masked string) string {
	if len(masked) < 4 {
		return ""
	}
	tail := masked[len(masked)-4:]
	for _, r := range tail {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return tail
}

// dispatchReversal runs one refund or void leg through void-or-refund: a
// synchronous Payments API call with no device. The answer is delivered off
// the caller's goroutine, as a device's own answer would be.
//
// The reversal takes its own no double refund guard, keyed apart from the
// sale's (see reverse): a retried reversal is the same leg, so the customer
// is refunded once and the second dispatch is refused.
func (t *RunTerminal) dispatchReversal(ctx context.Context, d TerminalDispatch) (TerminalReceipt, error) {
	res, err := t.reverse(ctx, d.Kind, d.Reference, d.AmountCents, d.IdempotencyKey)
	if err != nil {
		return TerminalReceipt{}, err
	}
	ref := res.TransID
	if ref == "" {
		ref = d.Reference
	}
	answer := TerminalAnswer{IntentID: d.IntentID, ProviderRef: ref, Verdict: "declined", AuthCode: res.RespCode}
	if res.Result == "A" {
		answer.Verdict = "authorized"
	}
	t.deliver(d, answer)
	return TerminalReceipt{ProviderRef: ref, DeviceID: d.DeviceID}, nil
}

// reversalGuardPrefix names a reversal's guard apart from the charge's.
const reversalGuardPrefix = "reverse:"

// reverse is the one Run void-or-refund call, shared by the ledger's
// reversal legs (dispatchReversal) and the counter's way back
// (RunTerminalGateway). kind is TerminalKindRefund or TerminalKindVoid; a
// void sends no amount. The key goes as user_identifier and names the
// no double refund guard: a key is reversed at most once per process.
//
// The reversal guard is keyed apart from the charge guard (CP-05 round 3
// P2-B): a lost sale leg is reversed under that leg's own key, the key the
// charge already marked attempted, and sharing the guard refused that
// reversal before any POST. The prefix is the guard's name only; the wire
// user_identifier is the key unchanged.
func (t *RunTerminal) reverse(ctx context.Context, kind string, transID string, amountCents int64, key string) (*runChargeCardRes, error) {
	cfg, err := t.resolve()
	if err != nil {
		return nil, err
	}
	if transID == "" {
		return nil, fmt.Errorf("the run %s leg names no trans_id to reverse", kind)
	}
	guard := t.guardFor(reversalGuardPrefix + key)
	guard.mu.Lock()
	defer guard.mu.Unlock()
	if guard.attempted {
		return nil, fmt.Errorf("the Run terminal already took the %s of order %s in this process: the reversal is not sent again", kind, key)
	}
	var amount int64
	if kind == TerminalKindRefund {
		amount = amountCents // cents; a void is the full transaction by definition
	}
	body, _ := json.Marshal(runVoidOrRefundTerminalRequest{
		TransID: transID, MID: cfg.MID, Amount: amount, UserIdentifier: key})
	// marked under the guard before the POST: a lost answer may still have
	// refunded, so the reversal is never sent twice.
	guard.attempted = true
	raw, status, err := t.authed(ctx, cfg, http.MethodPost, "/api/v1/void-or-refund", body, 0)
	if err != nil {
		return nil, fmt.Errorf("the run %s failed: %w", kind, err)
	}
	if status >= 400 {
		return nil, fmt.Errorf("run void-or-refund answered %d: %s", status, string(raw))
	}
	var res runChargeCardRes
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("bad run void-or-refund response: %w", err)
	}
	return &res, nil
}

// land delivers a final verdict, and lands nothing for a held one: the leg
// stays pending at the device, the sweep files the manager item, and the
// log names both figures for the person who resolves it.
func (t *RunTerminal) land(d TerminalDispatch, v runVerdict) {
	if v.end == runEndHeld {
		t.logger.Warn("the Run approval is not booked: "+v.held+"; the sweep files the manager item",
			"order_id", d.IdempotencyKey, "trans_id", v.ProviderRef)
		t.holdFee(d, v.heldFee)
		return
	}
	t.deliver(d, v.TerminalAnswer)
}

// holdFee keeps a held approval's figure above the leg on the leg, on the
// driver's own time as deliver does (the dispatch move commits first).
func (t *RunTerminal) holdFee(d TerminalDispatch, fee int64) {
	sink, ok := t.answers.(HeldAnswerSink)
	if !ok || fee <= 0 {
		return
	}
	time.AfterFunc(20*time.Millisecond, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := sink.HoldDeviceAnswer(ctx, d.IntentID, fee); err != nil {
			t.logger.Error("the Run terminal's held fee did not land",
				"intent_id", d.IntentID, "fee_cents", fee, "error", err)
		}
	})
}

// deliver lands one answer in the sink on the driver's own time: never
// inside the dispatch call, so the caller's dispatch move is committed
// before the answer can land, exactly as at a real device.
func (t *RunTerminal) deliver(d TerminalDispatch, a TerminalAnswer) {
	if t.answers == nil {
		return
	}
	time.AfterFunc(20*time.Millisecond, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := t.answers.ApplyDeviceAnswer(ctx, a); err != nil {
			t.logger.Error("the Run terminal answer did not land",
				"intent_id", d.IntentID, "verdict", a.Verdict, "error", err)
		}
	})
}

// SettlementBatch reads what one device settled: the Reporting API's
// transaction list filtered by terminal id. Without the reporting
// credential set it answers an error; the ledger's own authorized legs
// remain the books' source.
func (t *RunTerminal) SettlementBatch(ctx context.Context, deviceID string, from, to time.Time) ([]TerminalBatchLine, error) {
	rows, err := t.reportingTransactions(ctx, map[string]any{"terminal_id": deviceID})
	if err != nil {
		return nil, err
	}
	var out []TerminalBatchLine
	for _, tx := range rows {
		when, werr := time.Parse(time.RFC3339, tx.TransDate)
		if werr != nil || when.Before(from) || when.After(to) {
			continue
		}
		verdict := "declined"
		if tx.Result == "A" {
			verdict = "authorized"
		}
		out = append(out, TerminalBatchLine{ProviderRef: tx.TransID, Verdict: verdict,
			AmountCents: int64(tx.Amount), Last4: lastFourOf(tx.CardNumber), AuthCode: tx.AuthCode})
	}
	return out, nil
}

// ----- HTTP, auth and the reporting lookup -----

// resolve reads the current credentials, with the same contract as the
// keyed gateway: no guessed host, Run provisions the base.
func (t *RunTerminal) resolve() (GatewayConfig, error) {
	cfg := t.configProvider()
	if cfg.APIKey == "" || cfg.MID == "" || cfg.BaseURL == "" {
		return cfg, fmt.Errorf("the run card terminal is not configured: set the run_payments_api_key, run_payments_mid and run_payments_base_url settings")
	}
	return cfg, nil
}

// authed sends one request with the bearer api_key. On a 401 (the api_key is
// an expiring credential) it refreshes once, rotates through onRotate and
// retries. The key is set on the wire and never logged.
func (t *RunTerminal) authed(ctx context.Context, cfg GatewayConfig, method, path string, body []byte, attempt int) (raw []byte, status int, err error) {
	req, err := t.request(ctx, cfg.BaseURL+path, method, body, "Bearer "+cfg.APIKey)
	if err != nil {
		return nil, 0, err
	}
	raw, status, err = t.do(req)
	if err != nil {
		return nil, 0, err
	}
	if status != http.StatusUnauthorized || attempt > 0 {
		return raw, status, nil
	}
	newKey, newRefresh, rerr := t.refreshAPIKey(ctx, cfg.BaseURL, cfg.APIKey)
	if rerr != nil {
		return nil, status, fmt.Errorf("the api_key was rejected and the refresh failed: %w", rerr)
	}
	if t.onRotate != nil {
		t.onRotate(newKey, newRefresh)
	}
	cfg.APIKey = newKey
	req, err = t.request(ctx, cfg.BaseURL+path, method, body, "Bearer "+newKey)
	if err != nil {
		return nil, 0, err
	}
	return t.do(req)
}

// request builds one JSON request with the terminal's auth headers.
func (t *RunTerminal) request(ctx context.Context, url, method string, body []byte, bearer string) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", bearer) // never logged
	}
	return req, nil
}

func (t *RunTerminal) do(req *http.Request) (raw []byte, status int, err error) {
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, _ = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return raw, resp.StatusCode, nil
}

// refreshAPIKey exchanges the expiring api_key for a fresh credential set,
// the same documented endpoint the keyed gateway uses.
func (t *RunTerminal) refreshAPIKey(ctx context.Context, base, currentKey string) (apiKey, refreshToken string, err error) {
	body, _ := json.Marshal(runRefreshRequest{Token: currentKey})
	req, err := t.request(ctx, base+"/api/v1/api_keys/refresh", http.MethodPost, body, "")
	if err != nil {
		return "", "", err
	}
	raw, status, err := t.do(req)
	if err != nil {
		return "", "", err
	}
	if status >= 400 {
		return "", "", fmt.Errorf("status %d", status)
	}
	var out runRefreshResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", err
	}
	if out.APIKey == "" {
		return "", "", errors.New("the refresh returned no api_key")
	}
	return out.APIKey, out.RefreshToken, nil
}

// reportingConfigured says whether the reporting credential set is present:
// without it the transaction lookup cannot run at all, and the driver's own
// per order guard is what keeps a re dispatch from charging twice.
func (t *RunTerminal) reportingConfigured() bool {
	return t.reportingBase != "" && t.reportingID != ""
}

// lookupByOrder reads the Reporting API's transaction list filtered by
// order_id: the documented way back to a charge whose interaction is gone.
//
// It answers three things, and every caller keeps them apart:
//
//   - found: the charge exists (the transaction, no error).
//   - not found: the report answered and holds no such charge (nil, nil).
//   - could not ask: the report could not answer at all (nil, an error: a
//     500, a timeout, a refused connection, an expired token). A report that
//     could not ask is never read as a report that holds no charge: a blind
//     charge is the one thing the rule forbids.
//
// The reporting credential set being absent is errRunReportingUnset, which is
// neither of the three: the lookup cannot run at all.
func (t *RunTerminal) lookupByOrder(ctx context.Context, orderID string) (*runReportingTransaction, error) {
	if !t.reportingConfigured() {
		return nil, errRunReportingUnset
	}
	if orderID == "" {
		return nil, nil
	}
	rows, err := t.reportingTransactions(ctx, map[string]any{"order_id": orderID})
	if err != nil {
		t.logger.Warn("the Run transaction lookup failed", "order_id", orderID, "error", err)
		return nil, err
	}
	for i := range rows {
		if rows[i].OrderID == orderID {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// reportingTransactions reads the Reporting API's transaction list with a
// JSON query object filter (the documented q parameter), on an OAuth
// client credentials token the reporting credential set mints.
func (t *RunTerminal) reportingTransactions(ctx context.Context, filter map[string]any) ([]runReportingTransaction, error) {
	if t.reportingBase == "" || t.reportingID == "" {
		return nil, fmt.Errorf("the run reporting credential set is not configured: the transaction lookup and the settlement batch need it")
	}
	q, _ := json.Marshal(filter)
	token, err := t.reportingToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := t.request(ctx, t.reportingBase+"/api/3p/v1/transactions?q="+url.QueryEscape(string(q)), http.MethodGet, nil, "Bearer "+token)
	if err != nil {
		return nil, err
	}
	raw, status, err := t.do(req)
	if err != nil {
		return nil, fmt.Errorf("the run reporting request failed: %w", err)
	}
	if status >= 400 {
		return nil, fmt.Errorf("the run reporting request answered %d: %s", status, string(raw))
	}
	var out runReportingTransactions
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("bad run reporting response: %w", err)
	}
	return out.Items, nil
}

// reportingToken mints and caches the reporting access token (client
// credentials, HTTP Basic, the documented oauth/token endpoint).
func (t *RunTerminal) reportingToken(ctx context.Context) (string, error) {
	t.tokenMu.Lock()
	defer t.tokenMu.Unlock()
	if t.token != "" && time.Now().Before(t.tokenExpiry) {
		return t.token, nil
	}
	req, err := t.request(ctx, t.reportingBase+"/oauth/token", http.MethodPost,
		[]byte(`{"grant_type":"client_credentials"}`), "")
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(t.reportingID, t.reportingSecret)
	raw, status, err := t.do(req)
	if err != nil {
		return "", err
	}
	if status >= 400 {
		return "", fmt.Errorf("the run reporting token endpoint answered %d", status)
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.AccessToken == "" {
		return "", fmt.Errorf("the run reporting token endpoint answered no access token")
	}
	t.token, t.tokenExpiry = out.AccessToken, time.Now().Add(runReportingTokenLifetime)
	return t.token, nil
}
