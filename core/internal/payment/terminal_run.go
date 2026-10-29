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
//                   result A authorized, C declined (a decline is a
//                   succeeded interaction), B declined too (a transient
//                   processor retry: no money moved, the cashier starts a
//                   new leg), a failed or cancelled interaction declined (no
//                   charge stage ran). The answer carries Run's trans_id as
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
//                   Without the reporting credential set the lookup cannot
//                   run; the driver still refuses a blind re send of an
//                   order id its own process already posted.
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

// runTerminalDefaults: the poll cadence and the interaction window. Run
// keeps an interaction about three minutes; the window bounds how long the
// poller keeps asking before it falls to the transaction lookup.
const (
	runTerminalPollDefault    = 500 * time.Millisecond
	runTerminalWindowDefault  = 3 * time.Minute
	runReportingTokenLifetime = 25 * time.Minute
)

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

	// sentOrders remembers the order ids this process posted, so a re
	// dispatch of the same leg without the reporting credential set is
	// still refused rather than blindly re sent.
	sentMu sync.Mutex
	sent   map[string]bool

	// the reporting access token and when it expires.
	tokenMu     sync.Mutex
	token       string
	tokenExpiry time.Time
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
		client: &http.Client{Timeout: 30 * time.Second}, sent: map[string]bool{},
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

// WithInteractionWindow bounds how long the poller asks one interaction.
func (t *RunTerminal) WithInteractionWindow(d time.Duration) *RunTerminal {
	if d > 0 {
		t.window = d
	}
	return t
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
	Result   string `json:"result"` // A approved, B retry, C declined
	TransID  string `json:"trans_id"`
	RespCode string `json:"resp_code"`
	RespText string `json:"resp_text"`
	CardType string `json:"card_type"`
	Amount   string `json:"amount"`
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
	// Never re send a dispatch without looking the interaction up first:
	// a charge that already exists at Run under this order id is recovered
	// from the transaction lookup, never prompted again.
	if found := t.lookupByOrder(ctx, d.IdempotencyKey); found != nil {
		t.logger.Info("the Run charge already exists: recovered from the transaction lookup, the device is not prompted again",
			"order_id", d.IdempotencyKey, "trans_id", found.TransID)
		t.deliver(d, answerOfTransaction(d, found))
		return TerminalReceipt{ProviderRef: found.TransID, DeviceID: d.DeviceID}, nil
	}
	t.sentMu.Lock()
	already := t.sent[d.IdempotencyKey]
	t.sentMu.Unlock()
	if already {
		return TerminalReceipt{}, fmt.Errorf("the Run terminal already took order %s in this process and the transaction lookup found no charge for it: the dispatch is not re sent", d.IdempotencyKey)
	}
	body, _ := json.Marshal(runChargeCardRequest{Amount: dollarsOf(d.AmountCents), OrderID: d.IdempotencyKey})
	raw, status, err := t.authed(ctx, cfg, http.MethodPost,
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
	t.sentMu.Lock()
	t.sent[d.IdempotencyKey] = true
	t.sentMu.Unlock()
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
				if answer, done, uncertain := answerOfInteraction(d, interaction, &st); done {
					// An uncertain end (no verdict the interaction can speak
					// for) consults the transaction lookup before it declines:
					// a charge the report shows is authorized by its own trans
					// id, never booked declined with the money moved.
					if uncertain {
						if found := t.lookupByOrder(pctx, d.IdempotencyKey); found != nil {
							t.logger.Info("the Run interaction ended with no verdict and the transaction lookup found the charge",
								"order_id", d.IdempotencyKey, "trans_id", found.TransID)
							t.deliver(d, answerOfTransaction(d, found))
							return
						}
					}
					t.deliver(d, answer)
					return
				}
			}
		case err == nil && status == http.StatusNotFound:
			// the interaction vanished: the transaction lookup resolves it
			if found := t.lookupByOrder(pctx, d.IdempotencyKey); found != nil {
				t.logger.Info("the Run interaction expired and the transaction lookup resolved the leg",
					"order_id", d.IdempotencyKey, "trans_id", found.TransID)
				t.deliver(d, answerOfTransaction(d, found))
			} else {
				t.logger.Warn("the Run interaction expired and the transaction lookup found no charge: the sweep files the manager item",
					"order_id", d.IdempotencyKey, "interaction_id", interaction)
			}
			return
		}
		if time.Now().After(deadline) {
			if found := t.lookupByOrder(pctx, d.IdempotencyKey); found != nil {
				t.deliver(d, answerOfTransaction(d, found))
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

// answerOfInteraction maps one interaction status to the answer the sink
// takes, with done=false while it stays pending. A succeeded interaction is
// the workflow's end, not the card's: result A authorized, C declined, B
// declined too (no money moved; the cashier starts a new leg). A failed or
// cancelled interaction, or a succeeded one that carries no result object,
// ran no charge the answer can speak for: uncertain=true, and the caller
// consults the transaction lookup before it lands that decline, because a
// gateway fault is not a decline and the charge may have moved.
func answerOfInteraction(d TerminalDispatch, interaction string, st *runChargeCardStatus) (answer TerminalAnswer, done, uncertain bool) {
	answer = TerminalAnswer{IntentID: d.IntentID, ProviderRef: interaction, Verdict: "declined"}
	switch st.Status {
	case "pending":
		return answer, false, false
	case "succeeded":
		if st.Result != nil {
			answer.Brand = st.Result.CardType
			answer.AuthCode = st.Result.RespCode
			if st.Result.TransID != "" {
				answer.ProviderRef = st.Result.TransID
			}
			if st.Result.Result != "A" {
				return answer, true, false
			}
			answer.Verdict = "authorized"
			return answer, true, false
		}
		return answer, true, true
	case "failed", "cancelled":
		return answer, true, true
	}
	return answer, false, false
}

// answerOfTransaction maps one reporting transaction row to the answer the
// sink takes: the masked card number's last four are the only card shape
// the report carries, and the result code is the same A/B/C envelope.
func answerOfTransaction(d TerminalDispatch, tx *runReportingTransaction) TerminalAnswer {
	answer := TerminalAnswer{IntentID: d.IntentID, ProviderRef: tx.TransID, Verdict: "declined",
		AuthCode: tx.AuthCode, Last4: lastFourOf(tx.CardNumber)}
	if tx.Result == "A" {
		answer.Verdict = "authorized"
	}
	return answer
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
func (t *RunTerminal) dispatchReversal(ctx context.Context, d TerminalDispatch) (TerminalReceipt, error) {
	cfg, err := t.resolve()
	if err != nil {
		return TerminalReceipt{}, err
	}
	if d.Reference == "" {
		return TerminalReceipt{}, fmt.Errorf("the run %s leg names no trans_id to reverse", d.Kind)
	}
	var amount int64
	if d.Kind == TerminalKindRefund {
		amount = d.AmountCents // cents; a void is the full transaction by definition
	}
	body, _ := json.Marshal(runVoidOrRefundTerminalRequest{
		TransID: d.Reference, MID: cfg.MID, Amount: amount, UserIdentifier: d.IdempotencyKey})
	raw, status, err := t.authed(ctx, cfg, http.MethodPost, "/api/v1/void-or-refund", body, 0)
	if err != nil {
		return TerminalReceipt{}, fmt.Errorf("the run %s failed: %w", d.Kind, err)
	}
	if status >= 400 {
		return TerminalReceipt{}, fmt.Errorf("run void-or-refund answered %d: %s", status, string(raw))
	}
	var res runChargeCardRes
	if err := json.Unmarshal(raw, &res); err != nil {
		return TerminalReceipt{}, fmt.Errorf("bad run void-or-refund response: %w", err)
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

// authed sends one request with the bearer api_key and the mid header. On a
// 401 (the api_key is an expiring credential) it refreshes once, rotates
// through onRotate and retries. The key is set on the wire and never logged.
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

// lookupByOrder reads the Reporting API's transaction list filtered by
// order_id: the documented way back to a charge whose interaction is gone.
// Nil when the reporting credential set is absent or the lookup finds
// nothing.
func (t *RunTerminal) lookupByOrder(ctx context.Context, orderID string) *runReportingTransaction {
	if t.reportingBase == "" || t.reportingID == "" || orderID == "" {
		return nil
	}
	rows, err := t.reportingTransactions(ctx, map[string]any{"order_id": orderID})
	if err != nil {
		t.logger.Warn("the Run transaction lookup failed", "order_id", orderID, "error", err)
		return nil
	}
	for i := range rows {
		if rows[i].OrderID == orderID {
			return &rows[i]
		}
	}
	return nil
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
