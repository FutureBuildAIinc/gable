// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package account_test

// Every act that takes a day refuses a day that has not come in the branch's
// time zone, in words that name the field (G1 walk note N12).

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/gl"
	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// draftMemo inserts a draft credit memo of the world's customer.
func (w *arWorld) draftMemo() uuid.UUID {
	memo := uuid.New()
	w.exec(`INSERT INTO credit_memos (id, customer_id, branch_id, currency, reason_code, reason, amount, subtotal, tax_amount,
			total_amount, status, memo_date)
		VALUES ($1, $2, `+w.branchSQL()+`, 'USD', 'RETURN', 'returned goods', 0, 0, 0, 0, 'DRAFT', CURRENT_DATE)`, memo, w.customerID)
	return memo
}

func (w *arWorld) postMemo(memo uuid.UUID, cents int64) {
	w.t.Helper()
	w.mustAct("post the memo", func(ctx context.Context) (*account.Effects, error) {
		return w.svc.PostCreditMemo(ctx, account.PostCreditMemoIn{MemoID: memo, CustomerID: w.customerID, Number: "CM-" + memo.String()[:8],
			Currency: "USD", MemoDate: daysFromNow(-10), SubtotalCents: -cents, TotalCents: -cents, Actor: "u-test",
			Legs: []gl.Leg{{AccountCode: "4010", Description: "Sales Revenue", Debit: cents}}})
	})
}

func TestARRefusesADayToComeOnEveryActThatTakesOne(t *testing.T) {
	type tc struct {
		name  string
		field string
		run   func(w *arWorld) error // sets up in the past, then acts on tomorrow
	}
	cases := []tc{
		{"record a payment", "received_on", func(w *arWorld) error {
			_, err := w.act("record", func(ctx context.Context) (*account.Effects, error) {
				_, fx, e := w.svc.RecordPayment(ctx, account.RecordPaymentIn{CustomerID: w.customerID, BranchID: w.branchID,
					Currency: "USD", Method: "CHECK", AmountCents: 5000, ReceivedOn: w.businessDay().AddDate(0, 0, 1), Actor: "u-test"})
				return fx, e
			})
			return err
		}},
		{"apply a payment", "applied_on", func(w *arWorld) error {
			inv := w.invoice(10000, daysFromNow(-5))
			pay := w.payment(10000, daysFromNow(-1), "CHECK")
			_, err := w.act("apply", func(ctx context.Context) (*account.Effects, error) {
				return w.svc.Apply(ctx, account.ApplyIn{PaymentID: pay, Lines: []account.ApplyLine{{InvoiceID: inv.ID, AmountCents: 10000}},
					On: w.businessDay().AddDate(0, 0, 1), Actor: "u-test"})
			})
			return err
		}},
		{"apply deposits", "applied_on", func(w *arWorld) error {
			inv := w.invoice(10000, daysFromNow(-5))
			pay := w.payment(10000, daysFromNow(-1), "CHECK")
			_, err := w.act("deposits", func(ctx context.Context) (*account.Effects, error) {
				return w.svc.ApplyDeposits(ctx, inv.ID, []uuid.UUID{pay}, w.businessDay().AddDate(0, 0, 1), "u-test")
			})
			return err
		}},
		{"apply a credit memo", "applied_on", func(w *arWorld) error {
			inv := w.invoice(10000, daysFromNow(-30))
			memo := w.draftMemo()
			w.postMemo(memo, 5000)
			_, err := w.act("credit apply", func(ctx context.Context) (*account.Effects, error) {
				return w.svc.ApplyCreditMemo(ctx, account.ApplyCreditMemoIn{MemoID: memo,
					Lines: []account.ApplyLine{{InvoiceID: inv.ID, AmountCents: 3000}}, On: w.businessDay().AddDate(0, 0, 1), Actor: "u-test"})
			})
			return err
		}},
		{"write off", "on", func(w *arWorld) error {
			inv := w.invoice(10000, daysFromNow(-30))
			_, err := w.act("write off", func(ctx context.Context) (*account.Effects, error) {
				return w.svc.WriteOff(ctx, account.WriteOffIn{InvoiceID: inv.ID, AmountCents: 1000, Reason: "small", Actor: "u-test",
					On: w.businessDay().AddDate(0, 0, 1)})
			})
			return err
		}},
		{"refund a payment", "refunded_on", func(w *arWorld) error {
			pay := w.payment(4000, daysFromNow(-1), "CHECK")
			_, err := w.act("refund", func(ctx context.Context) (*account.Effects, error) {
				_, fx, e := w.svc.RefundPayment(ctx, account.RefundPaymentIn{PaymentID: pay, AmountCents: 1000, Reason: "back", Actor: "u-test",
					On: w.businessDay().AddDate(0, 0, 1)})
				return fx, e
			})
			return err
		}},
		{"refund a credit memo", "refunded_on", func(w *arWorld) error {
			memo := w.draftMemo()
			w.postMemo(memo, 5000)
			_, err := w.act("refund memo", func(ctx context.Context) (*account.Effects, error) {
				_, fx, e := w.svc.RefundCreditMemo(ctx, account.RefundCreditMemoIn{MemoID: memo, AmountCents: 1000, Reason: "back", Actor: "u-test",
					On: w.businessDay().AddDate(0, 0, 1)})
				return fx, e
			})
			return err
		}},
		{"reverse an application", "reversed_on", func(w *arWorld) error {
			inv := w.invoice(10000, daysFromNow(-5))
			w.payment(10000, daysFromNow(-1), "CHECK", account.ApplyLine{InvoiceID: inv.ID, AmountCents: 10000})
			app := w.appID(inv.ID, "PAYMENT")
			_, err := w.act("reverse", func(ctx context.Context) (*account.Effects, error) {
				return w.svc.Reverse(ctx, account.ReverseIn{ApplicationID: app, Reason: "bounced", Actor: "u-test",
					On: w.businessDay().AddDate(0, 0, 1)})
			})
			return err
		}},
		{"void a payment", "voided_on", func(w *arWorld) error {
			pay := w.payment(4000, daysFromNow(-1), "CHECK")
			_, err := w.act("void", func(ctx context.Context) (*account.Effects, error) {
				return w.svc.VoidPayment(ctx, account.VoidPaymentIn{PaymentID: pay, Reason: "keyed twice", Actor: "u-test",
					On: w.businessDay().AddDate(0, 0, 1)})
			})
			return err
		}},
		{"post an invoice", "invoice_date", func(w *arWorld) error {
			inv := &arInvoice{ID: uuid.New(), Total: 7000, Due: daysFromNow(30)}
			w.postInvoiceRow(w.customerID, inv, daysFromNow(-1))
			_, err := w.act("post invoice", func(ctx context.Context) (*account.Effects, error) {
				return w.svc.PostInvoice(ctx, account.PostInvoiceIn{InvoiceID: inv.ID, CustomerID: w.customerID,
					Number: "IN-" + inv.ID.String()[:8], Currency: "USD", TotalCents: inv.Total, On: w.businessDay().AddDate(0, 0, 1), Actor: "u-test",
					Legs: []gl.Leg{{AccountCode: "4010", Description: "Sales Revenue", Credit: inv.Total}}})
			})
			return err
		}},
		{"void an invoice", "voided_on", func(w *arWorld) error {
			inv := w.invoice(7000, daysFromNow(-3))
			_, err := w.act("void invoice", func(ctx context.Context) (*account.Effects, error) {
				return w.svc.VoidInvoice(ctx, account.VoidInvoiceIn{InvoiceID: inv.ID, EntryID: entryOf(w, inv.ID), Actor: "u-test",
					Reason: "keyed wrong", On: w.businessDay().AddDate(0, 0, 1)})
			})
			return err
		}},
		{"post a credit memo", "memo_date", func(w *arWorld) error {
			memo := w.draftMemo()
			_, err := w.act("post memo", func(ctx context.Context) (*account.Effects, error) {
				return w.svc.PostCreditMemo(ctx, account.PostCreditMemoIn{MemoID: memo, CustomerID: w.customerID, Number: "CM-" + memo.String()[:8],
					Currency: "USD", MemoDate: w.businessDay().AddDate(0, 0, 1), SubtotalCents: -5000, TotalCents: -5000, Actor: "u-test",
					Legs: []gl.Leg{{AccountCode: "4010", Description: "Sales Revenue", Debit: 5000}}})
			})
			return err
		}},
		{"void a credit memo", "voided_on", func(w *arWorld) error {
			memo := w.draftMemo()
			w.postMemo(memo, 5000)
			_, err := w.act("void memo", func(ctx context.Context) (*account.Effects, error) {
				return w.svc.VoidCreditMemo(ctx, account.VoidCreditMemoIn{MemoID: memo, Actor: "u-test", Reason: "keyed wrong",
					On: w.businessDay().AddDate(0, 0, 1)})
			})
			return err
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newARWorld(t)
			err := c.run(w)
			var he *httpx.Error
			if !errors.As(err, &he) {
				t.Fatalf("a day to come was %v, want a 400 naming %s", err, c.field)
			}
			if he.Status != 400 {
				t.Errorf("status %d, want 400", he.Status)
			}
			if len(he.Details) != 1 || he.Details[0].Field != c.field || !strings.Contains(he.Details[0].Message, "has not come yet") {
				t.Errorf("details %+v, want the field %s and words saying the day has not come yet", he.Details, c.field)
			}
			w.assertInvariants("after the refused act")
		})
	}
}

// A branch whose own day differs from the UTC day: its today is accepted and
// its tomorrow refused, whichever side of UTC the branch sits on.
func TestARBusinessDayIsTheBranchsOwnDay(t *testing.T) {
	for _, zone := range []string{"Pacific/Kiritimati", "Pacific/Pago_Pago"} {
		t.Run(zone, func(t *testing.T) {
			w := newARWorld(t)
			branch := uuid.New()
			w.exec(`INSERT INTO locations (id, type, code, timezone) VALUES ($1, 'BRANCH', $2, $3)`, branch, "ZN-"+branch.String()[:8], zone)
			t.Cleanup(func() {
				// the world's own cleanup removes the payment; the branch goes after
				w.exec(`UPDATE payments SET branch_id = `+w.branchSQL()+` WHERE branch_id = $1`, branch)
				w.exec(`UPDATE gl_journal_lines SET branch_id = `+w.branchSQL()+` WHERE branch_id = $1`, branch)
				w.exec(`UPDATE gl_journal_entries SET branch_id = `+w.branchSQL()+` WHERE branch_id = $1`, branch)
				w.exec(`DELETE FROM locations WHERE id = $1`, branch)
			})
			today, err := w.svc.LocalDate(context.Background(), branch, w.svc.Now())
			if err != nil {
				t.Fatal(err)
			}
			record := func(on string) error {
				_, err := w.act("record", func(ctx context.Context) (*account.Effects, error) {
					d, _ := w.svc.LocalDate(ctx, branch, w.svc.Now())
					if on == "tomorrow" {
						d = d.AddDate(0, 0, 1)
					}
					_, fx, e := w.svc.RecordPayment(ctx, account.RecordPaymentIn{CustomerID: w.customerID, BranchID: branch,
						Currency: "USD", Method: "CHECK", AmountCents: 1000, ReceivedOn: d, Actor: "u-test"})
					return fx, e
				})
				return err
			}
			if err := record("today"); err != nil {
				t.Errorf("the branch's own today (%s) was refused: %v", date(today), err)
			}
			if err := record("tomorrow"); err == nil {
				t.Errorf("the branch's tomorrow was accepted")
			}
		})
	}
}
