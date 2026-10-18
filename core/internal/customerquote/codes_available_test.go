// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package customerquote_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/customerquote"
)

const codesSentence = "Verification codes need a real email or text sender on this server."

func TestCodesUnavailable_TheSentenceIsPlain(t *testing.T) {
	if customerquote.CodesUnavailableMessage != codesSentence {
		t.Fatalf("the sentence is %q", customerquote.CodesUnavailableMessage)
	}
}

func TestCodesUnavailable_TheToggleRefusesToTurnOn(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.mail.setChannels(false, false)
	in := customerquote.SettingsInput{Tax: customerquote.Tax{Mode: customerquote.TaxAtCheckout}, Markup: customerquote.Markup{Kind: customerquote.KindMarkup}, RequireCode: true}
	if _, err := w.svc.PutSettings(ctx, w.a, in); !errors.Is(err, customerquote.ErrCodesUnavailable) {
		t.Fatalf("the contractor toggle: %v", err)
	}
	// Off stays possible.
	in.RequireCode = false
	if _, err := w.svc.PutSettings(ctx, w.a, in); err != nil {
		t.Fatalf("turning it off: %v", err)
	}
	// A quote asking for a code is refused the same way, on create and on update.
	req := true
	q := w.basicInput()
	q.RequireCode = &req
	if _, err := w.svc.Create(ctx, w.a, q); !errors.Is(err, customerquote.ErrCodesUnavailable) {
		t.Fatalf("a quote with the toggle on: %v", err)
	}
	draft, err := w.svc.Create(ctx, w.a, w.basicInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Update(ctx, w.a, draft.ID, `"1"`, q); !errors.Is(err, customerquote.ErrCodesUnavailable) {
		t.Fatalf("an update with the toggle on: %v", err)
	}
	// It turns on with a real text sender or a real email sender.
	w.mail.setChannels(false, true)
	in.RequireCode = true
	if _, err := w.svc.PutSettings(ctx, w.a, in); err != nil {
		t.Fatalf("with a real text sender: %v", err)
	}
	w.mail.setChannels(true, false)
	in.RequireCode = true
	if _, err := w.svc.PutSettings(ctx, w.a, in); err != nil {
		t.Fatalf("with a real email sender: %v", err)
	}
	// ... but not on a text sender that is the stub.
	w.mail.setChannels(false, false)
	if _, err := w.svc.PutSettings(ctx, w.a, in); !errors.Is(err, customerquote.ErrCodesUnavailable) {
		t.Fatalf("with neither: %v", err)
	}
}

func TestCodesUnavailable_AQuoteAlreadyAskingForACodeCannotBeAcceptedOrSentUntilTheToggleIsOff(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	req := true
	in := w.basicInput()
	in.RequireCode = &req
	q, token := w.createSent(in) // with a real sender
	var choices []customerquote.Choice
	for _, l := range q.Lines {
		choices = append(choices, customerquote.Choice{LineID: l.ID, State: customerquote.SelectionApproved})
	}
	if _, err := w.svc.Choose(ctx, token, anon, choices); err != nil {
		t.Fatal(err)
	}

	w.mail.setChannels(false, false) // the server's senders are now the log only one and the stub

	// The public page says so in plain words.
	view, err := w.svc.View(ctx, token, anon)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view.Notice, "cannot be accepted") || !strings.Contains(view.Notice, "turn off") {
		t.Errorf("the page's notice: %q", view.Notice)
	}
	if strings.Contains(view.Notice, "email") && strings.Contains(view.Notice, "@") {
		t.Errorf("the notice names an address: %q", view.Notice)
	}
	// A code request and an acceptance answer the refusal; nothing is stored or sent.
	if err := w.svc.RequestCode(ctx, token, anon, "email"); !errors.Is(err, customerquote.ErrCodesUnavailable) {
		t.Errorf("a code request: %v", err)
	}
	_, err = w.svc.Accept(ctx, token, anon, customerquote.AcceptInput{FullName: "Pat Homeowner", Email: "pat@homeowner.example", Signature: typed("Pat Homeowner"), Code: "123456"})
	if !errors.Is(err, customerquote.ErrCodesUnavailable) {
		t.Errorf("an acceptance: %v", err)
	}
	var n int
	_ = w.db.Pool.QueryRow(ctx, `SELECT count(*) FROM pro_portal_verification_codes WHERE quote_id = $1`, q.ID).Scan(&n)
	if n != 0 || w.mail.lastCode() != "" {
		t.Errorf("a code was minted or sent: %d rows, %q", n, w.mail.lastCode())
	}
	// Declining is still the customer's right.
	if _, err := w.svc.Decline(ctx, token, anon, customerquote.DeclineInput{FullName: "Pat Homeowner"}); err != nil {
		t.Errorf("a decline: %v", err)
	}

	// The contractor revises, turns the toggle off and the quote goes through again.
	if _, err := w.svc.Revise(ctx, w.a, q.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ := w.svc.Get(ctx, w.a, q.ID)
	if _, err := w.svc.Send(ctx, w.a, q.ID, `"`+itoa(cur.Revision)+`"`); !errors.Is(err, customerquote.ErrCodesUnavailable) {
		t.Fatalf("sending a draft that still asks for a code: %v", err)
	}
	off := false
	in2 := w.basicInput()
	in2.RequireCode = &off
	upd, err := w.svc.Update(ctx, w.a, q.ID, `"`+itoa(cur.Revision)+`"`, in2)
	if err != nil {
		t.Fatal(err)
	}
	res, err := w.svc.Send(ctx, w.a, q.ID, `"`+itoa(upd.Revision)+`"`)
	if err != nil {
		t.Fatalf("send with the toggle off: %v", err)
	}
	if _, err := w.acceptAll(strings.TrimPrefix(res.Path, "/q/"), typed("Pat Homeowner"), ""); err != nil {
		t.Fatalf("accepting with the toggle off: %v", err)
	}
}

func TestCodesUnavailable_OnlyTheChannelThatWorksIsOffered(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	req := true
	in := w.basicInput()
	in.RequireCode = &req
	_, token := w.createSent(in)
	w.mail.setChannels(true, false) // email is real, sms is the stub
	if err := w.svc.RequestCode(ctx, token, anon, "sms"); err == nil {
		t.Error("a code was requested by the sms stub")
	}
	if err := w.svc.RequestCode(ctx, token, anon, "email"); err != nil {
		t.Errorf("by email: %v", err)
	}
}
