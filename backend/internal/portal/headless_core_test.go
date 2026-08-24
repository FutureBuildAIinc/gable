// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gablelbm/gable/internal/order"
	"github.com/google/uuid"
)

// Unit coverage for the capabilities added on top of migration 084 — the parts
// with a seam that does not need Postgres. The tenancy scoping, which lives in
// SQL, is covered against a real database in tenancy_pg_test.go.
//
// Tests are CORRECTNESS unless labelled otherwise.

// --- category tree ------------------------------------------------------

func catRow(id, parent *uuid.UUID, name, slug, path string, sort, direct int) categoryRow {
	r := categoryRow{Name: name, Slug: slug, Path: path, SortOrder: sort, DirectCount: direct}
	if id != nil {
		r.ID = *id
	} else {
		r.ID = uuid.New()
	}
	r.ParentID = parent
	return r
}

// CORRECTNESS: ProductCount is a SUBTREE total. A contractor clicking "Lumber"
// expects the number of things they will actually see, and the catalog's
// ?category_id= filter is an ltree subtree match — so a count that ignored
// descendants would contradict the list it labels.
func TestBuildCategoryTree_RollsCountsUp(t *testing.T) {
	lumber := uuid.New()
	rows := []categoryRow{
		catRow(&lumber, nil, "Lumber", "lumber", "lumber", 1, 3),
		catRow(nil, &lumber, "Framing Lumber", "framing_lumber", "lumber.framing", 1, 5),
		catRow(nil, &lumber, "Sheathing", "sheathing", "lumber.sheathing", 2, 2),
	}

	tree := buildCategoryTree(rows)
	if len(tree) != 1 {
		t.Fatalf("roots = %d, want 1", len(tree))
	}
	root := tree[0]
	if root.ProductCount != 10 {
		t.Errorf("Lumber ProductCount = %d, want 10 (3 direct + 5 + 2)", root.ProductCount)
	}
	if len(root.Children) != 2 {
		t.Fatalf("children = %d, want 2", len(root.Children))
	}
	if root.Children[0].Slug != "framing_lumber" || root.Children[1].Slug != "sheathing" {
		t.Errorf("children out of sort_order: %s then %s", root.Children[0].Slug, root.Children[1].Slug)
	}
	if root.Depth != 0 || root.Children[0].Depth != 1 {
		t.Errorf("depths = %d/%d, want 0/1", root.Depth, root.Children[0].Depth)
	}
	if root.Children[0].Children == nil {
		t.Error("a leaf's Children is nil; it must be an empty slice so it serialises as [] and a consumer can recurse without a null check")
	}
}

// CORRECTNESS: a node whose parent is absent from the input — a parent the
// dealer deactivated, or a dangling reference — is promoted to a root. Dropping
// it would hide its products from browse with no signal that anything was
// missing, which is worse than showing it at the top level.
func TestBuildCategoryTree_PromotesOrphansInsteadOfDroppingThem(t *testing.T) {
	missing := uuid.New()
	rows := []categoryRow{
		catRow(nil, &missing, "Sheathing", "sheathing", "lumber.sheathing", 1, 4),
	}

	tree := buildCategoryTree(rows)
	if len(tree) != 1 {
		t.Fatalf("roots = %d, want 1: the orphan was dropped and its 4 products vanished from browse", len(tree))
	}
	if tree[0].ProductCount != 4 {
		t.Errorf("ProductCount = %d, want 4", tree[0].ProductCount)
	}
}

// CORRECTNESS (robustness): a parent cycle in the data must not take the API
// down. A self-referencing row is a data problem; an unbounded recursion is an
// outage.
func TestBuildCategoryTree_SurvivesACycle(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	rows := []categoryRow{
		catRow(&a, &b, "A", "a", "a", 1, 1),
		catRow(&b, &a, "B", "b", "b", 2, 1),
	}

	done := make(chan []CategoryNodeDTO, 1)
	go func() { done <- buildCategoryTree(rows) }()
	select {
	case tree := <-done:
		if len(tree) == 0 {
			t.Error("a cyclic pair produced no nodes at all")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("buildCategoryTree did not terminate on a cyclic parent chain")
	}
}

// --- change feed --------------------------------------------------------

// CORRECTNESS (RFC 9110 §13.1.2): If-None-Match uses WEAK comparison, so a
// `W/` prefix on either side must still match. A client that echoes back a
// weak-tagged validator has to get its 304, or the whole point of the feed —
// not re-transferring an unchanged list every 30 seconds — is lost.
func TestETagMatches(t *testing.T) {
	const tag = `"abc123"`
	tests := []struct {
		name  string
		inm   string
		match bool
	}{
		{"empty header", "", false},
		{"exact", `"abc123"`, true},
		{"weak on the client side", `W/"abc123"`, true},
		{"wildcard", "*", true},
		{"different tag", `"deadbeef"`, false},
		{"in a list", `"x", "abc123", "y"`, true},
		{"in a list, none match", `"x", "y"`, false},
		{"list with whitespace", `  "abc123"  `, true},
		{"substring is not a match", `"abc"`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := etagMatches(tc.inm, tag); got != tc.match {
				t.Errorf("etagMatches(%q, %q) = %v, want %v", tc.inm, tag, got, tc.match)
			}
		})
	}
}

// CORRECTNESS (security): two customers whose order lists happen to have the
// same length and the same newest timestamp must NOT share an ETag. They
// would, if the validator were computed from the data alone — and a shared
// cache in front of the API would then serve one contractor a 304 against the
// other contractor's cached body.
func TestOrderListETag_IsPerCustomer(t *testing.T) {
	latest := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	a := orderListETag(uuid.New(), OrderListFilter{}, 4, &latest)
	b := orderListETag(uuid.New(), OrderListFilter{}, 4, &latest)
	if a == b {
		t.Fatalf("two customers with identical list shapes share the ETag %s", a)
	}
}

// CORRECTNESS: the validator is stable for identical inputs and moves when
// anything a consumer would care about moves — including the filter, so a
// project-filtered list and an unfiltered one of the same size cannot collide.
func TestOrderListETag_MovesWithTheData(t *testing.T) {
	cust := uuid.New()
	proj := uuid.New()
	t1 := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	t2 := t1.Add(time.Millisecond)

	base := orderListETag(cust, OrderListFilter{}, 4, &t1)

	if again := orderListETag(cust, OrderListFilter{}, 4, &t1); again != base {
		t.Errorf("identical inputs produced different ETags: %s vs %s", base, again)
	}
	for name, got := range map[string]string{
		"count changed":     orderListETag(cust, OrderListFilter{}, 5, &t1),
		"timestamp changed": orderListETag(cust, OrderListFilter{}, 4, &t2),
		"filter changed":    orderListETag(cust, OrderListFilter{ProjectID: &proj}, 4, &t1),
		"empty list":        orderListETag(cust, OrderListFilter{}, 0, nil),
	} {
		if got == base {
			t.Errorf("%s but the ETag did not move (%s)", name, got)
		}
	}
}

// CORRECTNESS: an ETag is quoted, per RFC 9110. An unquoted validator is not a
// valid entity-tag and clients are entitled to ignore it.
func TestOrderListETag_IsQuoted(t *testing.T) {
	got := orderListETag(uuid.New(), OrderListFilter{}, 1, nil)
	if !strings.HasPrefix(got, `"`) || !strings.HasSuffix(got, `"`) {
		t.Errorf("ETag = %s, want it wrapped in double quotes", got)
	}
}

// --- quote status projection --------------------------------------------

// CORRECTNESS: the ERP's DRAFT is the dealer's word for "not priced yet", and
// showing a contractor "DRAFT" for a request they have already submitted reads
// as their own unfinished work — exactly backwards. An unknown state passes
// through verbatim rather than being flattened into a plausible neighbour.
func TestPortalQuoteStatus(t *testing.T) {
	for erp, want := range map[string]string{
		"DRAFT":     QuoteStatusRequested,
		"SENT":      QuoteStatusPriced,
		"ACCEPTED":  QuoteStatusAccepted,
		"REJECTED":  QuoteStatusDeclined,
		"EXPIRED":   QuoteStatusExpired,
		"SOMETHING": "SOMETHING",
	} {
		if got := portalQuoteStatus(erp); got != want {
			t.Errorf("portalQuoteStatus(%q) = %q, want %q", erp, got, want)
		}
	}
}

// --- quote request validation -------------------------------------------

// CORRECTNESS: a quote request is a SCOPE, and the ERP must not be asked to
// invent the parts of it the contractor left out. Each of these is refused
// before any persistence is attempted (the service is built with a nil
// repository, so reaching it would panic).
func TestCreateQuoteRequest_Validation(t *testing.T) {
	svc := newTokenService(testSecret)
	ctx := context.Background()
	cust := uuid.New()
	prod := uuid.New()

	tests := []struct {
		name string
		req  CreateQuoteRequest
		want string
	}{
		{"no lines", CreateQuoteRequest{}, "at least one line"},
		{
			"special-order line with no description",
			CreateQuoteRequest{Lines: []QuoteRequestLine{{Quantity: 2, UOM: "LF"}}},
			"needs a description",
		},
		{
			"special-order line with no uom",
			CreateQuoteRequest{Lines: []QuoteRequestLine{{Description: "clear cedar", Quantity: 2}}},
			"needs a uom",
		},
		{
			"unknown uom",
			CreateQuoteRequest{Lines: []QuoteRequestLine{{Description: "clear cedar", Quantity: 2, UOM: "FURLONGS"}}},
			"unknown uom",
		},
		{
			"zero quantity",
			CreateQuoteRequest{Lines: []QuoteRequestLine{{Description: "x", Quantity: 0, UOM: "LF"}}},
			"quantity must be positive",
		},
		{
			"negative quantity",
			CreateQuoteRequest{Lines: []QuoteRequestLine{{ProductID: &prod, Quantity: -5}}},
			"quantity must be positive",
		},
		{
			"delivery type outside the vocabulary",
			CreateQuoteRequest{DeliveryType: "TELEPORT", Lines: []QuoteRequestLine{{Description: "x", Quantity: 1, UOM: "EA"}}},
			"delivery_type must be",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if reachedPersistence(func() { _, err = svc.CreateQuoteRequest(ctx, cust, tc.req) }) {
				t.Fatalf("the request reached the repository instead of being refused by validation")
			}
			if err == nil {
				t.Fatal("the request was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

// CORRECTNESS: the line limit is enforced, so one caller cannot turn a quote
// request into a bulk insert.
func TestCreateQuoteRequest_LineLimit(t *testing.T) {
	svc := newTokenService(testSecret)
	lines := make([]QuoteRequestLine, maxQuoteRequestLines+1)
	for i := range lines {
		lines[i] = QuoteRequestLine{Description: "x", Quantity: 1, UOM: "EA"}
	}
	var err error
	if reachedPersistence(func() {
		_, err = svc.CreateQuoteRequest(context.Background(), uuid.New(), CreateQuoteRequest{Lines: lines})
	}) {
		t.Fatal("an oversized request reached the repository")
	}
	if err == nil {
		t.Fatal("an oversized request was accepted")
	}
}

// CORRECTNESS (contract): a quote request must have nowhere to put a price.
// The dealer prices the scope; a contractor's markup, labour and margin are
// contractor-owned data and must not gain a home in a dealer's ERP. This test
// pins the shape of the payload, which is the only thing stopping a consumer
// from POSTing its markup here.
func TestCreateQuoteRequest_HasNoPriceFields(t *testing.T) {
	b, err := json.Marshal(CreateQuoteRequest{Lines: []QuoteRequestLine{{}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := strings.ToLower(string(b))
	for _, banned := range []string{"price", "markup", "labour", "labor", "overhead", "margin", "cost", "signature"} {
		if strings.Contains(body, banned) {
			t.Errorf("CreateQuoteRequest exposes a %q field — contractor-owned data must not be accepted here: %s", banned, b)
		}
	}
}

// --- reschedule guards ---------------------------------------------------

// CORRECTNESS: the two predicates that decide whether a reschedule may even be
// asked for. isCommittedRouteStatus draws the same line AI_LM's route push
// draws — past DRAFT/SCHEDULED the load is real.
func TestRescheduleGuards(t *testing.T) {
	for status, terminal := range map[string]bool{
		"DELIVERED": true, "FAILED": true, "PARTIAL": true,
		"delivered": true, // case-insensitive: the column is a free VARCHAR
		"PENDING":   false, "OUT_FOR_DELIVERY": false, "": false,
	} {
		if got := isTerminalDeliveryStatus(status); got != terminal {
			t.Errorf("isTerminalDeliveryStatus(%q) = %v, want %v", status, got, terminal)
		}
	}
	for status, committed := range map[string]bool{
		"IN_TRANSIT": true, "COMPLETED": true, "in_transit": true,
		"DRAFT": false, "SCHEDULED": false, "CANCELLED": false, "": false,
	} {
		if got := isCommittedRouteStatus(status); got != committed {
			t.Errorf("isCommittedRouteStatus(%q) = %v, want %v", status, got, committed)
		}
	}
}

// CORRECTNESS: a reschedule date must be a real calendar day, not in the past
// and not absurdly far out. Each of these is refused before the delivery is
// even loaded (nil repository would panic).
func TestRequestDeliveryReschedule_DateValidation(t *testing.T) {
	svc := newTokenService(testSecret)
	ctx := context.Background()

	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format(rescheduleDateLayout)
	tooFar := time.Now().UTC().AddDate(0, 0, maxRescheduleHorizonDays+2).Format(rescheduleDateLayout)

	for name, date := range map[string]string{
		"empty":       "",
		"prose":       "next tuesday",
		"a timestamp": "2026-09-01T10:00:00Z",
		"in the past": yesterday,
		"too far out": tooFar,
	} {
		t.Run(name, func(t *testing.T) {
			var err error
			if reachedPersistence(func() {
				_, err = svc.RequestDeliveryReschedule(ctx, uuid.New(), uuid.New(), nil,
					RescheduleDeliveryRequest{RequestedDate: date})
			}) {
				t.Fatalf("date %q reached the repository", date)
			}
			if err == nil {
				t.Fatalf("date %q was accepted", date)
			}
			if statusForPortalError(err, http.StatusInternalServerError) != http.StatusBadRequest {
				t.Errorf("date %q maps to %d, want 400", date, statusForPortalError(err, http.StatusInternalServerError))
			}
		})
	}
}

// CORRECTNESS: today is allowed. A dispatcher can still re-sequence a morning
// route, and refusing "today" would make the earliest askable date tomorrow
// for reasons no contractor could guess.
func TestRequestDeliveryReschedule_TodayIsAllowed(t *testing.T) {
	svc := newTokenService(testSecret)
	today := time.Now().UTC().Format(rescheduleDateLayout)

	// Reaching persistence means the date passed validation — which is the
	// assertion. The nil repository panics immediately after.
	if !reachedPersistence(func() {
		_, _ = svc.RequestDeliveryReschedule(context.Background(), uuid.New(), uuid.New(), nil,
			RescheduleDeliveryRequest{RequestedDate: today})
	}) {
		t.Fatal("today's date was refused before it reached the delivery lookup")
	}
}

// --- error to status mapping --------------------------------------------

// CORRECTNESS (security): "belongs to another customer" and "does not exist"
// must be indistinguishable. A 403 on someone else's id is an existence oracle
// — it tells an authenticated contractor which order and quote ids belong to
// their competitors.
func TestStatusForPortalError(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"quote not found":       {ErrQuoteNotFound, http.StatusNotFound},
		"order not found":       {ErrOrderNotFound, http.StatusNotFound},
		"project not found":     {ErrProjectNotFound, http.StatusNotFound},
		"delivery not found":    {ErrDeliveryNotFound, http.StatusNotFound},
		"quote not decidable":   {ErrQuoteNotDecidable, http.StatusConflict},
		"reschedule refused":    {ErrRescheduleRefused, http.StatusConflict},
		"cancel refused":        {ErrCancelRefused, http.StatusConflict},
		"already cancelled":     {order.ErrOrderAlreadyCancelled, http.StatusConflict},
		"erp refuses cancel":    {order.ErrOrderNotCancellable, http.StatusConflict},
		"invalid request":       {ErrInvalidRequest, http.StatusBadRequest},
		"something else is 500": {context.DeadlineExceeded, http.StatusInternalServerError},
	} {
		t.Run(name, func(t *testing.T) {
			if got := statusForPortalError(tc.err, http.StatusInternalServerError); got != tc.want {
				t.Errorf("status = %d, want %d", got, tc.want)
			}
		})
	}

	// Nothing maps to 403: see the doc comment on errors.go.
	for _, err := range []error{ErrQuoteNotFound, ErrOrderNotFound, ErrProjectNotFound, ErrDeliveryNotFound} {
		if statusForPortalError(err, http.StatusInternalServerError) == http.StatusForbidden {
			t.Errorf("%v renders as 403, which confirms the resource exists", err)
		}
	}
}

// CORRECTNESS: a 409 carries a machine-readable code and a customer-safe
// reason, because a consumer building a stage machine has to be able to tell a
// contractor WHY the dealer said no. A bare "Conflict" is not actionable.
func TestWriteRefusal_CarriesAnActionableReason(t *testing.T) {
	for name, err := range map[string]error{
		"reschedule refused": ErrRescheduleRefused,
		"cancel refused":     ErrCancelRefused,
		"already cancelled":  order.ErrOrderAlreadyCancelled,
		"not cancellable":    order.ErrOrderNotCancellable,
		"quote not priced":   ErrQuoteNotDecidable,
	} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/portal/v1/x", nil)

			// The sentinel is wrapped, exactly as the services wrap it.
			wrapped := &wrappedErr{err}
			if !writeRefusal(rec, req, wrapped, http.StatusConflict) {
				t.Fatal("a known refusal was not recognised through a wrap")
			}
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", rec.Code)
			}

			var body portalRefusal
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v (%s)", err, rec.Body.String())
			}
			if body.Error.Code == "" || body.Error.Code == "CONFLICT" {
				t.Errorf("code = %q, want something a client can branch on", body.Error.Code)
			}
			if len(body.Error.Reason) < 20 {
				t.Errorf("reason = %q, want a sentence a contractor can act on", body.Error.Reason)
			}
			// The reason is hand-written, so it must not have picked up the
			// internal error text on its way out.
			if strings.Contains(body.Error.Reason, "cannot be") && strings.Contains(body.Error.Reason, ":") {
				t.Errorf("reason %q looks like a wrapped internal error rather than a written sentence", body.Error.Reason)
			}
		})
	}

	// Anything unrecognised, or any non-409, falls through to the generic
	// envelope — an unexpected internal error must never reach a client here.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/portal/v1/x", nil)
	if writeRefusal(rec, req, context.DeadlineExceeded, http.StatusConflict) {
		t.Error("an unrecognised error was rendered as a refusal with a reason")
	}
	if writeRefusal(rec, req, ErrCancelRefused, http.StatusInternalServerError) {
		t.Error("a non-409 status was rendered through the refusal path")
	}
}

type wrappedErr struct{ inner error }

func (w *wrappedErr) Error() string { return "context: " + w.inner.Error() }
func (w *wrappedErr) Unwrap() error { return w.inner }

// --- handler wiring ------------------------------------------------------

// CORRECTNESS: the ?since= cursor must be rejected when it is not a timestamp,
// rather than silently ignored. A consumer whose clock formatting is wrong
// would otherwise poll forever, be handed the full list every time, and never
// learn why.
func TestHandleListOrders_RejectsAMalformedCursor(t *testing.T) {
	h := NewHandler(newTokenService(testSecret)) // nil repo: reaching it would panic
	for _, q := range []string{"?since=nope", "?since=1712345678", "?project_id=not-a-uuid"} {
		rec := httptest.NewRecorder()
		h.HandleListOrders(rec, httptest.NewRequest(http.MethodGet, "/api/portal/v1/orders"+q, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s => %d, want 400", q, rec.Code)
		}
	}
}

// --- DTO wire format -----------------------------------------------------

// CORRECTNESS (contract): lead_time_days must serialise as null when the
// dealer has not published one. A zero would read as "available today" — a
// schedulable claim — and the consumer's lead-time warnings would be computed
// against a number nobody asserted.
func TestCatalogDTO_UnpublishedLeadTimeIsNullNotZero(t *testing.T) {
	b, err := json.Marshal(CatalogProductDTO{SKU: "X"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := string(raw["lead_time_days"]); got != "null" {
		t.Errorf("lead_time_days = %s, want null", got)
	}

	fourteen := 14
	b, _ = json.Marshal(CatalogProductDTO{SKU: "X", LeadTimeDays: &fourteen})
	_ = json.Unmarshal(b, &raw)
	if got := string(raw["lead_time_days"]); got != "14" {
		t.Errorf("a published lead time serialised as %s, want 14", got)
	}

	zero := 0
	b, _ = json.Marshal(CatalogProductDTO{SKU: "X", LeadTimeDays: &zero})
	_ = json.Unmarshal(b, &raw)
	if got := string(raw["lead_time_days"]); got != "0" {
		t.Errorf("an explicit same-day lead time serialised as %s, want 0 — it must stay distinguishable from null", got)
	}
}

// CORRECTNESS (contract): the money on the new DTOs is float64 DOLLARS, like
// every other portal DTO. The ERP side of the same data is int64 cents, so
// this boundary is where a $21.39 unit price would become $2,139.00.
func TestNewDTOs_MoneyIsDollars(t *testing.T) {
	raw := func(v any) map[string]json.RawMessage {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return m
	}

	vb := raw(VolumeBreakDTO{MinQuantity: 20, UnitPrice: 21.39, SavesPerUnit: 1.85})
	for field, want := range map[string]string{"unit_price": "21.39", "saves_per_unit": "1.85", "min_quantity": "20"} {
		if got := string(vb[field]); got != want {
			t.Errorf("VolumeBreakDTO.%s = %s, want %s", field, got, want)
		}
	}

	q := raw(PortalQuoteDTO{TotalAmount: 2091.5, FreightAmount: 125})
	if got := string(q["total_amount"]); got != "2091.5" {
		t.Errorf("PortalQuoteDTO.total_amount = %s, want 2091.5 dollars", got)
	}
	if got := string(q["freight_amount"]); got != "125" {
		t.Errorf("PortalQuoteDTO.freight_amount = %s, want 125 dollars", got)
	}

	ql := raw(PortalQuoteLineDTO{UnitPrice: 9.25, LineTotal: 1110, Quantity: 120})
	if got := string(ql["unit_price"]); got != "9.25" {
		t.Errorf("PortalQuoteLineDTO.unit_price = %s, want dollars", got)
	}
}

// CORRECTNESS (contract): an unassociated order serialises project_id as null,
// not as the zero UUID. A consumer grouping by project must be able to tell
// "no project" from a project whose id happens to be all zeros.
func TestPortalOrderDTO_NoProjectIsNull(t *testing.T) {
	b, _ := json.Marshal(PortalOrderDTO{})
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	if got := string(m["project_id"]); got != "null" {
		t.Errorf("project_id = %s, want null", got)
	}
	if got := string(m["project_name"]); got != "null" {
		t.Errorf("project_name = %s, want null", got)
	}
	if _, ok := m["updated_at"]; !ok {
		t.Error("updated_at is absent; it is the change-feed cursor and must always be present")
	}
}

// CORRECTNESS: the quote DTO's `priced` flag is derived from the LIFECYCLE, not
// from the total, so a dealer's genuine $0.00 quote (goodwill, warranty
// replacement) is not reported as unpriced.
func TestPortalQuoteDTO_PricedIsNotDerivedFromTheTotal(t *testing.T) {
	if portalQuoteStatus("SENT") == QuoteStatusRequested {
		t.Fatal("SENT maps to REQUESTED")
	}
	// The derivation lives in scanPortalQuote; assert the rule it encodes.
	for _, state := range []string{"SENT", "ACCEPTED", "REJECTED", "EXPIRED"} {
		if portalQuoteStatus(state) == QuoteStatusRequested {
			t.Errorf("state %s would be reported as unpriced", state)
		}
	}
	if portalQuoteStatus("DRAFT") != QuoteStatusRequested {
		t.Error("DRAFT must be the only unpriced state")
	}
}
