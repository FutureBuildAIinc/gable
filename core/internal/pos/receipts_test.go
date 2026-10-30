// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos_test

// Counter documents: a print count per sale so the second and later
// prints say REPRINT with the count, the layout choice on the receipt route,
// and the sealed signature a sale carries (the typed name, the drawn PNG, the
// time and a SHA-256 seal that never changes). The 80mm render itself is a
// separate slice; these tests drive the route through a stand in renderer
// that prints what the real one is handed.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/customer"
	"github.com/gablelbm/gable/internal/document"
	"github.com/gablelbm/gable/internal/invoice"
	"github.com/gablelbm/gable/internal/pos"
	"github.com/gablelbm/gable/internal/testutil"
	"github.com/google/uuid"
)

type toggleInvoiceRepo struct {
	invoice.Repository
	fail *bool
}

func (r *toggleInvoiceRepo) GetInvoice(ctx context.Context, id uuid.UUID) (*invoice.Invoice, error) {
	if r.fail != nil && *r.fail {
		return nil, errors.New("invoice lookup failed")
	}
	return r.Repository.GetInvoice(ctx, id)
}

// a one pixel PNG, the smallest honest signature image
const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="

// thermalStandIn renders the text the 80mm receipt is handed, so a test can
// read the reprint marker and the signature block the route passed it.
func thermalStandIn(fail *bool) document.ThermalRenderer {
	return func(_ context.Context, invoiceID uuid.UUID, p document.ReceiptPrint) ([]byte, error) {
		if fail != nil && *fail {
			return nil, errors.New("the renderer failed")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "RECEIPT %s\n", invoiceID)
		if p.Reprint() {
			fmt.Fprintf(&b, "REPRINT %d\n", p.Number)
		}
		if p.Signature != nil {
			fmt.Fprintf(&b, "SIGNED %s %s %d\n", p.Signature.SignerName, p.Signature.SealHex[:12], len(p.Signature.PNG))
		}
		return []byte(b.String()), nil
	}
}

func withReceipts(render document.ThermalRenderer) func(*fixture) {
	return func(f *fixture) {
		f.mounts = append(f.mounts, func(f *fixture, mux *http.ServeMux) {
			h := document.NewHandler(nil, nil, nil, nil, nil).WithSaleReceipts(f.service).WithReceiptPrints(f.service)
			if render != nil {
				h.WithThermalReceipt(render)
			}
			h.RegisterReceiptRoutes(mux, func(next http.Handler) http.Handler { return next })
		})
	}
}

func withRealThermalReceipts() func(*fixture) {
	return withRealInvoiceAndThermalReceipts(nil)
}

func withRealInvoiceAndThermalReceipts(failInvoice *bool) func(*fixture) {
	return func(f *fixture) {
		f.mounts = append(f.mounts, func(f *fixture, mux *http.ServeMux) {
			store := document.NewStore(f.db)
			docSvc := document.NewService(nil).WithYards(store).WithJobs(store).WithInvoicePayments(f.service)
			invRepo := &toggleInvoiceRepo{Repository: invoice.NewRepository(f.db), fail: failInvoice}
			invSvc := invoice.NewService(invRepo, nil, nil, f.db)
			custSvc := customer.NewService(customer.NewRepository(f.db))
			h := document.NewHandler(docSvc, nil, invSvc, custSvc, nil).WithSaleReceipts(f.service).WithReceiptPrints(f.service)
			h.WithThermalReceipt(h.ThermalRendererOf(f.service))
			h.RegisterReceiptRoutes(mux, func(next http.Handler) http.Handler { return next })
		})
	}
}

func (f *fixture) printReceipt(saleID, query string) resp {
	f.t.Helper()
	path := "/api/v1/pos/transactions/" + saleID + "/receipt"
	if query != "" {
		path += "?" + query
	}
	return f.do("GET", path, nil, "X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

func (f *fixture) sign(saleID, name, png string) resp {
	f.t.Helper()
	return f.do("POST", "/api/v1/pos/transactions/"+saleID+"/signature",
		map[string]any{"signer_name": name, "image_png_base64": png},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(f.t))
}

func (f *fixture) printCount(saleID string) int64 {
	f.t.Helper()
	return f.scalar(`SELECT print_count::bigint FROM pos_transactions WHERE id = $1`, uuid.MustParse(saleID)).(int64)
}

func completedSale(t *testing.T, f *fixture) (string, int64) {
	t.Helper()
	sale, total := f.cartOf("1", &f.customerID)
	if r := f.completeSale(sale, tender("cash", total)); r.status != http.StatusOK {
		t.Fatalf("complete = %d: %s", r.status, r.raw)
	}
	return sale, total
}

// RULE: each print counts against the sale; the first says nothing of a
// reprint, the second and later say REPRINT with the count.
func TestEachPrintIsCountedAndTheLaterOnesSayReprint(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), withReceipts(thermalStandIn(nil)))
	sale, _ := completedSale(t, f)

	first := f.printReceipt(sale, "layout=thermal80")
	if first.status != http.StatusOK || strings.Contains(string(first.raw), "REPRINT") {
		t.Fatalf("first print = %d: %s, want 200 with no reprint marker", first.status, first.raw)
	}
	if first.header.Get("X-Receipt-Print") != "1" {
		t.Errorf("X-Receipt-Print = %q, want 1", first.header.Get("X-Receipt-Print"))
	}
	second := f.printReceipt(sale, "layout=thermal80")
	if !strings.Contains(string(second.raw), "REPRINT 2") || second.header.Get("X-Receipt-Print") != "2" {
		t.Errorf("second print = %s (print %q), want REPRINT 2", second.raw, second.header.Get("X-Receipt-Print"))
	}
	third := f.printReceipt(sale, "layout=thermal80")
	if !strings.Contains(string(third.raw), "REPRINT 3") {
		t.Errorf("third print = %s, want REPRINT 3", third.raw)
	}
	if got := f.printCount(sale); got != 3 {
		t.Errorf("print_count = %d, want 3", got)
	}
	// the sale states its count
	if got := num(t, f.getSale(t, sale), "print_count"); got != 3 {
		t.Errorf("the sale's print_count = %d, want 3", got)
	}
}

// RULE: a render that fails is no print: the count is given back, so the next
// real print is still the first.
func TestAFailedRenderGivesTheCountBack(t *testing.T) {
	testutil.LockOutboxTables(t)
	fail := true
	f := newFixture(t, testutil.RequireDB(t), withReceipts(thermalStandIn(&fail)))
	sale, _ := completedSale(t, f)
	if r := f.printReceipt(sale, "layout=thermal80"); r.status != http.StatusInternalServerError {
		t.Fatalf("a failed render = %d, want 500: %s", r.status, r.raw)
	}
	if got := f.printCount(sale); got != 0 {
		t.Errorf("print_count = %d after a failed render, want 0", got)
	}
	fail = false
	if r := f.printReceipt(sale, "layout=thermal80"); strings.Contains(string(r.raw), "REPRINT") {
		t.Errorf("the first real print says reprint: %s", r.raw)
	}
}

// RULE: the layout is a choice on the route: an unknown layout and an unknown
// query are 400; without the 80mm renderer the layout answers 409
// layout_unavailable and counts nothing; an open sale has no receipt.
func TestTheLayoutChoiceIsStrictAndHonestAboutWhatIsAvailable(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), withReceipts(nil))
	sale, _ := completedSale(t, f)
	if r := f.printReceipt(sale, "layout=landscape"); r.status != http.StatusBadRequest {
		t.Errorf("an unknown layout = %d, want 400: %s", r.status, r.raw)
	}
	if r := f.printReceipt(sale, "size=big"); r.status != http.StatusBadRequest {
		t.Errorf("an unknown query = %d, want 400: %s", r.status, r.raw)
	}
	r := f.printReceipt(sale, "layout=thermal80")
	blockerOf(t, r, "layout_unavailable")
	if got := f.printCount(sale); got != 0 {
		t.Errorf("print_count = %d after an unavailable layout, want 0", got)
	}
	open := f.startSale(&f.customerID)
	if r := f.printReceipt(open, "layout=thermal80"); r.status != http.StatusConflict {
		t.Errorf("a receipt of an open sale = %d, want 409: %s", r.status, r.raw)
	}
}

// RULE: a completed sale is signed once: the typed name, the drawn PNG, the
// time and a seal over the sale, its total, the signer, the time and the
// image's hash. The seal recomputes, the receipt carries the block, and the
// stored row refuses every update.
func TestASaleCarriesOneSealedSignature(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), withReceipts(thermalStandIn(nil)))
	sale, total := completedSale(t, f)

	r := f.sign(sale, "Pat Signer", tinyPNG)
	if r.status != http.StatusCreated {
		t.Fatalf("sign = %d: %s", r.status, r.raw)
	}
	seal := str(t, r.body, "seal")
	if len(seal) != 64 {
		t.Fatalf("seal = %q, want 64 hex characters", seal)
	}
	if num(t, r.body, "total_cents") != total || r.body["signer_name"] != "Pat Signer" {
		t.Errorf("signature = %v, want the sale's total and the signer", r.body)
	}
	signedAt, err := time.Parse(time.RFC3339Nano, str(t, r.body, "signed_at"))
	if err != nil {
		t.Fatalf("signed_at: %v", err)
	}
	// the seal recomputes from what was stored
	var storedAt time.Time
	var hash, storedSeal string
	if err := f.db.Pool.QueryRow(context.Background(),
		`SELECT signed_at, image_sha256, seal FROM pos_signatures WHERE sale_id = $1`, uuid.MustParse(sale)).Scan(&storedAt, &hash, &storedSeal); err != nil {
		t.Fatal(err)
	}
	if want := pos.SealOf(uuid.MustParse(sale), total, "Pat Signer", storedAt, hash); want != seal || storedSeal != seal {
		t.Errorf("seal %s, stored %s, recomputed %s: they must agree", seal, storedSeal, want)
	}
	if !storedAt.Equal(signedAt) {
		t.Errorf("signed_at %v does not match the stored %v", signedAt, storedAt)
	}
	// the receipt prints the block
	p := f.printReceipt(sale, "layout=thermal80")
	if !strings.Contains(string(p.raw), "SIGNED Pat Signer "+seal[:12]) {
		t.Errorf("receipt = %s, want the signer and the seal's first twelve", p.raw)
	}
	// signed once, and the row never changes
	blockerOf(t, f.sign(sale, "Someone Else", tinyPNG), "signature_exists")
	if _, err := f.db.Pool.Exec(context.Background(), `UPDATE pos_signatures SET signer_name = 'Forged' WHERE sale_id = $1`, uuid.MustParse(sale)); err == nil {
		t.Errorf("a sealed signature accepted an update")
	}
	if n := f.scalar(`SELECT count(*) FROM audit_log WHERE entity_id::text = $1 AND action = 'pos.signature.captured'`, sale); n.(int64) != 1 {
		t.Errorf("audit rows of the capture = %v, want 1", n)
	}
}

// RULE: the capture is refused what is not a signature: a sale not yet
// completed, no name, no image, text that is not base64, an image that is not
// a PNG and one past the size cap.
func TestTheSignatureCaptureRefusesWhatIsNotASignature(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t))
	open := f.startSale(&f.customerID)
	blockerOf(t, f.sign(open, "Pat Signer", tinyPNG), "not_completed")
	sale, _ := completedSale(t, f)
	notPNG := base64.StdEncoding.EncodeToString([]byte("GIF89a not a png"))
	big := base64.StdEncoding.EncodeToString(append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 131073)...))
	for name, c := range map[string][2]string{
		"no name":      {"", tinyPNG},
		"no image":     {"Pat", ""},
		"not base64":   {"Pat", "@@@not base64@@@"},
		"not a png":    {"Pat", notPNG},
		"past the cap": {"Pat", big},
	} {
		if r := f.sign(sale, c[0], c[1]); r.status != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", name, r.status, r.raw)
		}
	}
	// a canvas data URL is read for its payload
	if r := f.sign(sale, "Pat", "data:image/png;base64,"+tinyPNG); r.status != http.StatusCreated {
		t.Errorf("a data URL = %d, want 201: %s", r.status, r.raw)
	}
	if r := f.sign(uuid.NewString(), "Pat", tinyPNG); r.status != http.StatusNotFound {
		t.Errorf("a missing sale = %d, want 404: %s", r.status, r.raw)
	}
}

// Three captures race for one sale (pool size 4, three contenders): exactly
// one seals, the others are told it is signed.
func TestRacingSignaturesSealOnce(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4))
	sale, _ := completedSale(t, f)
	statuses := make([]int, 3)
	var wg sync.WaitGroup
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = f.sign(sale, fmt.Sprintf("Signer %d", i), tinyPNG).status
		}(i)
	}
	wg.Wait()
	created := 0
	for _, st := range statuses {
		switch st {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
		default:
			t.Errorf("statuses = %v, want only 201 and 409", statuses)
		}
	}
	if created != 1 {
		t.Errorf("statuses = %v, want exactly one 201", statuses)
	}
	if n := f.scalar(`SELECT count(*) FROM pos_signatures WHERE sale_id = $1`, uuid.MustParse(sale)); n.(int64) != 1 {
		t.Errorf("signatures = %v, want 1", n)
	}
}

// Racing prints are numbered apart (pool size 4, three contenders): the three
// prints of one sale are 1, 2 and 3 in some order.
func TestRacingPrintsAreNumberedApart(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDBMaxConns(t, 4), withReceipts(thermalStandIn(nil)))
	sale, _ := completedSale(t, f)
	numbers := make([]string, 3)
	var wg sync.WaitGroup
	for i := range numbers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			numbers[i] = f.printReceipt(sale, "layout=thermal80").header.Get("X-Receipt-Print")
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, n := range numbers {
		seen[n] = true
	}
	if len(seen) != 3 || !seen["1"] || !seen["2"] || !seen["3"] {
		t.Errorf("print numbers = %v, want 1, 2 and 3 each once", numbers)
	}
}

// RULE: GET /pos/transactions/{id}/receipt?layout=thermal80 renders a real
// 80mm PDF through GenerateThermalReceiptPDF: the first print has no REPRINT
// banner, the second print has REPRINT #2, and the sealed signature's name
// and 12-character seal prefix appear in the PDF text.
func TestTheRealThermalReceiptPDFRendersSaleSignatureAndReprint(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), withRealThermalReceipts())
	sale, _ := completedSale(t, f)
	saleNumber := str(t, f.getSale(t, sale), "number")

	sigResp := f.sign(sale, "Pat Signer", tinyPNG)
	if sigResp.status != http.StatusCreated {
		t.Fatalf("sign = %d: %s", sigResp.status, sigResp.raw)
	}
	seal := str(t, sigResp.body, "seal")

	first := f.printReceipt(sale, "layout=thermal80")
	if first.status != http.StatusOK {
		t.Fatalf("first print = %d: %s", first.status, first.raw)
	}
	if !strings.HasPrefix(string(first.raw), "%PDF-") || !strings.Contains(string(first.raw), "%%EOF") {
		t.Fatalf("first print is not a complete PDF")
	}
	if strings.Contains(string(first.raw), "REPRINT") {
		t.Errorf("first print unexpectedly contains REPRINT")
	}
	for _, want := range []string{saleNumber, "Counter Co", f.sku, "2x4x8 SPF", "CASH", "Signed by Pat Signer", "sealed " + seal[:12]} {
		if !strings.Contains(string(first.raw), want) {
			t.Errorf("first print PDF missing %q", want)
		}
	}

	second := f.printReceipt(sale, "layout=thermal80")
	if second.status != http.StatusOK {
		t.Fatalf("second print = %d: %s", second.status, second.raw)
	}
	for _, want := range []string{"REPRINT", "#2", "Signed by Pat Signer", "sealed " + seal[:12]} {
		if !strings.Contains(string(second.raw), want) {
			t.Errorf("second print PDF missing %q", want)
		}
	}
}

// RULE: GET /api/v1/pos/transactions/{id}/receipt (1) does not mutate
// pos_transactions.revision, (2) unbumps print_count when an invoice-layout
// render fails, and (3) never increments print_count on a sale that a
// concurrent VoidSale has voided (pool size 4).
func TestReceiptPrintPreservesRevisionUnbumpsFailedInvoiceAndRejectsVoidedSale(t *testing.T) {
	testutil.LockOutboxTables(t)
	db := testutil.RequireDBMaxConns(t, 4)
	var failInvoice bool
	f := newFixture(t, db, withRealInvoiceAndThermalReceipts(&failInvoice))
	f.openTill(0)

	// (1) & (2): failed invoice-layout render unbumps print_count, successful
	// invoice and thermal80 prints leave pos_transactions.revision unchanged so
	// a subsequent void with the pre-receipt revision succeeds.
	sale1, _ := completedSale(t, f)
	revBefore := num(t, f.getSale(t, sale1), "revision")

	failInvoice = true
	if r := f.printReceipt(sale1, "layout=invoice"); r.status == http.StatusOK {
		t.Fatalf("expected failing invoice render to return non-200, got 200")
	}
	if got := f.printCount(sale1); got != 0 {
		t.Errorf("print_count after failed invoice render = %d, want 0", got)
	}
	if got := num(t, f.getSale(t, sale1), "revision"); got != revBefore {
		t.Errorf("revision after failed invoice render = %d, want %d", got, revBefore)
	}

	failInvoice = false
	firstPrint := f.printReceipt(sale1, "layout=invoice")
	if firstPrint.status != http.StatusOK {
		t.Fatalf("invoice receipt print = %d: %s", firstPrint.status, firstPrint.raw)
	}
	if firstPrint.header.Get("X-Receipt-Print") != "1" {
		t.Errorf("X-Receipt-Print after failed render = %q, want 1", firstPrint.header.Get("X-Receipt-Print"))
	}
	secondPrint := f.printReceipt(sale1, "layout=thermal80")
	if secondPrint.status != http.StatusOK || secondPrint.header.Get("X-Receipt-Print") != "2" {
		t.Fatalf("thermal receipt print = %d (%q): %s", secondPrint.status, secondPrint.header.Get("X-Receipt-Print"), secondPrint.raw)
	}
	if got := f.printCount(sale1); got != 2 {
		t.Errorf("print_count = %d, want 2", got)
	}
	if got := num(t, f.getSale(t, sale1), "revision"); got != revBefore {
		t.Errorf("revision after receipt prints = %d, want %d", got, revBefore)
	}
	void1 := f.do("POST", "/api/v1/pos/transactions/"+sale1+"/void",
		map[string]any{"reason": "void with pre-receipt revision", "revision": revBefore},
		"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	if void1.status != http.StatusOK {
		t.Fatalf("void with pre-receipt revision = %d: %s", void1.status, void1.raw)
	}

	// (3) Concurrent VoidSale vs PrintReceipt at pool size 4 never increments
	// print_count on a VOIDED sale.
	sale2, _ := completedSale(t, f)
	sale2Meta := f.getSale(t, sale2)
	rev2 := num(t, sale2Meta, "revision")
	inv2 := uuid.MustParse(str(t, sale2Meta, "invoice_id"))

	ctx := t.Context()
	txHold, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txHold: %v", err)
	}
	defer func() { _ = txHold.Rollback(ctx) }()
	if _, err := txHold.Exec(ctx, `SELECT id FROM invoices WHERE id = $1 FOR UPDATE`, inv2); err != nil {
		t.Fatalf("lock invoice: %v", err)
	}
	var holdPID int32
	if err := txHold.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holdPID); err != nil {
		t.Fatalf("read txHold backend pid: %v", err)
	}

	voidCh := make(chan resp, 1)
	go func() {
		voidCh <- f.do("POST", "/api/v1/pos/transactions/"+sale2+"/void",
			map[string]any{"reason": "concurrent void", "revision": rev2},
			"X-Test-Role", "cashier", "X-Test-Sub", mustUUID(t))
	}()

	// Wait until VoidSale has locked pos_transactions and is blocked on our
	// held invoice row: waiting on this backend's blockers, not on any
	// ungranted lock in the database, which another parallel package can add.
	var voidPID int32
	for range 400 {
		if err := db.Pool.QueryRow(ctx,
			`SELECT pid FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid)) LIMIT 1`, holdPID).Scan(&voidPID); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if voidPID == 0 {
		t.Fatal("timed out waiting for VoidSale to block on the held invoice")
	}

	printCh := make(chan resp, 1)
	go func() {
		printCh <- f.printReceipt(sale2, "layout=thermal80")
	}()

	// Wait until PrintReceipt has passed GetSale and is blocked on
	// pos_transactions behind VoidSale.
	printBlocked := false
	for range 400 {
		var waiting int64
		if err := db.Pool.QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, voidPID).Scan(&waiting); err == nil && waiting >= 1 {
			printBlocked = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !printBlocked {
		t.Fatal("timed out waiting for PrintReceipt to block on the voiding sale")
	}

	_ = txHold.Rollback(ctx)

	vRes := <-voidCh
	pRes := <-printCh
	if vRes.status != http.StatusOK {
		t.Fatalf("concurrent void = %d: %s", vRes.status, vRes.raw)
	}
	if pRes.status != http.StatusConflict {
		t.Errorf("concurrent print against voiding sale = %d, want 409: %s", pRes.status, pRes.raw)
	}
	if got := f.printCount(sale2); got != 0 {
		t.Errorf("print_count on VOIDED sale = %d, want 0", got)
	}
}

// RULE: the default document of a paid counter cash sale is a receipt: it is
// headed RECEIPT, it says PAID, it names the tender that paid it, and it
// carries no total due and no remit to, because there is nothing left to
// remit (CP-14). The 80mm document of the same sale carries the same heading.
func TestAPaidCashSalesDefaultDocumentIsAReceipt(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), withRealInvoiceAndThermalReceipts(nil))
	sale, _ := completedSale(t, f)

	r := f.printReceipt(sale, "")
	if r.status != http.StatusOK {
		t.Fatalf("the default print = %d: %s", r.status, r.raw)
	}
	body := string(r.raw)
	for _, want := range []string{"RECEIPT", "PAID", "CASH:"} {
		if !strings.Contains(body, want) {
			t.Errorf("the default document is missing %q", want)
		}
	}
	for _, unwanted := range []string{"TOTAL DUE", "REMIT TO"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the default document carries %q", unwanted)
		}
	}

	// the 80mm document of the same sale carries the same heading
	thermal := f.printReceipt(sale, "layout=thermal80")
	if thermal.status != http.StatusOK {
		t.Fatalf("the 80mm print = %d: %s", thermal.status, thermal.raw)
	}
	if !strings.Contains(string(thermal.raw), "RECEIPT") {
		t.Errorf("the 80mm document is not headed RECEIPT")
	}
}

// RULE: a rounded cash sale's default document carries the rounding the
// branch's rule made, with its direction; the stored total does not move, so
// the document is where the penny is explained (CP-18).
func TestARoundedCashSalesDocumentCarriesTheRounding(t *testing.T) {
	testutil.LockOutboxTables(t)
	f := newFixture(t, testutil.RequireDB(t), withRealInvoiceAndThermalReceipts(nil))
	f.noTax()
	f.setRounding("NEAREST_5_CENTS")

	sale, _ := f.saleAtPrice(103)
	if r := f.completeSale(sale, tender("cash", 105)); r.status != http.StatusOK {
		t.Fatalf("the rounded sale = %d: %s", r.status, r.raw)
	}
	// the sale the books stored is untouched by the rule
	if got := num(t, f.getSale(t, sale), "total_cents"); got != 103 {
		t.Errorf("the stored total = %d, want 103", got)
	}

	r := f.printReceipt(sale, "")
	if r.status != http.StatusOK {
		t.Fatalf("the default print = %d: %s", r.status, r.raw)
	}
	body := string(r.raw)
	for _, want := range []string{"Cash rounding +$0.02", "TOTAL: $1.03", "CASH: $1.05"} {
		if !strings.Contains(body, want) {
			t.Errorf("the default document is missing %q", want)
		}
	}
}
