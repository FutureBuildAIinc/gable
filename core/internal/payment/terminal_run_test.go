// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package payment_test

// The Run Payments card present driver (CP-05), proven against a scripted
// fake HTTP server of Run's documented Terminal API contract: dispatch
// (POST charge-card, a dollars string and our idempotency key as order_id),
// the poll (GET charge-card/{interaction_id}), the approved answer, the
// decline (result C under status succeeded), the retry (result B, no money
// moved, the cashier starts a new leg), the failed interaction, the expiry
// (the interaction vanishes and the transaction lookup by order_id resolves
// the lost answer), the rule that a dispatch is never re sent without
// looking the interaction up first (the reporting lookup runs before every
// charge-card POST), refund and void through void-or-refund, and the poll
// and the sweep racing on one intent at pool size 4 with three contenders.
// No test touches a live Run host: every URL is the fake's own.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/payment"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

// runFakeTerminal is the scripted fake of Run's documented contract: the
// Terminal API charge-card endpoints, the Payments API void-or-refund, and
// the Reporting API transaction lookup by order_id. Every response it serves
// is shaped as the public reference documents it.
type runFakeTerminal struct {
	mu sync.Mutex
	// chargePosts holds every charge-card POST body (amount, order_id).
	chargePosts []map[string]any
	// voidPosts holds every void-or-refund POST body.
	voidPosts []map[string]any
	// interactionOf maps the issued interaction id to its order id.
	interactionOf map[string]string
	// pollStep scripts the answer of one poll: the poll count decides the
	// response (0 based). Nil answers pending forever.
	pollStep func(order string, poll int) (int, map[string]any)
	// voidAnswer scripts the void-or-refund answer (nil: approved).
	voidAnswer func(body map[string]any) map[string]any
	// reporting holds the transactions the lookup answers, by order id.
	reporting map[string]map[string]any
	// lookupCalls counts the reporting lookups served.
	lookupCalls int
	// pollsByOrder counts the polls served per order id.
	pollsByOrder map[string]int
	tokens       int
}

func newRunFakeTerminal() *runFakeTerminal {
	return &runFakeTerminal{interactionOf: map[string]string{}, reporting: map[string]map[string]any{},
		pollsByOrder: map[string]int{}}
}

func (f *runFakeTerminal) chargePostCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.chargePosts)
}

func (f *runFakeTerminal) lastChargePost() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.chargePosts[len(f.chargePosts)-1]
}

func (f *runFakeTerminal) voidPosts_() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.voidPosts))
	copy(out, f.voidPosts)
	return out
}

// chargeResult builds the documented result object of a charge-card status.
func chargeResult(result, transID, respCode, cardType string) map[string]any {
	return map[string]any{"result": result, "trans_id": transID, "resp_code": respCode,
		"resp_text": "APPROVED", "card_type": cardType, "amount": "25.00", "mid": "MID-TESTSTUB"}
}

func (f *runFakeTerminal) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/terminal/") && strings.HasSuffix(r.URL.Path, "/charge-card"):
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		order, _ := body["order_id"].(string)
		f.mu.Lock()
		f.chargePosts = append(f.chargePosts, body)
		f.mu.Unlock()
		interaction := "11111111-2222-3333-4444-" + fmt.Sprintf("%012d", len(order)%1000000) + fmt.Sprintf("%06d", f.chargePostCount()*7)
		f.mu.Lock()
		f.interactionOf[interaction] = order
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{"interaction_id": interaction})
	case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/charge-card/"):
		parts := strings.Split(r.URL.Path, "/")
		interaction := parts[len(parts)-1]
		f.mu.Lock()
		order := f.interactionOf[interaction]
		step := f.pollStep
		f.pollsByOrder[order]++
		poll := f.pollsByOrder[order] - 1
		f.mu.Unlock()
		if step == nil {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"interaction_id": interaction, "status": "pending", "result": nil, "updated_at": "2026-01-01T00:00:00Z"})
			return
		}
		code, resp := step(order, poll)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/void-or-refund":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.voidPosts = append(f.voidPosts, body)
		answer := f.voidAnswer
		f.mu.Unlock()
		if answer == nil {
			answer = func(map[string]any) map[string]any {
				return map[string]any{"result": "A", "trans_id": "TRX-TESTSTUB", "resp_code": "00", "resp_text": "APPROVED"}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer(body))
	case r.Method == http.MethodGet && r.URL.Path == "/api/3p/v1/transactions":
		var q map[string]any
		if raw := r.URL.Query().Get("q"); raw != "" {
			_ = json.Unmarshal([]byte(raw), &q)
		}
		order, _ := q["order_id"].(string)
		f.mu.Lock()
		f.lookupCalls++
		row := f.reporting[order]
		f.mu.Unlock()
		items := []map[string]any{}
		if row != nil {
			items = append(items, row)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	case r.Method == http.MethodPost && r.URL.Path == "/oauth/token":
		f.mu.Lock()
		f.tokens++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "teststub" + "teststub", "token_type": "bearer"})
	default:
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "not found"})
	}
}

// newRunFake starts the fake server and stops it with the test.
func newRunFake(t *testing.T) *runFakeServer {
	t.Helper()
	f := newRunFakeTerminal()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &runFakeServer{fake: f, server: srv}
}

// newRunDriver wires the driver on the fake, with the real ledger as its sink.
func newRunDriver(t *testing.T, fs *runFakeServer, ledger *payment.TerminalLedger) *payment.RunTerminal {
	t.Helper()
	return payment.NewRunTerminal(func() payment.GatewayConfig{
		return payment.GatewayConfig{APIKey: "teststub" + "teststub", MID: "MID-TESTSTUB", BaseURL: fs.server.URL}
	}, ledger, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithPollInterval(5 * time.Millisecond).
		WithInteractionWindow(2 * time.Second).
		WithReporting(fs.server.URL, "clientid-teststub", "clientsecret-teststub")
}

// claimRunLeg claims and dispatch-moves one sale leg the driver takes.
func claimRunLeg(t *testing.T, l *payment.TerminalLedger, sale uuid.UUID, amount int64) *payment.TerminalIntent {
	t.Helper()
	in := &payment.TerminalIntent{ID: uuid.New(), TransactionID: sale, LegKey: "card-0",
		RegisterID: "REG-" + sale.String()[:12], BranchID: m29BranchOf(t, l), AmountCents: amount,
		Kind: payment.TerminalKindSale, IdempotencyKey: "sale-" + sale.String() + "-card-0",
		LeaseUntil: time.Now().Add(time.Minute)}
	if err := l.Claim(context.Background(), in); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if ok, err := l.MarkDispatched(context.Background(), in.ID, "", in.LeaseUntil); err != nil || !ok {
		t.Fatalf("dispatch move: ok=%v err=%v", ok, err)
	}
	return in
}

// waitForIntentState polls the ledger until the intent holds one of the
// wanted states, and fails the test on timeout.
func waitForIntentState(t *testing.T, l *payment.TerminalLedger, id uuid.UUID, want ...payment.TerminalIntentState) *payment.TerminalIntent {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		in, err := l.Get(context.Background(), id)
		if err != nil {
			t.Fatalf("get intent: %v", err)
		}
		for _, w := range want {
			if in.State == w {
				return in
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the intent never reached %v: it is %s", want, in.State)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// runFakeServer pairs the fake with its server.
type runFakeServer struct {
	fake   *runFakeTerminal
	server *httptest.Server
}

// newRunTerminalFixtureOn builds the fake and the driver on the shared ledger.
func newRunTerminalFixtureOn(t *testing.T, ledger *payment.TerminalLedger) (*runFakeServer, *payment.RunTerminal) {
	t.Helper()
	fs := newRunFake(t)
	return fs, newRunDriver(t, fs, ledger)
}

// TestRunTerminalSaleDispatchPostsTheDocumentedChargeCard: a sale leg is one
// POST charge-card whose amount is the dollars string of the cents and whose
// order_id is our idempotency key; the 202 interaction id is the receipt's
// provider reference.
func TestRunTerminalSaleDispatchPostsTheDocumentedChargeCard(t *testing.T) {
	fs := newRunFake(t)
	var sink recordingSink
	term := payment.NewRunTerminal(func() payment.GatewayConfig{
		return payment.GatewayConfig{APIKey: "teststub" + "teststub", MID: "MID-TESTSTUB", BaseURL: fs.server.URL}
	}, &sink, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithPollInterval(5 * time.Millisecond).WithInteractionWindow(50 * time.Millisecond).
		WithReporting(fs.server.URL, "clientid-teststub", "clientsecret-teststub")
	receipt, err := term.Dispatch(context.Background(), payment.TerminalDispatch{
		IntentID: uuid.New(), Kind: payment.TerminalKindSale, AmountCents: 2500,
		Currency: "USD", RegisterID: "REG-1", DeviceID: "HSN-TESTSTUB", IdempotencyKey: "sale-xyz-card-0"})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if receipt.ProviderRef == "" {
		t.Fatal("the receipt carries no interaction id")
	}
	if got := fs.fake.lastChargePost()["amount"]; got != "25.00" {
		t.Fatalf("the charge amount is %v, want the dollars string 25.00", got)
	}
	if got := fs.fake.lastChargePost()["order_id"]; got != "sale-xyz-card-0" {
		t.Fatalf("the order id is %v, want the idempotency key", got)
	}
}

// recordingSink captures answers for driver tests that run without a ledger.
type recordingSink struct {
	mu       sync.Mutex
	answers  []payment.TerminalAnswer
	outcomes []payment.AnswerOutcome
}

func (s *recordingSink) ApplyDeviceAnswer(ctx context.Context, a payment.TerminalAnswer) (payment.AnswerOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers = append(s.answers, a)
	s.outcomes = append(s.outcomes, payment.AnswerApplied)
	return payment.AnswerApplied, nil
}

func (s *recordingSink) waitForAnswer(t *testing.T) payment.TerminalAnswer {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s.mu.Lock()
		n := len(s.answers)
		s.mu.Unlock()
		if n > 0 {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.answers[0]
		}
		if time.Now().After(deadline) {
			t.Fatal("the driver delivered no answer")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// The poll outcome table: status succeeded with result A, C and B, and a
// failed interaction, each mapped to the intent verdict the ledger records.
func TestRunTerminalPollOutcomes(t *testing.T) {
	cases := []struct {
		name      string
		step      func(order string, poll int) (int, map[string]any)
		wantState payment.TerminalIntentState
		wantVerdt string
		wantRef   string
	}{
		{"approve", func(string, int) (int, map[string]any) {
			return 200, map[string]any{"status": "succeeded", "result": chargeResult("A", "TRX-A1", "00", "VISA"), "updated_at": "2026-01-01T00:00:00Z"}
		}, payment.TerminalAuthorized, "authorized", "TRX-A1"},
		{"decline C under succeeded", func(string, int) (int, map[string]any) {
			return 200, map[string]any{"status": "succeeded", "result": chargeResult("C", "", "05", "VISA"), "updated_at": "2026-01-01T00:00:00Z"}
		}, payment.TerminalDeclined, "declined", ""},
		{"retry B is declined", func(string, int) (int, map[string]any) {
			return 200, map[string]any{"status": "succeeded", "result": chargeResult("B", "", "91", "VISA"), "updated_at": "2026-01-01T00:00:00Z"}
		}, payment.TerminalDeclined, "declined", ""},
		{"failed interaction is declined", func(string, int) (int, map[string]any) {
			return 200, map[string]any{"status": "failed", "result": nil, "updated_at": "2026-01-01T00:00:00Z"}
		}, payment.TerminalDeclined, "declined", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ledger, sale := newTerminalFixture(t)
			fs := newRunFake(t)
			fs.fake.pollStep = tc.step
			term := newRunDriver(t, fs, ledger)
			in := claimRunLeg(t, ledger, sale, 2500)
			if _, err := term.Dispatch(context.Background(), payment.TerminalDispatch{
				IntentID: in.ID, Kind: in.Kind, AmountCents: in.AmountCents, RegisterID: in.RegisterID,
				DeviceID: in.DeviceID, IdempotencyKey: in.IdempotencyKey}); err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			got := waitForIntentState(t, ledger, in.ID, tc.wantState)
			if got.AnswerVerdict != tc.wantVerdt {
				t.Fatalf("verdict %s, want %s", got.AnswerVerdict, tc.wantVerdt)
			}
			if tc.wantRef != "" && got.AnswerProviderRef != tc.wantRef {
				t.Fatalf("answer provider ref %s, want Run's trans id %s", got.AnswerProviderRef, tc.wantRef)
			}
			if tc.wantState == payment.TerminalAuthorized && got.AnswerBrand != "VISA" {
				t.Fatalf("brand %s, want the charge result's card_type", got.AnswerBrand)
			}
		})
	}
}

// TestRunTerminalExpiryResolvedByTransactionLookup: the interaction vanishes
// past its window (every poll 404s), and the transaction lookup by order_id
// is the only documented recovery: the found charge authorizes the leg with
// Run's trans id.
func TestRunTerminalExpiryResolvedByTransactionLookup(t *testing.T) {
	ledger, sale := newTerminalFixture(t)
	fs := newRunFake(t)
	fs.fake.pollStep = func(string, int) (int, map[string]any) {
		return 404, map[string]any{"error": "not found"}
	}
	fs.fake.reporting["sale-"+sale.String()+"-card-0"] = map[string]any{
		"order_id": "sale-" + sale.String() + "-card-0", "trans_id": "TRX-FOUND", "result": "A",
		"auth_code": "OK42", "card_number": "XXXXXXXXXXXX4242", "amount": 2500.0, "resp_text": "APPROVED"}
	term := newRunDriver(t, fs, ledger)
	in := claimRunLeg(t, ledger, sale, 2500)
	if _, err := term.Dispatch(context.Background(), payment.TerminalDispatch{
		IntentID: in.ID, Kind: in.Kind, AmountCents: in.AmountCents, RegisterID: in.RegisterID,
		DeviceID: in.DeviceID, IdempotencyKey: in.IdempotencyKey}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got := waitForIntentState(t, ledger, in.ID, payment.TerminalAuthorized, payment.TerminalDeclined)
	if got.State != payment.TerminalAuthorized {
		t.Fatalf("the lookup should authorize the leg, it is %s", got.State)
	}
	if got.AnswerProviderRef != "TRX-FOUND" {
		t.Fatalf("answer ref %s, want TRX-FOUND", got.AnswerProviderRef)
	}
	if got.AnswerAuthCode != "OK42" {
		t.Fatalf("auth code %s, want OK42", got.AnswerAuthCode)
	}
	if got.AnswerLast4 != "4242" {
		t.Fatalf("last four %s, want the masked card number's 4242", got.AnswerLast4)
	}
}

// TestRunTerminalNeverResendsADispatch (ruling: never re send a dispatch
// without looking the interaction up first): the reporting lookup runs before
// every charge-card POST, so a second dispatch of the same idempotency key
// finds the charge that already exists and never prompts the device again.
func TestRunTerminalNeverResendsADispatch(t *testing.T) {
	ledger, sale := newTerminalFixture(t)
	fs := newRunFake(t)
	fs.fake.pollStep = func(string, int) (int, map[string]any) {
		return 200, map[string]any{"status": "pending", "result": nil, "updated_at": "2026-01-01T00:00:00Z"}
	}
	term := newRunDriver(t, fs, ledger)
	in := claimRunLeg(t, ledger, sale, 2500)
	d := payment.TerminalDispatch{IntentID: in.ID, Kind: in.Kind, AmountCents: in.AmountCents,
		RegisterID: in.RegisterID, DeviceID: in.DeviceID, IdempotencyKey: in.IdempotencyKey}
	_, err := term.Dispatch(context.Background(), d)
	if err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	if fs.fake.chargePostCount() != 1 {
		t.Fatalf("the first dispatch posted %d charges, want 1", fs.fake.chargePostCount())
	}
	// the charge settled at Run after the 202: the lookup must find it
	fs.fake.reporting[in.IdempotencyKey] = map[string]any{"order_id": in.IdempotencyKey,
		"trans_id": "TRX-ONCE", "result": "A", "auth_code": "OK01", "card_number": "XXXXXXXXXXXX1111",
		"amount": 2500.0, "resp_text": "APPROVED"}
	second, err := term.Dispatch(context.Background(), d)
	if err != nil {
		t.Fatalf("second dispatch: %v", err)
	}
	if fs.fake.chargePostCount() != 1 {
		t.Fatalf("the re dispatch posted a second charge (%d posts), want the lookup to answer it", fs.fake.chargePostCount())
	}
	if second.ProviderRef != "TRX-ONCE" {
		t.Fatalf("the recovered receipt carries %s, want the found trans id", second.ProviderRef)
	}
	got := waitForIntentState(t, ledger, in.ID, payment.TerminalAuthorized)
	if got.AnswerProviderRef != "TRX-ONCE" {
		t.Fatalf("answer ref %s, want TRX-ONCE", got.AnswerProviderRef)
	}
}

// TestRunTerminalRefundAndVoidGoThroughVoidOrRefund: a refund and a void leg
// are Payments API calls with no device: trans_id is the dispatch's
// reference, the amount is cents on a refund and absent on a void, and the
// answer the driver delivers carries Run's trans id of the reversal.
func TestRunTerminalRefundAndVoidGoThroughVoidOrRefund(t *testing.T) {
	ledger, sale := newTerminalFixture(t)
	fs, term := newRunTerminalFixtureOn(t, ledger)
	for _, tc := range []struct {
		kind   string
		amount int64
	}{
		{payment.TerminalKindRefund, 1200},
		{payment.TerminalKindVoid, 0},
	} {
		in := &payment.TerminalIntent{ID: uuid.New(), TransactionID: sale, LegKey: tc.kind + "-0",
			RegisterID: "REG-" + sale.String()[:12], BranchID: m29BranchOf(t, ledger), AmountCents: tc.amount,
			Kind: tc.kind, IdempotencyKey: tc.kind + "-" + sale.String(), LeaseUntil: time.Now().Add(time.Minute)}
		if err := ledger.Claim(context.Background(), in); err != nil {
			t.Fatalf("claim: %v", err)
		}
		if ok, err := ledger.MarkDispatched(context.Background(), in.ID, "", in.LeaseUntil); err != nil || !ok {
			t.Fatalf("dispatch move: %v %v", ok, err)
		}
		if _, err := term.Dispatch(context.Background(), payment.TerminalDispatch{
			IntentID: in.ID, Kind: tc.kind, AmountCents: tc.amount, RegisterID: in.RegisterID,
			DeviceID: in.DeviceID, IdempotencyKey: in.IdempotencyKey, Reference: "TRX-SALE1"}); err != nil {
			t.Fatalf("%s dispatch: %v", tc.kind, err)
		}
		waitForIntentState(t, ledger, in.ID, payment.TerminalAuthorized)
		posts := fs.fake.voidPosts_()
		if len(posts) == 0 {
			t.Fatalf("the %s never reached void-or-refund", tc.kind)
		}
		body := posts[len(posts)-1]
		if body["trans_id"] != "TRX-SALE1" {
			t.Fatalf("trans id %v, want the dispatch's reference", body["trans_id"])
		}
		if body["user_identifier"] != in.IdempotencyKey {
			t.Fatalf("user identifier %v, want the idempotency key", body["user_identifier"])
		}
		if tc.kind == payment.TerminalKindRefund && body["amount"].(float64) != 1200 {
			t.Fatalf("refund amount %v, want 1200 cents", body["amount"])
		}
	}
}

// TestRunTerminalRefundDecline: a void-or-refund answer of result C declines
// the reversal leg, never authorizes it.
func TestRunTerminalRefundDecline(t *testing.T) {
	ledger, sale := newTerminalFixture(t)
	fs, term := newRunTerminalFixtureOn(t, ledger)
	fs.fake.voidAnswer = func(map[string]any) map[string]any {
		return map[string]any{"result": "C", "trans_id": "", "resp_code": "05", "resp_text": "DECLINE"}
	}
	in := &payment.TerminalIntent{ID: uuid.New(), TransactionID: sale, LegKey: "refund-1",
		RegisterID: "REG-" + sale.String()[:12], BranchID: m29BranchOf(t, ledger), AmountCents: 500,
		Kind: payment.TerminalKindRefund, IdempotencyKey: "refund-" + sale.String(), LeaseUntil: time.Now().Add(time.Minute)}
	if err := ledger.Claim(context.Background(), in); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if ok, err := ledger.MarkDispatched(context.Background(), in.ID, "", in.LeaseUntil); err != nil || !ok {
		t.Fatalf("dispatch move: %v %v", ok, err)
	}
	if _, err := term.Dispatch(context.Background(), payment.TerminalDispatch{
		IntentID: in.ID, Kind: payment.TerminalKindRefund, AmountCents: 500, RegisterID: in.RegisterID,
		DeviceID: in.DeviceID, IdempotencyKey: in.IdempotencyKey, Reference: "TRX-SALE2"}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got := waitForIntentState(t, ledger, in.ID, payment.TerminalDeclined)
	if got.AnswerVerdict != "declined" {
		t.Fatalf("verdict %s, want declined", got.AnswerVerdict)
	}
}

// TestRunTerminalPollAndSweepRace (BRIEF-COMMON, transactions): the poll's
// answer and the sweep resolving the same intent race at pool size 4 with
// three contenders (two answers of the same verdict and the sweep). Whatever
// the interleaving, the intent ends in exactly one final state and a lost
// intent carries exactly one manager item.
func TestRunTerminalPollAndSweepRace(t *testing.T) {
	testutil.LockTerminalTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	ctx := context.Background()
	var branch uuid.UUID
	if err := db.Pool.QueryRow(ctx, `SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id'`).Scan(&branch); err != nil {
		t.Fatalf("default branch: %v", err)
	}
	for i := 0; i < 20; i++ {
		sale := uuid.New()
		if _, err := db.Pool.Exec(ctx, `INSERT INTO pos_transactions
			(id, register_id, cashier_id, subtotal, tax_amount, total, status, branch_id, change_due, revision, currency, number, created_at, updated_at)
			VALUES ($1, $2, $3, 0, 0, 0, 'OPEN', $4, 0, 1, 'USD', $5, NOW(), NOW())`,
			sale, "REG-M29-"+sale.String()[:8], uuid.New(), branch, "POS-"+sale.String()[:8]); err != nil {
			t.Fatalf("sale row: %v", err)
		}
		ledger := payment.NewTerminalLedger(db)
		fs := newRunFake(t)
		fs.fake.pollStep = func(_ string, poll int) (int, map[string]any) {
			if poll < 2 {
				return 200, map[string]any{"status": "pending", "result": nil, "updated_at": "2026-01-01T00:00:00Z"}
			}
			return 200, map[string]any{"status": "succeeded", "result": chargeResult("A", "TRX-RACE"+strconv.Itoa(i), "00", "VISA"), "updated_at": "2026-01-01T00:00:00Z"}
		}
		term := newRunDriver(t, fs, ledger)
		in := &payment.TerminalIntent{ID: uuid.New(), TransactionID: sale, LegKey: "card-0",
			RegisterID: "REG-" + sale.String()[:12], BranchID: branch, AmountCents: 100,
			Kind: payment.TerminalKindSale, IdempotencyKey: "sale-" + sale.String() + "-card-0",
			LeaseUntil: time.Now().Add(40 * time.Millisecond)}
		if err := ledger.Claim(ctx, in); err != nil {
			t.Fatalf("claim: %v", err)
		}
		if ok, err := ledger.MarkDispatched(ctx, in.ID, "", in.LeaseUntil); err != nil || !ok {
			t.Fatalf("dispatch move: %v %v", ok, err)
		}
		if _, err := term.Dispatch(ctx, payment.TerminalDispatch{IntentID: in.ID, Kind: in.Kind,
			AmountCents: in.AmountCents, RegisterID: in.RegisterID, DeviceID: in.DeviceID,
			IdempotencyKey: in.IdempotencyKey}); err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		answer := payment.TerminalAnswer{IntentID: in.ID, ProviderRef: "TRX-RACE" + strconv.Itoa(i),
			Verdict: "authorized", Brand: "VISA", Last4: "4242", AuthCode: "00"}
		start := make(chan struct{})
		var wg sync.WaitGroup
		contender := func(fn func()) {
			wg.Add(1)
			go func() { defer wg.Done(); <-start; fn() }()
		}
		contender(func() { _, _ = ledger.ApplyDeviceAnswer(ctx, answer) })
		contender(func() { _, _ = ledger.ApplyDeviceAnswer(ctx, answer) })
		contender(func() {
			time.Sleep(40 * time.Millisecond)
			_, _ = ledger.Sweep(ctx, time.Now().Add(time.Hour))
		})
		close(start)
		wg.Wait()
		deadline := time.Now().Add(3 * time.Second)
		for {
			got, err := ledger.Get(ctx, in.ID)
			if err != nil {
				t.Fatalf("get intent: %v", err)
			}
			final := got.State == payment.TerminalAuthorized || got.State == payment.TerminalLost
			if final {
				if got.State == payment.TerminalLost {
					item, err := ledger.LostItemOfIntent(ctx, in.ID)
					if err != nil {
						t.Fatalf("lost item read: %v", err)
					}
					if item == nil {
						t.Fatalf("iteration %d: the intent is lost with no manager item", i)
					}
					if item.LateVerdict != "" && item.LateVerdict != "authorized" {
						t.Fatalf("iteration %d: the late verdict is %s", i, item.LateVerdict)
					}
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("iteration %d: the intent never settled, it is %s", i, got.State)
			}
			time.Sleep(5 * time.Millisecond)
		}
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM payment_terminal_lost_items WHERE transaction_id = $1`, sale)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM payment_terminal_intents WHERE transaction_id = $1`, sale)
		_, _ = db.Pool.Exec(context.Background(), `DELETE FROM pos_transactions WHERE id = $1`, sale)
	}
}
