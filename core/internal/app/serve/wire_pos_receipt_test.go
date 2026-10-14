// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package serve

// The counter's own receipt routes (item 111): a cashier prints and emails the
// receipt of a sale at its own yard without the documents roles, and the sale's
// reach is the sale read's reach.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// recordingMailer is the sender the fixture's document routes send through:
// it records each message and sends nothing.
type recordingMailer struct {
	mu   sync.Mutex
	sent []string
}

func (m *recordingMailer) record(to ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, strings.Join(to, ","))
}

func (m *recordingMailer) SendInvoice(_ context.Context, to string, _ string, _ []byte) error {
	m.record(to)
	return nil
}

func (m *recordingMailer) SendDeliveryNotification(_ context.Context, to, _, _ string) error {
	m.record(to)
	return nil
}

func (m *recordingMailer) SendEmailWithAttachment(_ context.Context, to []string, _, _, _ string, _ []byte) error {
	m.record(to...)
	return nil
}

// recipients is what was sent so far, after waiting up to want messages (the
// send runs after the route has answered).
func (m *recordingMailer) recipients(want int) []string {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		n := len(m.sent)
		m.mu.Unlock()
		if n >= want {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.sent...)
}

// seedReceiptSale copies the fixture's open sale at one branch into a sale
// with the status given, and a posted invoice for the customer when invoiced.
func seedReceiptSale(t *testing.T, f *keyPinFixture, txn uuid.UUID, branch, customer uuid.UUID, status string, invoiced bool) (sale, inv uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	sale, inv = uuid.New(), uuid.New()
	var invArg any
	if invoiced {
		order := uuid.New()
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO orders (id, customer_id, branch_id, status, total_amount, delivery_type, currency)
			 VALUES ($1, $2, $3, 'CONFIRMED', 10, 'PICKUP', 'USD')`, order, customer, branch); err != nil {
			t.Fatalf("seed receipt order: %v", err)
		}
		if _, err := f.db.Pool.Exec(ctx,
			`INSERT INTO invoices (id, order_id, customer_id, status, total_amount, branch_id, currency)
			 VALUES ($1, $2, $3, 'PAID', 10, $4, 'USD')`, inv, order, customer, branch); err != nil {
			t.Fatalf("seed receipt invoice: %v", err)
		}
		invArg = inv
		t.Cleanup(func() {
			_, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE id = $1`, sale)
			_, _ = f.db.Pool.Exec(ctx, `DELETE FROM email_deliveries WHERE document_id = $1`, inv)
			_, _ = f.db.Pool.Exec(ctx, `DELETE FROM invoices WHERE id = $1`, inv)
			_, _ = f.db.Pool.Exec(ctx, `DELETE FROM orders WHERE id = $1`, order)
		})
	} else {
		t.Cleanup(func() { _, _ = f.db.Pool.Exec(ctx, `DELETE FROM pos_transactions WHERE id = $1`, sale) })
	}
	if _, err := f.db.Pool.Exec(ctx,
		`INSERT INTO pos_transactions (id, register_id, cashier_id, status, branch_id, till_session_id, currency, invoice_id)
		 SELECT $1, register_id, cashier_id, $3, branch_id, till_session_id, currency, $4 FROM pos_transactions WHERE id = $2`,
		sale, txn, status, invArg); err != nil {
		t.Fatalf("seed receipt sale: %v", err)
	}
	return sale, inv
}

func receiptCode(t *testing.T, buf []byte) string {
	t.Helper()
	var body struct {
		Error struct {
			Details []struct {
				Code string `json:"code"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(buf, &body); err != nil {
		t.Fatalf("decode %s: %v", buf, err)
	}
	if len(body.Error.Details) == 0 {
		return ""
	}
	return body.Error.Details[0].Code
}

// TestPosReceipt_CashierPrintsAndEmailsTheSalesInvoice is the counter's
// receipt wire: the cashier role alone reaches both routes; the old documents
// routes still refuse it.
func TestPosReceipt_CashierPrintsAndEmailsTheSalesInvoice(t *testing.T) {
	f := newKeyPinFixture(t)
	ctx := context.Background()
	ada := uuid.New()
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO customers (id, name, account_number, email, primary_branch_id)
		VALUES ($1, 'pin receipt cust', $2, 'receipt-contact@example.test', $3)`,
		ada, "PINR-"+ada.String()[:8], f.branchA); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if _, err := f.db.Pool.Exec(ctx, `INSERT INTO customer_branches (customer_id, branch_id) VALUES ($1, $2)`, ada, f.branchA); err != nil {
		t.Fatalf("seed customer branch: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM customer_branches WHERE customer_id = $1`, ada)
		_, _ = f.db.Pool.Exec(ctx, `DELETE FROM customers WHERE id = $1`, ada)
	})
	noheaderGrant(t, f, "pin-cashier", f.branchA)
	hdr := headerA(f)
	sale, inv := seedReceiptSale(t, f, f.txnA, f.branchA, ada, "COMPLETED", true)
	other, _ := seedReceiptSale(t, f, f.txnB, f.branchB, ada, "COMPLETED", true)
	open, _ := seedReceiptSale(t, f, f.txnA, f.branchA, ada, "OPEN", false)
	noContact, _ := seedReceiptSale(t, f, f.txnA, f.branchA, f.custID, "COMPLETED", true)

	t.Run("the cashier prints the sale's invoice", func(t *testing.T) {
		get := func(role, path string) (*http.Response, []byte) {
			req, err := http.NewRequest("GET", f.srv.URL+path, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Test-Role", role)
			req.Header.Set("X-Test-Sub", "pin-"+role)
			for k, v := range hdr {
				req.Header.Set(k, v)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			buf, err := io.ReadAll(res.Body)
			if err != nil {
				t.Fatal(err)
			}
			return res, buf
		}
		res, buf := get("cashier", "/api/v1/pos/transactions/"+sale.String()+"/receipt")
		if res.StatusCode != http.StatusOK {
			t.Fatalf("print: %d %s, want 200", res.StatusCode, buf)
		}
		if !strings.HasPrefix(string(buf), "%PDF") {
			t.Errorf("print body is not the invoice document: %.20q", buf)
		}
		// The renderer stamps a time into the file, so the bytes of two
		// renders differ; the route's headers are the shared function's.
		via, _ := get("admin", "/api/v1/documents/print/invoice/"+inv.String())
		for _, h := range []string{"Content-Type", "Content-Disposition"} {
			if res.Header.Get(h) != via.Header.Get(h) {
				t.Errorf("%s: receipt %q, documents route %q", h, res.Header.Get(h), via.Header.Get(h))
			}
		}
	})
	t.Run("the cashier emails it to the account's contact once", func(t *testing.T) {
		status, buf := f.userAs(t, "cashier", "POST", "/api/v1/pos/transactions/"+sale.String()+"/receipt/email", "", hdr)
		if status != http.StatusAccepted || !strings.Contains(string(buf), `"status":"queued"`) {
			t.Fatalf("email: %d %s, want 202 queued", status, buf)
		}
		got := f.mail.recipients(1)
		if len(got) != 1 || got[0] != "receipt-contact@example.test" {
			t.Errorf("sent to %v, want the account's address once", got)
		}
	})
	t.Run("another yard's sale is the missing sale's 404 for both", func(t *testing.T) {
		for _, c := range []struct{ method, path string }{
			{"GET", "/api/v1/pos/transactions/" + other.String() + "/receipt"},
			{"POST", "/api/v1/pos/transactions/" + other.String() + "/receipt/email"},
		} {
			if status, buf := f.userAs(t, "cashier", c.method, c.path, "", hdr); status != http.StatusNotFound {
				t.Errorf("%s %s: %d %s, want 404", c.method, c.path, status, buf)
			}
			if status, buf := f.key(t, f.bound, c.method, c.path, "", nil); status != http.StatusNotFound {
				t.Errorf("bound key %s %s: %d %s, want the sale read's 404", c.method, c.path, status, buf)
			}
		}
		if n := len(f.mail.recipients(0)); n != 1 {
			t.Errorf("a refused email was sent: %d messages in all", n)
		}
	})
	t.Run("an open sale has no receipt", func(t *testing.T) {
		for _, c := range []struct{ method, path string }{
			{"GET", "/api/v1/pos/transactions/" + open.String() + "/receipt"},
			{"POST", "/api/v1/pos/transactions/" + open.String() + "/receipt/email"},
		} {
			status, buf := f.userAs(t, "cashier", c.method, c.path, "", hdr)
			if status != http.StatusConflict || receiptCode(t, buf) != "no_receipt" {
				t.Errorf("%s %s: %d %s, want 409 no_receipt", c.method, c.path, status, buf)
			}
		}
	})
	t.Run("an account with no contact answers no_contact in words", func(t *testing.T) {
		status, buf := f.userAs(t, "cashier", "POST", "/api/v1/pos/transactions/"+noContact.String()+"/receipt/email", "", hdr)
		if status != http.StatusConflict || receiptCode(t, buf) != "no_contact" {
			t.Fatalf("email without a contact: %d %s, want 409 no_contact", status, buf)
		}
		if n := len(f.mail.recipients(0)); n != 1 {
			t.Errorf("an email with no contact was sent: %d messages in all", n)
		}
		if status, buf := f.userAs(t, "cashier", "GET", "/api/v1/pos/transactions/"+noContact.String()+"/receipt", "", hdr); status != http.StatusOK {
			t.Errorf("print with no contact: %d %s, want 200 (print needs no address)", status, buf)
		}
	})
	t.Run("the documents routes still refuse a cashier", func(t *testing.T) {
		if status, _ := f.userAs(t, "cashier", "GET", "/api/v1/documents/print/invoice/"+inv.String(), "", hdr); status != http.StatusForbidden {
			t.Errorf("documents print as cashier: %d, want 403", status)
		}
		if status, _ := f.userAs(t, "cashier", "POST", "/api/v1/invoices/"+inv.String()+"/email", "", hdr); status != http.StatusForbidden {
			t.Errorf("invoice email as cashier: %d, want 403", status)
		}
	})
}
