// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/customerquote"
	"github.com/google/uuid"
)

// pngBytes is the smallest valid PNG: a one pixel image.
var pngBytes = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 0x0d, 'I', 'H', 'D', 'R', 0, 0, 0, 1, 0, 0, 0, 1, 8, 6, 0, 0, 0, 0x1f, 0x15, 0xc4, 0x89}

func typed(name string) customerquote.Signature {
	return customerquote.Signature{Kind: "typed", Text: name}
}

func (w *world) acceptAll(token string, sig customerquote.Signature, code string) (*customerquote.PublicView, error) {
	w.t.Helper()
	ctx := context.Background()
	view, err := w.svc.View(ctx, token, anon)
	if err != nil {
		w.t.Fatal(err)
	}
	var choices []customerquote.Choice
	seenGroup := map[uuid.UUID]bool{}
	for _, l := range view.Lines {
		if l.GroupID != nil {
			if seenGroup[*l.GroupID] {
				continue
			}
			seenGroup[*l.GroupID] = true
		}
		choices = append(choices, customerquote.Choice{LineID: l.ID, State: customerquote.SelectionApproved})
	}
	if _, err := w.svc.Choose(ctx, token, anon, choices); err != nil {
		w.t.Fatalf("choose: %v", err)
	}
	return w.svc.Accept(ctx, token, anon, customerquote.AcceptInput{FullName: "Pat Homeowner", Email: "pat@homeowner.example", Signature: sig, Code: code})
}

func TestSelections_ApproveDeclineAndChooseOneOfAGroup(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	in := w.basicInput()
	in.Groups = []customerquote.GroupInput{{Key: "decking", Label: "Decking"}}
	g := "decking"
	in.Lines = append(in.Lines,
		customerquote.LineInput{ProductID: w.ply.String(), Quantity: qty(t, "20"), GroupKey: &g},
		customerquote.LineInput{ProductID: w.nail.String(), Quantity: qty(t, "30"), GroupKey: &g})
	q, token := w.createSent(in)
	// lines: stud, ply, nail standalone; two options in the group
	optA, optB := q.Lines[3].ID, q.Lines[4].ID

	// Not signable until everything is decided.
	_, err := w.svc.Accept(ctx, token, anon, customerquote.AcceptInput{FullName: "Pat Homeowner", Email: "pat@homeowner.example", Signature: typed("Pat Homeowner")})
	if !errors.Is(err, customerquote.ErrIncomplete) {
		t.Fatalf("accept with nothing decided: %v", err)
	}

	view, err := w.svc.Choose(ctx, token, anon, []customerquote.Choice{
		{LineID: q.Lines[0].ID, State: customerquote.SelectionApproved},
		{LineID: q.Lines[1].ID, State: customerquote.SelectionDeclined},
		{LineID: q.Lines[2].ID, State: customerquote.SelectionApproved},
		{LineID: optA, State: customerquote.SelectionApproved},
	})
	if err != nil {
		t.Fatal(err)
	}
	states := map[uuid.UUID]string{}
	for _, l := range view.Lines {
		states[l.ID] = l.Selection
	}
	if states[optA] != "approved" || states[optB] != "declined" || states[q.Lines[1].ID] != "declined" {
		t.Errorf("states %v: approving one option must decline the other", states)
	}
	// Choosing the other option swaps them.
	view, err = w.svc.Choose(ctx, token, anon, []customerquote.Choice{{LineID: optB, State: customerquote.SelectionApproved}})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range view.Lines {
		if l.ID == optA && l.Selection != "declined" {
			t.Errorf("the first option is %s after choosing the second", l.Selection)
		}
		if l.ID == optB && l.Selection != "approved" {
			t.Errorf("the second option is %s", l.Selection)
		}
	}
	// A line from another quote is refused.
	other, _ := w.createSent(w.basicInput())
	if _, err := w.svc.Choose(ctx, token, anon, []customerquote.Choice{{LineID: other.Lines[0].ID, State: customerquote.SelectionApproved}}); err == nil {
		t.Error("a line of another quote was accepted as a selection")
	}
	if _, err := w.svc.Choose(ctx, token, anon, []customerquote.Choice{{LineID: optA, State: "maybe"}}); err == nil {
		t.Error("an unknown state was accepted")
	}

	// The total counts approved lines only: stud 68750 + nail 6000 + option B (30 nails) 45000 vs cost.
	// 30 x $12.00 = $360.00 cost, +25% = 45000.
	res, err := w.svc.Accept(ctx, token, anon, customerquote.AcceptInput{FullName: "Pat Homeowner", Email: "pat@homeowner.example", Signature: typed("Pat Homeowner")})
	if err != nil {
		t.Fatal(err)
	}
	wantMaterials := int64(68750 + 6000 + 45000)
	wantSub := wantMaterials + 104000 + 15000
	if res.Totals.SubtotalCents != wantSub {
		t.Errorf("subtotal %d, want %d", res.Totals.SubtotalCents, wantSub)
	}
}

func TestAccept_RecordsTheSignOffAndAnImmutableSnapshot(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	q, token := w.createSent(w.basicInput())
	sig := customerquote.Signature{Kind: "drawn", PNG: base64.StdEncoding.EncodeToString(pngBytes)}
	res, err := w.acceptAll(token, sig, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "approved" || res.Approval == nil || res.Approval.SignedBy != "Pat Homeowner" {
		t.Fatalf("view after accept: %+v", res.Approval)
	}
	log, err := w.svc.Approvals(ctx, w.a, q.ID)
	if err != nil || len(log) != 1 {
		t.Fatalf("approval log: %d rows, %v", len(log), err)
	}
	e := log[0]
	if e.Step != customerquote.StepCustomerApproved || e.ActorKind != customerquote.ActorCustomer || e.ActorName != "Pat Homeowner" {
		t.Errorf("entry %+v", e)
	}
	if e.SourceAddress != "203.0.113.9" || e.UserAgent != "cq-test" {
		t.Errorf("address %q agent %q", e.SourceAddress, e.UserAgent)
	}
	if e.TokenHash != customerquote.HashToken(token) {
		t.Error("the entry does not carry the link's token hash")
	}
	if e.SignatureKind != "drawn" || e.SignatureData != sig.PNG || e.ActorContact != "pat@homeowner.example" {
		t.Errorf("signature %q contact %q", e.SignatureKind, e.ActorContact)
	}
	sum := sha256.Sum256([]byte(e.Snapshot))
	if hex.EncodeToString(sum[:]) != e.SnapshotSHA256 || e.SnapshotSHA256 == "" {
		t.Error("the stored hash is not the SHA-256 of the stored snapshot bytes")
	}
	if e.TotalCents == nil || *e.TotalCents != 255418 {
		t.Errorf("total %v, want 255418", e.TotalCents)
	}
	for _, want := range []string{`"decision":"approved"`, `"total_cents":255418`, `"Framing crew"`} {
		if !strings.Contains(e.Snapshot, want) {
			t.Errorf("snapshot lacks %s", want)
		}
	}
	// The snapshot holds what the customer saw: no cost, no markup, no contractor only line.
	for _, bad := range []string{"Own travel", "Own insurance", "cost_cents", "markup", "margin", "55000", "12000"} {
		if strings.Contains(e.Snapshot, bad) {
			t.Errorf("snapshot leaks %q", bad)
		}
	}
	// The quote is closed to further decisions.
	if _, err := w.svc.Decline(ctx, token, anon, customerquote.DeclineInput{FullName: "Pat Homeowner"}); !errors.Is(err, customerquote.ErrInvalidState) {
		t.Errorf("declining an approved quote: %v", err)
	}
	if _, err := w.svc.Choose(ctx, token, anon, []customerquote.Choice{{LineID: q.Lines[0].ID, State: customerquote.SelectionDeclined}}); !errors.Is(err, customerquote.ErrInvalidState) {
		t.Errorf("changing a selection after approval: %v", err)
	}
	if _, err := w.svc.Accept(ctx, token, anon, customerquote.AcceptInput{FullName: "Pat Homeowner", Email: "pat@homeowner.example", Signature: typed("Pat Homeowner")}); !errors.Is(err, customerquote.ErrInvalidState) {
		t.Errorf("a second approval: %v", err)
	}
}

func TestAccept_ValidatesTheSignOff(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	q, token := w.createSent(w.basicInput())
	var choices []customerquote.Choice
	for _, l := range q.Lines {
		choices = append(choices, customerquote.Choice{LineID: l.ID, State: customerquote.SelectionApproved})
	}
	if _, err := w.svc.Choose(ctx, token, anon, choices); err != nil {
		t.Fatal(err)
	}
	good := customerquote.AcceptInput{FullName: "Pat Homeowner", Email: "pat@homeowner.example", Signature: typed("Pat Homeowner")}
	bad := map[string]func(*customerquote.AcceptInput){
		"no name":         func(a *customerquote.AcceptInput) { a.FullName = "" },
		"one letter name": func(a *customerquote.AcceptInput) { a.FullName = "P" },
		"no contact":      func(a *customerquote.AcceptInput) { a.Email, a.Phone = "", "" },
		"bad email":       func(a *customerquote.AcceptInput) { a.Email = "not-an-email" },
		"bad phone":       func(a *customerquote.AcceptInput) { a.Email, a.Phone = "", "12" },
		"no signature":    func(a *customerquote.AcceptInput) { a.Signature = customerquote.Signature{} },
		"empty typed":     func(a *customerquote.AcceptInput) { a.Signature = typed("") },
		"drawn not a png": func(a *customerquote.AcceptInput) {
			a.Signature = customerquote.Signature{Kind: "drawn", PNG: base64.StdEncoding.EncodeToString([]byte("GIF89a...."))}
		},
		"drawn not base64": func(a *customerquote.AcceptInput) { a.Signature = customerquote.Signature{Kind: "drawn", PNG: "!!!"} },
		"drawn too big": func(a *customerquote.AcceptInput) {
			big := append(append([]byte{}, pngBytes...), make([]byte, 200*1024)...)
			a.Signature = customerquote.Signature{Kind: "drawn", PNG: base64.StdEncoding.EncodeToString(big)}
		},
	}
	for name, mut := range bad {
		in := good
		mut(&in)
		if _, err := w.svc.Accept(ctx, token, anon, in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// None of the refusals closed the quote.
	if _, err := w.svc.Accept(ctx, token, anon, good); err != nil {
		t.Fatalf("the valid sign off: %v", err)
	}
}

func TestDecline_RecordsTheDecisionAndTheSnapshot(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	q, token := w.createSent(w.basicInput())
	res, err := w.svc.Decline(ctx, token, anon, customerquote.DeclineInput{FullName: "Pat Homeowner", Reason: "Too much"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "declined" || res.Approval == nil || res.Approval.Decision != "declined" {
		t.Fatalf("view %+v", res.Approval)
	}
	log, _ := w.svc.Approvals(ctx, w.a, q.ID)
	if len(log) != 1 || log[0].Step != customerquote.StepCustomerDeclined || log[0].Note != "Too much" || log[0].SnapshotSHA256 == "" {
		t.Errorf("log %+v", log)
	}
	if _, err := w.svc.Decline(ctx, token, anon, customerquote.DeclineInput{FullName: "Pat Homeowner"}); !errors.Is(err, customerquote.ErrInvalidState) {
		t.Errorf("declining twice: %v", err)
	}
	// The contractor can revise a declined quote; the old decision stays on record.
	rev, err := w.svc.Revise(ctx, w.a, q.ID)
	if err != nil || rev.Status != customerquote.StatusDraft {
		t.Fatalf("revise: %v %v", rev, err)
	}
	if _, err := w.svc.View(ctx, token, anon); !errors.Is(err, customerquote.ErrLinkUnknown) {
		t.Errorf("the old link after revising: %v", err)
	}
	log, _ = w.svc.Approvals(ctx, w.a, q.ID)
	if len(log) != 1 {
		t.Errorf("the record lost entries: %d", len(log))
	}
}

func TestCode_GatesAcceptanceAndIsHashedSingleUseShortLivedAndAttemptLimited(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	in := w.basicInput()
	req := true
	in.RequireCode = &req
	q, token := w.createSent(in)
	var choices []customerquote.Choice
	for _, l := range q.Lines {
		choices = append(choices, customerquote.Choice{LineID: l.ID, State: customerquote.SelectionApproved})
	}
	if _, err := w.svc.Choose(ctx, token, anon, choices); err != nil {
		t.Fatal(err)
	}
	accept := func(code string) error {
		_, err := w.svc.Accept(ctx, token, anon, customerquote.AcceptInput{FullName: "Pat Homeowner", Email: "pat@homeowner.example", Signature: typed("Pat Homeowner"), Code: code})
		return err
	}

	// Refused without a code, and with no code issued.
	if err := accept(""); !errors.Is(err, customerquote.ErrCodeRequired) {
		t.Fatalf("no code: %v", err)
	}
	if err := accept("123456"); !errors.Is(err, customerquote.ErrCodeRequired) {
		t.Fatalf("a code that was never issued: %v", err)
	}

	// A code goes to the contact on the quote; only its hash is stored.
	if err := w.svc.RequestCode(ctx, token, anon, "email"); err != nil {
		t.Fatal(err)
	}
	code := w.mail.lastCode()
	if len(code) != 6 {
		t.Fatalf("code %q", code)
	}
	var stored, salt string
	if err := w.db.Pool.QueryRow(ctx, `SELECT code_hash, salt FROM pro_portal_verification_codes WHERE quote_id = $1`, q.ID).Scan(&stored, &salt); err != nil {
		t.Fatal(err)
	}
	if stored != customerquote.HashCode(salt, q.ID.String(), code) || strings.Contains(stored, code) {
		t.Error("the code is not stored as a salted hash")
	}
	var n int
	_ = w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM pro_portal_verification_codes WHERE salt = $1 OR destination = $1 OR code_hash = $1`, code).Scan(&n)
	if n != 0 {
		t.Error("the raw code is stored")
	}
	if strings.Contains(w.logs.String(), code) {
		t.Error("the code reached the log")
	}

	// Wrong guesses are counted and limited.
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	for i := 0; i < customerquote.CodeMaxAttempts; i++ {
		if err := accept(wrong); !errors.Is(err, customerquote.ErrCodeWrong) {
			t.Fatalf("wrong guess %d: %v", i+1, err)
		}
	}
	if err := accept(code); !errors.Is(err, customerquote.ErrCodeLocked) {
		t.Errorf("the right code after the attempt limit: %v", err)
	}
	var attempts int
	_ = w.db.Pool.QueryRow(ctx, `SELECT attempts FROM pro_portal_verification_codes WHERE quote_id = $1`, q.ID).Scan(&attempts)
	if attempts != customerquote.CodeMaxAttempts {
		t.Errorf("attempts %d", attempts)
	}

	// A fresh code works once, and only once.
	if err := w.svc.RequestCode(ctx, token, anon, "email"); err != nil {
		t.Fatal(err)
	}
	fresh := w.mail.lastCode()
	if err := accept(fresh); err != nil {
		t.Fatalf("the fresh code: %v", err)
	}
	log, _ := w.svc.Approvals(ctx, w.a, q.ID)
	if len(log) != 1 || !log[0].CodeVerified {
		t.Errorf("the record does not say the code was verified: %+v", log)
	}
	var consumed *time.Time
	_ = w.db.Pool.QueryRow(ctx, `SELECT consumed_at FROM pro_portal_verification_codes WHERE quote_id = $1 ORDER BY created_at DESC LIMIT 1`, q.ID).Scan(&consumed)
	if consumed == nil {
		t.Error("the code was not marked used")
	}
}

func TestCode_ExpiresAndIsSingleUse(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	in := w.basicInput()
	req := true
	in.RequireCode = &req
	q, token := w.createSent(in)
	var choices []customerquote.Choice
	for _, l := range q.Lines {
		choices = append(choices, customerquote.Choice{LineID: l.ID, State: customerquote.SelectionApproved})
	}
	_, _ = w.svc.Choose(ctx, token, anon, choices)
	accept := func(code string) error {
		_, err := w.svc.Accept(ctx, token, anon, customerquote.AcceptInput{FullName: "Pat Homeowner", Email: "pat@homeowner.example", Signature: typed("Pat Homeowner"), Code: code})
		return err
	}
	if err := w.svc.RequestCode(ctx, token, anon, "sms"); err != nil {
		t.Fatal(err)
	}
	code := w.mail.lastCode()
	w.advance(customerquote.CodeTTL)
	if err := accept(code); !errors.Is(err, customerquote.ErrCodeExpired) {
		t.Fatalf("an expired code: %v", err)
	}
	// Single use: a consumed code is not good for a second acceptance (here the first is refused for
	// a missing decision, which does not burn the code).
	w.advance(-customerquote.CodeTTL)
	if err := w.svc.RequestCode(ctx, token, anon, "email"); err != nil {
		t.Fatal(err)
	}
	code = w.mail.lastCode()
	if err := accept(code); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM pro_portal_verification_codes WHERE quote_id = $1 AND consumed_at IS NOT NULL`, q.ID).Scan(&n)
	if n != 1 {
		t.Errorf("%d consumed codes", n)
	}
}

func TestCode_RequestsAreLimitedAndGoOnlyToTheContactOnFile(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	in := w.basicInput()
	req := true
	in.RequireCode = &req
	in.EndCustomer.Phone = ""
	_, token := w.createSent(in)
	if err := w.svc.RequestCode(ctx, token, anon, "sms"); err == nil {
		t.Error("a code was sent by sms to a quote with no phone")
	}
	if err := w.svc.RequestCode(ctx, token, anon, "pigeon"); err == nil {
		t.Error("an unknown channel was accepted")
	}
	for i := 0; i < 3; i++ {
		if err := w.svc.RequestCode(ctx, token, anon, "email"); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	if err := w.svc.RequestCode(ctx, token, anon, "email"); !errors.Is(err, customerquote.ErrRateLimited) {
		t.Errorf("the fourth code request: %v", err)
	}
	// A quote that does not use a code refuses to send one.
	_, token2 := w.createSent(w.basicInput())
	if err := w.svc.RequestCode(ctx, token2, anon, "email"); err == nil {
		t.Error("a code was requested for a quote that does not use one")
	}
	// A failed delivery is reported, not swallowed.
	w.mail.fail = true
	in2 := w.basicInput()
	in2.RequireCode = &req
	_, token3 := w.createSent(in2)
	if err := w.svc.RequestCode(ctx, token3, anon, "email"); !errors.Is(err, customerquote.ErrDeliveryFailed) {
		t.Errorf("a failed delivery: %v", err)
	}
}

func TestCode_LifetimeBudgetRefusesAcrossWindows(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	in := w.basicInput()
	req := true
	in.RequireCode = &req
	q, token := w.createSent(in)
	var choices []customerquote.Choice
	for _, l := range q.Lines {
		choices = append(choices, customerquote.Choice{LineID: l.ID, State: customerquote.SelectionApproved})
	}
	if _, err := w.svc.Choose(ctx, token, anon, choices); err != nil {
		t.Fatal(err)
	}
	accept := func(tok, code string) error {
		_, err := w.svc.Accept(ctx, tok, anon, customerquote.AcceptInput{FullName: "Pat Homeowner", Email: "pat@homeowner.example", Signature: typed("Pat Homeowner"), Code: code})
		return err
	}

	// Across windows, 3 codes x 5 wrong guesses = 15 lifetime wrong attempts exhaust the link.
	for c := 0; c < 3; c++ {
		if err := w.svc.RequestCode(ctx, token, anon, "email"); err != nil {
			t.Fatalf("code %d: %v", c+1, err)
		}
		wrong := "000000"
		if w.mail.lastCode() == wrong {
			wrong = "111111"
		}
		for a := 0; a < customerquote.CodeMaxAttempts; a++ {
			if err := accept(token, wrong); !errors.Is(err, customerquote.ErrCodeWrong) {
				t.Fatalf("code %d guess %d: %v", c+1, a+1, err)
			}
		}
		w.advance(11 * time.Minute)
	}
	if err := w.svc.RequestCode(ctx, token, anon, "email"); !errors.Is(err, customerquote.ErrCodeBudgetExhausted) {
		t.Fatalf("request after 15 wrong guesses across windows: %v, want ErrCodeBudgetExhausted", err)
	}
	if err := accept(token, "000000"); !errors.Is(err, customerquote.ErrCodeBudgetExhausted) {
		t.Fatalf("guess after 15 wrong guesses: %v, want ErrCodeBudgetExhausted", err)
	}

	// Rotating the link issues a fresh token with its own budget.
	rot, err := w.svc.Rotate(ctx, w.a, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	freshToken := strings.TrimPrefix(rot.Path, "/q/")
	// Request 10 codes across four windows: the 10th succeeds, the 11th is refused.
	for win := 0; win < 4; win++ {
		wantCodes := 3
		if win == 3 {
			wantCodes = 1
		}
		for i := 0; i < wantCodes; i++ {
			if err := w.svc.RequestCode(ctx, freshToken, anon, "email"); err != nil {
				t.Fatalf("window %d code %d: %v", win+1, i+1, err)
			}
		}
		if win < 3 {
			w.advance(11 * time.Minute)
		}
	}
	if err := w.svc.RequestCode(ctx, freshToken, anon, "email"); !errors.Is(err, customerquote.ErrCodeBudgetExhausted) {
		t.Fatalf("11th code request on link: %v, want ErrCodeBudgetExhausted", err)
	}
	// The 10th code itself can still be verified while its attempts remain.
	if err := accept(freshToken, w.mail.lastCode()); err != nil {
		t.Fatalf("accepting with the 10th code: %v", err)
	}
}

func TestConfirm_PlacesTheOrderForTheApprovedMaterialAndRecordsIt(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	in := w.basicInput()
	q, token := w.createSent(in)
	if _, err := w.svc.Confirm(ctx, w.a, q.ID); !errors.Is(err, customerquote.ErrInvalidState) {
		t.Fatalf("confirming before the customer approves: %v", err)
	}
	// Approve the studs and nails, decline the plywood.
	if _, err := w.svc.Choose(ctx, token, anon, []customerquote.Choice{
		{LineID: q.Lines[0].ID, State: customerquote.SelectionApproved},
		{LineID: q.Lines[1].ID, State: customerquote.SelectionDeclined},
		{LineID: q.Lines[2].ID, State: customerquote.SelectionApproved},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Accept(ctx, token, anon, customerquote.AcceptInput{FullName: "Pat Homeowner", Email: "pat@homeowner.example", Signature: typed("Pat Homeowner")}); err != nil {
		t.Fatal(err)
	}
	done, err := w.svc.Confirm(ctx, w.a, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != customerquote.StatusConfirmed || done.OrderID == nil || done.ConfirmedAt == nil {
		t.Fatalf("confirmed quote %+v", done)
	}
	if w.orders.calls != 1 || len(w.orders.last.Lines) != 2 || w.orders.last.ProjectID != w.projA || w.orders.last.CustomerID != w.a.CustomerID {
		t.Fatalf("order placement %+v after %d calls", w.orders.last, w.orders.calls)
	}
	for _, l := range w.orders.last.Lines {
		if l.ProductID == w.ply {
			t.Error("the declined plywood was ordered")
		}
	}
	if w.orders.last.Reference != done.Reference {
		t.Errorf("the order does not carry the quote reference: %q", w.orders.last.Reference)
	}
	log, _ := w.svc.Approvals(ctx, w.a, q.ID)
	if len(log) != 2 || log[1].Step != customerquote.StepContractorConfirmed || log[1].ActorKind != customerquote.ActorContractor || log[1].ActorName != w.a.Name {
		t.Errorf("record %+v", log)
	}
	if _, err := w.svc.Confirm(ctx, w.a, q.ID); !errors.Is(err, customerquote.ErrInvalidState) {
		t.Errorf("confirming twice: %v", err)
	}
	// The customer's link shows the fulfilment state of the order, and no more.
	view, err := w.svc.View(ctx, token, anon)
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != "confirmed" || view.Fulfilment == nil || *view.Fulfilment != customerquote.FulfilmentOrdered {
		t.Errorf("customer view: status %s fulfilment %v", view.Status, view.Fulfilment)
	}
	if _, err := w.db.Pool.Exec(ctx, `UPDATE orders SET status = 'FULFILLED' WHERE id = $1`, *done.OrderID); err != nil {
		t.Fatal(err)
	}
	view, _ = w.svc.View(ctx, token, anon)
	if view.Fulfilment == nil || *view.Fulfilment != customerquote.FulfilmentFulfilled {
		t.Errorf("fulfilment after the order is fulfilled: %v", view.Fulfilment)
	}
}

func TestDealer_SeesTheSharedProjectAndAcceptsOnce(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	draft, err := w.svc.Create(ctx, w.a, w.basicInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.DealerGet(ctx, draft.ID); !errors.Is(err, customerquote.ErrNotFound) {
		t.Errorf("a draft is the contractor's own: %v", err)
	}
	q, token := w.createSent(w.basicInput())
	d, err := w.svc.DealerGet(ctx, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Project == nil || d.Project.Name != "Maple Street Deck" || d.JobSiteAddress != "12 Maple Street" || d.Customer.Name != "Pat Homeowner" ||
		d.SiteContact.Name != "Sam Foreman" || d.Selections.Pending != 3 || d.Contractor.Name != "Quote Contractor A" {
		t.Errorf("shared fields: %+v", d)
	}
	if len(d.Materials) != 3 || d.Materials[0].DealerCostCents != 55000 {
		t.Errorf("materials at dealer prices: %+v", d.Materials)
	}
	if _, err := w.svc.DealerAccept(ctx, customerquote.DealerActor{Ref: "desk-1", Name: "Desk User"}, q.ID); !errors.Is(err, customerquote.ErrInvalidState) {
		t.Errorf("accepting before confirmation: %v", err)
	}
	if _, err := w.acceptAll(token, typed("Pat Homeowner"), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Confirm(ctx, w.a, q.ID); err != nil {
		t.Fatal(err)
	}
	out, err := w.svc.DealerAccept(ctx, customerquote.DealerActor{Ref: "desk-1", Name: "Desk User"}, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.DealerAcceptedAt == nil || out.Order == nil || len(out.Approvals) != 3 {
		t.Fatalf("after acceptance: %+v", out)
	}
	steps := []string{out.Approvals[0].Step, out.Approvals[1].Step, out.Approvals[2].Step}
	if strings.Join(steps, ",") != "customer_approved,contractor_confirmed,dealer_accepted" {
		t.Errorf("steps %v", steps)
	}
	if _, err := w.svc.DealerAccept(ctx, customerquote.DealerActor{Ref: "desk-2", Name: "Another"}, q.ID); !errors.Is(err, customerquote.ErrInvalidState) {
		t.Errorf("accepting twice: %v", err)
	}
	list, _, err := w.svc.DealerList(ctx, 50, nil, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range list {
		if p.ID == q.ID {
			found = true
		}
		if p.ID == draft.ID {
			t.Error("a draft is in the dealer's list")
		}
	}
	if !found {
		t.Error("the sent quote is not in the dealer's list")
	}
}

func TestConcurrency_TwoAcceptancesOfOneLinkGiveOneApproval(t *testing.T) {
	w := newWorldPool(t, 4)
	ctx := context.Background()
	q, token := w.createSent(w.basicInput())
	var choices []customerquote.Choice
	for _, l := range q.Lines {
		choices = append(choices, customerquote.Choice{LineID: l.ID, State: customerquote.SelectionApproved})
	}
	if _, err := w.svc.Choose(ctx, token, anon, choices); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 1; round++ {
		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, 3)
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, errs[i] = w.svc.Accept(ctx, token, customerquote.Caller{Address: "203.0.113.9"}, customerquote.AcceptInput{
					FullName: "Pat Homeowner", Email: "pat@homeowner.example", Signature: typed("Pat Homeowner")})
			}()
		}
		close(start)
		wg.Wait()
		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case errors.Is(err, customerquote.ErrInvalidState):
			default:
				t.Errorf("a contender failed with %v", err)
			}
		}
		if ok != 1 {
			t.Fatalf("%d acceptances succeeded, want exactly 1 (%v)", ok, errs)
		}
	}
	var n int
	if err := w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM pro_customer_quote_approval_log WHERE quote_id = $1 AND step = 'customer_approved'`, q.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d approval rows, %v", n, err)
	}
}

func TestConcurrency_AcceptAndDeclineRaceGivesOneDecision(t *testing.T) {
	w := newWorldPool(t, 4)
	ctx := context.Background()
	q, token := w.createSent(w.basicInput())
	var choices []customerquote.Choice
	for _, l := range q.Lines {
		choices = append(choices, customerquote.Choice{LineID: l.ID, State: customerquote.SelectionApproved})
	}
	_, _ = w.svc.Choose(ctx, token, anon, choices)
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if i == 0 {
				_, errs[i] = w.svc.Decline(ctx, token, anon, customerquote.DeclineInput{FullName: "Pat Homeowner"})
				return
			}
			_, errs[i] = w.svc.Accept(ctx, token, anon, customerquote.AcceptInput{FullName: "Pat Homeowner", Email: "pat@homeowner.example", Signature: typed("Pat Homeowner")})
		}()
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	var n int
	_ = w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM pro_customer_quote_approval_log WHERE quote_id = $1`, q.ID).Scan(&n)
	if ok != 1 || n != 1 {
		t.Fatalf("%d decisions succeeded and %d rows were logged, want 1 and 1 (%v)", ok, n, errs)
	}
}

func TestConcurrency_ThreeConfirmationsPlaceOneOrder(t *testing.T) {
	w := newWorldPool(t, 4)
	ctx := context.Background()
	q, token := w.createSent(w.basicInput())
	if _, err := w.acceptAll(token, typed("Pat Homeowner"), ""); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = w.svc.Confirm(ctx, w.a, q.ID)
		}()
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	var orders int
	_ = w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE project_id = $1`, w.projA).Scan(&orders)
	if ok != 1 || orders != 1 {
		t.Fatalf("%d confirmations succeeded and %d orders exist, want 1 and 1 (%v)", ok, orders, errs)
	}
}

func TestConcurrency_ThreeSendsIssueOneLink(t *testing.T) {
	w := newWorldPool(t, 4)
	ctx := context.Background()
	q, err := w.svc.Create(ctx, w.a, w.basicInput())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = w.svc.Send(ctx, w.a, q.ID, "")
		}()
	}
	close(start)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	var links int
	_ = w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM pro_portal_share_tokens WHERE quote_id = $1`, q.ID).Scan(&links)
	if ok != 1 || links != 1 {
		t.Fatalf("%d sends succeeded and %d links exist, want 1 and 1 (%v)", ok, links, errs)
	}
}

func TestRevise_ThenResendAllowsANewDecisionUnderANewRevision(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	q, token := w.createSent(w.basicInput())
	if _, err := w.svc.Decline(ctx, token, anon, customerquote.DeclineInput{FullName: "Pat Homeowner", Reason: "price"}); err != nil {
		t.Fatal(err)
	}
	draft, err := w.svc.Revise(ctx, w.a, q.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The contractor lowers the markup and sends again.
	in := w.basicInput()
	in.Markup = &customerquote.Markup{Kind: customerquote.KindMarkup, BPS: 1000}
	updated, err := w.svc.Update(ctx, w.a, q.ID, `"`+itoa(draft.Revision)+`"`, in)
	if err != nil {
		t.Fatal(err)
	}
	res, err := w.svc.Send(ctx, w.a, q.ID, `"`+itoa(updated.Revision)+`"`)
	if err != nil {
		t.Fatal(err)
	}
	newToken := strings.TrimPrefix(res.Path, "/q/")
	if newToken == token {
		t.Fatal("the revised quote went out on the old link")
	}
	if _, err := w.acceptAll(newToken, typed("Pat Homeowner"), ""); err != nil {
		t.Fatalf("the second decision: %v", err)
	}
	log, _ := w.svc.Approvals(ctx, w.a, q.ID)
	if len(log) != 2 || log[0].Step != customerquote.StepCustomerDeclined || log[1].Step != customerquote.StepCustomerApproved || log[0].QuoteRevision >= log[1].QuoteRevision {
		t.Fatalf("the record: %+v", log)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestWithdraw_KillsTheLinkAndTheQuoteStopsBeingShared(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	q, token := w.createSent(w.basicInput())
	out, err := w.svc.Withdraw(ctx, w.a, q.ID)
	if err != nil || out.Status != customerquote.StatusWithdrawn {
		t.Fatalf("withdraw: %v %v", out, err)
	}
	if _, err := w.svc.View(ctx, token, anon); !errors.Is(err, customerquote.ErrLinkUnknown) {
		t.Errorf("a withdrawn quote's link: %v", err)
	}
	if _, err := w.svc.Withdraw(ctx, w.a, q.ID); !errors.Is(err, customerquote.ErrInvalidState) {
		t.Errorf("withdrawing twice: %v", err)
	}
	if _, err := w.svc.Rotate(ctx, w.a, q.ID); !errors.Is(err, customerquote.ErrInvalidState) {
		t.Errorf("rotating a withdrawn quote: %v", err)
	}
}

func TestSettings_AreTheDefaultsOfANewQuoteAndAreValidated(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	st, err := w.svc.PutSettings(ctx, w.a, customerquote.SettingsInput{DisplayName: "A and Sons Building", PaymentTerms: "Due on receipt", DepositBPS: 1000,
		Tax: customerquote.Tax{Mode: customerquote.TaxIncluded}, Markup: customerquote.Markup{Kind: customerquote.KindMargin, BPS: 3000}, RequireCode: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.DisplayName != "A and Sons Building" || !st.RequireCode || st.Markup.Kind != customerquote.KindMargin {
		t.Fatalf("settings %+v", st)
	}
	// A quote that names none of them takes them.
	in := w.basicInput()
	in.Markup, in.Tax, in.PaymentTerms, in.DepositBPS = nil, nil, nil, nil
	q, err := w.svc.Create(ctx, w.a, in)
	if err != nil {
		t.Fatal(err)
	}
	if q.Markup.Kind != customerquote.KindMargin || q.Markup.BPS != 3000 || q.Tax.Mode != customerquote.TaxIncluded ||
		q.PaymentTerms != "Due on receipt" || q.DepositBPS != 1000 || !q.RequireCode {
		t.Errorf("defaults not applied: %+v", q)
	}
	// 30 percent margin on the studs: 55000 / 0.7.
	if q.Lines[0].SellCents != 78571 {
		t.Errorf("stud sell %d", q.Lines[0].SellCents)
	}
	for name, bad := range map[string]customerquote.SettingsInput{
		"margin 100":     {Tax: customerquote.Tax{Mode: customerquote.TaxAtCheckout}, Markup: customerquote.Markup{Kind: customerquote.KindMargin, BPS: 10000}},
		"unknown tax":    {Tax: customerquote.Tax{Mode: "vat"}, Markup: customerquote.Markup{Kind: customerquote.KindMarkup}},
		"deposit 101":    {DepositBPS: 10001, Tax: customerquote.Tax{Mode: customerquote.TaxAtCheckout}, Markup: customerquote.Markup{Kind: customerquote.KindMarkup}},
		"rate in at-co":  {Tax: customerquote.Tax{Mode: customerquote.TaxAtCheckout, RateBPS: 5}, Markup: customerquote.Markup{Kind: customerquote.KindMarkup}},
		"rate over 50pc": {Tax: customerquote.Tax{Mode: customerquote.TaxSiteRate, RateBPS: 5001}, Markup: customerquote.Markup{Kind: customerquote.KindMarkup}},
	} {
		if _, err := w.svc.PutSettings(ctx, w.a, bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// Another contractor's settings are their own.
	other, err := w.svc.GetSettings(ctx, w.b)
	if err != nil || other.DisplayName != "" || other.RequireCode {
		t.Errorf("another contractor's settings: %+v %v", other, err)
	}
}

func TestExport_CSVDefusesSpreadsheetFormulas(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	in := w.basicInput()
	in.Title = "=HYPERLINK(\"http://x.example\",\"click\")"
	in.EndCustomer.Name = "+1 Name"
	if _, err := w.svc.Create(ctx, w.a, in); err != nil {
		t.Fatal(err)
	}
	ex, err := w.svc.Export(ctx, w.a)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"quotes", "customers", "projects"} {
		var sb strings.Builder
		if err := ex.WriteCSV(&sb, kind); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(sb.String(), "\n") {
			for _, cell := range strings.Split(line, ",") {
				cell = strings.Trim(cell, `"`)
				if cell != "" && strings.ContainsRune("=+-@", rune(cell[0])) && !strings.HasPrefix(cell, "-") {
					t.Errorf("%s: a formula cell survives: %q", kind, cell)
				}
			}
		}
	}
	var sb strings.Builder
	_ = ex.WriteCSV(&sb, "quotes")
	if !strings.Contains(sb.String(), `'=HYPERLINK`) {
		t.Errorf("the title was not defused: %s", sb.String())
	}
	if err := ex.WriteCSV(&sb, "nope"); err == nil {
		t.Error("an unknown kind was accepted")
	}
}
