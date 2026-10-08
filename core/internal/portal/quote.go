// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gablelbm/gable/internal/product"
	"github.com/gablelbm/gable/internal/quote"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The portal quote is a customer-scoped VIEW of the ERP quote in
// internal/quote, not a parallel system. The split is deliberate:
//
//   - The LIFECYCLE belongs to the ERP. Accept and decline go through
//     quote.Service.UpdateState, so the transition table in
//     quote.validateStateTransition is the single authority on what may
//     follow what. The portal adds a narrower rule on top (see AcceptQuote)
//     but never a wider one.
//   - The PROJECTION belongs to the portal, which owns its own SQL over
//     quotes/quote_lines exactly as it already does over orders, invoices and
//     deliveries. That is what lets every read carry `AND customer_id = $n`.
//
// What a portal user can send is a SCOPE, never a price. CreateQuoteRequest
// has no unit price, no markup, no labour, no overhead and no margin field,
// and the insert writes 0.00 into every money column. The dealer prices it.
// Accepting a contractor's own numbers into a dealer's ERP would make this
// endpoint a store for contractor-owned data, which is the one thing it must
// not become.

// Portal-facing quote status vocabulary. These are a projection of
// quotes.state, kept separate because "DRAFT" is the dealer's word for a quote
// they have not priced yet and it reads as the contractor's own unfinished
// work — which is exactly backwards.
const (
	QuoteStatusRequested = "REQUESTED" // ERP DRAFT: sent to the dealer, not yet priced
	QuoteStatusPriced    = "PRICED"    // ERP SENT: the dealer has priced and returned it
	QuoteStatusAccepted  = "ACCEPTED"  // ERP ACCEPTED: terminal
	QuoteStatusDeclined  = "DECLINED"  // ERP REJECTED
	QuoteStatusExpired   = "EXPIRED"   // ERP EXPIRED
)

// portalQuoteStatus maps an ERP quote state onto the portal vocabulary. An
// unrecognised state passes through verbatim rather than being flattened into
// a plausible-looking neighbour: a consumer seeing a word it does not know is
// a smaller problem than a consumer confidently shown the wrong stage.
func portalQuoteStatus(state string) string {
	switch quote.QuoteState(state) {
	case quote.QuoteStateDraft:
		return QuoteStatusRequested
	case quote.QuoteStateSent:
		return QuoteStatusPriced
	case quote.QuoteStateAccepted:
		return QuoteStatusAccepted
	case quote.QuoteStateRejected:
		return QuoteStatusDeclined
	case quote.QuoteStateExpired:
		return QuoteStatusExpired
	default:
		return state
	}
}

// --- DTOs ---------------------------------------------------------------

// PortalQuoteDTO is a customer-facing quote.
// TODO: align with int64 cents — TotalAmount and FreightAmount are float64
// dollars, matching every other portal DTO.
type PortalQuoteDTO struct {
	ID       uuid.UUID `json:"id"`
	Status   string    `json:"status"`    // portal vocabulary, see the constants above
	ERPState string    `json:"erp_state"` // the raw quotes.state, so a consumer can de-dup on what the dealer actually said

	ProjectID   *uuid.UUID `json:"project_id"`
	ProjectName *string    `json:"project_name"`
	Notes       string     `json:"notes"`

	// Priced is false until the dealer sends the quote back. TotalAmount is
	// 0.00 while Priced is false, and a consumer must not render that as a
	// $0.00 quotation.
	Priced        bool    `json:"priced"`
	TotalAmount   float64 `json:"total_amount"`
	FreightAmount float64 `json:"freight_amount"`
	DeliveryType  string  `json:"delivery_type"`

	ExpiresAt  *time.Time `json:"expires_at"`
	SentAt     *time.Time `json:"sent_at"`
	AcceptedAt *time.Time `json:"accepted_at"`
	RejectedAt *time.Time `json:"rejected_at"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`

	Lines []PortalQuoteLineDTO `json:"lines"`
}

// PortalQuoteLineDTO is one line of a customer-facing quote.
// TODO: align with int64 cents — UnitPrice and LineTotal are float64 dollars.
type PortalQuoteLineDTO struct {
	ID          uuid.UUID  `json:"id"`
	ProductID   *uuid.UUID `json:"product_id"`
	ProductSKU  string     `json:"product_sku"`
	Description string     `json:"description"`

	// CustomerNote is what the contractor wrote when asking. It is the only
	// free text on the line and it is never overwritten by the dealer's
	// pricing pass.
	CustomerNote string `json:"customer_note"`

	Quantity  float64 `json:"quantity"`
	UOM       string  `json:"uom"`
	UnitPrice float64 `json:"unit_price"`
	LineTotal float64 `json:"line_total"`

	// IsSpecialOrder is true for a line with no catalog product behind it —
	// the "price me this thing you don't stock" case that has no other way
	// into the ERP.
	IsSpecialOrder bool `json:"is_special_order"`
}

// CreateQuoteRequest is a portal user asking the dealer to price a scope.
//
// There is no price field anywhere in this type, by design. See the package
// note at the top of this file.
type CreateQuoteRequest struct {
	ProjectID    *uuid.UUID         `json:"project_id"`
	Notes        string             `json:"notes"`
	DeliveryType string             `json:"delivery_type"` // PICKUP (default) or DELIVERY
	Lines        []QuoteRequestLine `json:"lines"`
}

// QuoteRequestLine is one item on the scope being sent for pricing.
//
// ProductID null means a special-order line: something the dealer does not
// stock, described in words. Description and UOM are then required, because
// the alternative is the ERP inventing a unit of measure for a thing it has
// never seen.
type QuoteRequestLine struct {
	ProductID   *uuid.UUID `json:"product_id"`
	Description string     `json:"description"`
	Quantity    float64    `json:"quantity"`
	UOM         string     `json:"uom"`
	Note        string     `json:"note"`
}

// maxQuoteRequestLines bounds a single request. 200 is well past any real
// takeoff a contractor types by hand and keeps one caller from turning a
// quote request into a bulk insert.
const maxQuoteRequestLines = 200

// validUOMs is the uom_type enum from migration 001. A UOM outside it fails at
// the database with an opaque enum error, so it is rejected here where the
// message can name the field.
var validUOMs = map[string]bool{
	string(product.UOM_PCS): true, string(product.UOM_EA): true,
	string(product.UOM_LF): true, string(product.UOM_SF): true,
	string(product.UOM_BF): true, string(product.UOM_MBF): true,
	string(product.UOM_SQ): true, string(product.UOM_BOX): true,
	string(product.UOM_CTN): true, string(product.UOM_RL): true,
	string(product.UOM_GAL): true, string(product.UOM_LBS): true,
	string(product.UOM_BAG): true, string(product.UOM_BUNDLE): true,
	string(product.UOM_PAIR): true, string(product.UOM_SET): true,
}

// --- Service ------------------------------------------------------------

// ListQuotes returns every quote belonging to the calling customer.
func (s *Service) ListQuotes(ctx context.Context, customerID uuid.UUID) ([]PortalQuoteDTO, error) {
	return s.repo.ListPortalQuotes(ctx, customerID)
}

// GetQuote returns one quote, scoped to the calling customer. A quote owned by
// another customer is reported as not found rather than as forbidden, so the
// response cannot be used to probe which quote ids exist.
func (s *Service) GetQuote(ctx context.Context, quoteID, customerID uuid.UUID) (*PortalQuoteDTO, error) {
	return s.repo.GetPortalQuote(ctx, quoteID, customerID)
}

// CreateQuoteRequest records a scope for the dealer to price.
//
// The resulting row is an ordinary ERP quote in DRAFT with source='portal' and
// every money column at zero. It appears on the dealer's quote desk exactly
// like a quote a counter salesperson started.
func (s *Service) CreateQuoteRequest(ctx context.Context, customerID uuid.UUID, req CreateQuoteRequest) (*PortalQuoteDTO, error) {
	if len(req.Lines) == 0 {
		return nil, fmt.Errorf("a quote request needs at least one line")
	}
	if len(req.Lines) > maxQuoteRequestLines {
		return nil, fmt.Errorf("a quote request is limited to %d lines", maxQuoteRequestLines)
	}

	deliveryType := strings.ToUpper(strings.TrimSpace(req.DeliveryType))
	switch deliveryType {
	case "":
		deliveryType = "PICKUP"
	case "PICKUP", "DELIVERY":
	default:
		return nil, fmt.Errorf("delivery_type must be PICKUP or DELIVERY")
	}

	// Tenancy: a project id from the request body is caller-controlled and is
	// checked against the session's customer before it is written. Without
	// this a portal user could file their quote against another contractor's
	// job and read that job's name back out of the response.
	if req.ProjectID != nil {
		ok, err := s.repo.ProjectBelongsToCustomer(ctx, *req.ProjectID, customerID)
		if err != nil {
			return nil, fmt.Errorf("failed to verify project: %w", err)
		}
		if !ok {
			return nil, ErrProjectNotFound
		}
	}

	lines := make([]portalQuoteLineInsert, 0, len(req.Lines))
	for i, l := range req.Lines {
		if l.Quantity <= 0 {
			return nil, fmt.Errorf("line %d: quantity must be positive", i+1)
		}

		ins := portalQuoteLineInsert{
			ProductID:    l.ProductID,
			Quantity:     l.Quantity,
			Description:  strings.TrimSpace(l.Description),
			CustomerNote: strings.TrimSpace(l.Note),
			UOM:          strings.ToUpper(strings.TrimSpace(l.UOM)),
		}

		if l.ProductID != nil {
			// A catalog line snapshots the dealer's own SKU/description/UOM.
			// Taking them from the request instead would let a caller relabel
			// a product on the dealer's quote desk.
			sku, desc, uom, err := s.repo.LookupQuoteLineProduct(ctx, *l.ProductID)
			if err != nil {
				return nil, fmt.Errorf("line %d: product not found", i+1)
			}
			ins.SKU = sku
			if ins.Description == "" {
				ins.Description = desc
			}
			if ins.UOM == "" {
				ins.UOM = uom
			}
		} else {
			// Special order: nothing in the catalog to copy from, so the
			// caller must say what it is and how it is measured. Defaulting
			// the UOM here would be the ERP inventing a unit for a product it
			// has never seen.
			if ins.Description == "" {
				return nil, fmt.Errorf("line %d: a line with no product_id needs a description", i+1)
			}
			if ins.UOM == "" {
				return nil, fmt.Errorf("line %d: a line with no product_id needs a uom", i+1)
			}
			ins.SKU = "SPECIAL-ORDER"
		}

		if !validUOMs[ins.UOM] {
			return nil, fmt.Errorf("line %d: unknown uom %q", i+1, ins.UOM)
		}
		lines = append(lines, ins)
	}

	quoteID, err := s.repo.CreatePortalQuote(ctx, customerID, portalQuoteInsert{
		ProjectID:    req.ProjectID,
		Notes:        strings.TrimSpace(req.Notes),
		DeliveryType: deliveryType,
	}, lines)
	if err != nil {
		return nil, fmt.Errorf("failed to create quote request: %w", err)
	}

	s.logger.Info("Portal quote request created",
		"customer_id", customerID, "quote_id", quoteID, "lines", len(lines))

	return s.repo.GetPortalQuote(ctx, quoteID, customerID)
}

// AcceptQuote accepts a priced quote on behalf of the customer.
//
// The portal rule is narrower than the ERP's: quote.validateStateTransition
// permits DRAFT -> ACCEPTED (a counter salesperson closing a quote in the
// room), but a contractor may only accept a quote the dealer has actually
// priced and SENT. Accepting a DRAFT through this endpoint would let a portal
// user lock in a total the dealer has not written yet — today $0.00.
func (s *Service) AcceptQuote(ctx context.Context, quoteID, customerID uuid.UUID) (*PortalQuoteDTO, error) {
	return s.decideQuote(ctx, quoteID, customerID, quote.QuoteStateAccepted)
}

// DeclineQuote rejects a priced quote on behalf of the customer. Same
// SENT-only precondition as AcceptQuote, for the same reason.
func (s *Service) DeclineQuote(ctx context.Context, quoteID, customerID uuid.UUID) (*PortalQuoteDTO, error) {
	return s.decideQuote(ctx, quoteID, customerID, quote.QuoteStateRejected)
}

func (s *Service) decideQuote(ctx context.Context, quoteID, customerID uuid.UUID, target quote.QuoteState) (*PortalQuoteDTO, error) {
	// Ownership first, and with the same "not found" answer for someone
	// else's quote as for a quote that does not exist.
	current, err := s.repo.GetPortalQuote(ctx, quoteID, customerID)
	if err != nil {
		return nil, err
	}

	if quote.QuoteState(current.ERPState) != quote.QuoteStateSent {
		return nil, fmt.Errorf("%w: quote is %s, not %s",
			ErrQuoteNotDecidable, current.Status, QuoteStatusPriced)
	}

	if s.quoteSvc == nil {
		return nil, fmt.Errorf("quote lifecycle service is not configured")
	}
	if err := s.quoteSvc.UpdateState(ctx, quoteID, target); err != nil {
		return nil, err
	}

	s.logger.Info("Portal quote decision",
		"customer_id", customerID, "quote_id", quoteID, "state", target)

	return s.repo.GetPortalQuote(ctx, quoteID, customerID)
}

// --- Repository ---------------------------------------------------------

type portalQuoteInsert struct {
	ProjectID    *uuid.UUID
	Notes        string
	DeliveryType string
}

type portalQuoteLineInsert struct {
	ProductID    *uuid.UUID
	SKU          string
	Description  string
	CustomerNote string
	Quantity     float64
	UOM          string
}

// LookupQuoteLineProduct reads the dealer's own SKU, description and primary
// UOM for a product, so a quote line snapshots dealer data rather than
// whatever the caller typed.
func (r *PostgresRepository) LookupQuoteLineProduct(ctx context.Context, productID uuid.UUID) (sku, description, uom string, err error) {
	query := `SELECT sku, description, uom_primary::text FROM products WHERE id = $1`
	err = r.db.GetExecutor(ctx).QueryRow(ctx, query, productID).Scan(&sku, &description, &uom)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", "", "", fmt.Errorf("product not found")
		}
		return "", "", "", fmt.Errorf("failed to look up product: %w", err)
	}
	return sku, description, uom, nil
}

// CreatePortalQuote inserts a DRAFT, unpriced quote and its lines in one
// transaction.
//
// Every money column is written as an explicit 0: total_amount, freight_amount,
// unit_price and line_total. That is not a placeholder for a number the portal
// forgot to send — it is the record that the quote is UNPRICED, and the DTO's
// `priced` flag is derived from the state, not from the total, so a real $0.00
// dealer quote could never be confused with one.
//
// branch_id falls back to system_settings.default_branch_id the same way
// quote.PostgresRepository.CreateQuote does; a portal session carries no branch
// context, and the column is NOT NULL since migration 063.
func (r *PostgresRepository) CreatePortalQuote(ctx context.Context, customerID uuid.UUID, hdr portalQuoteInsert, lines []portalQuoteLineInsert) (uuid.UUID, error) {
	quoteID := uuid.New()

	err := r.db.RunInTx(ctx, func(txCtx context.Context) error {
		exec := r.db.GetExecutor(txCtx)

		_, err := exec.Exec(txCtx, `
			INSERT INTO quotes (
				id, customer_id, project_id, state, total_amount, freight_amount,
				delivery_type, source, customer_notes, margin_total,
				created_at, updated_at, branch_id
			) VALUES (
				$1, $2, $3, 'DRAFT', 0, 0,
				$4, 'portal', $5, 0,
				NOW(), NOW(),
				(SELECT value::uuid FROM system_settings WHERE key = 'default_branch_id')
			)
		`, quoteID, customerID, hdr.ProjectID, hdr.DeliveryType, hdr.Notes)
		if err != nil {
			return fmt.Errorf("failed to insert quote header: %w", err)
		}

		for _, l := range lines {
			_, err := exec.Exec(txCtx, `
				INSERT INTO quote_lines (
					id, quote_id, product_id, sku, description, customer_note,
					quantity, uom, unit_price, line_total, created_at
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8::uom_type, 0, 0, NOW())
			`, uuid.New(), quoteID, l.ProductID, l.SKU, l.Description, l.CustomerNote, l.Quantity, l.UOM)
			if err != nil {
				return fmt.Errorf("failed to insert quote line: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, err
	}
	return quoteID, nil
}

// portalQuoteSelect is the shared projection. It is one string so the list and
// the single read cannot drift apart — and so the `AND q.customer_id = $n`
// tenancy clause that both of them append is visibly the only difference.
const portalQuoteSelect = `
	SELECT q.id, q.state::text, q.project_id, p.name,
	       COALESCE(q.customer_notes, ''), COALESCE(q.total_amount, 0)::float8,
	       COALESCE(q.freight_amount, 0)::float8, COALESCE(q.delivery_type, 'PICKUP'),
	       q.expires_at, q.sent_at, q.accepted_at, q.rejected_at,
	       q.created_at, q.updated_at
	FROM quotes q
	LEFT JOIN projects p ON p.id = q.project_id
`

func scanPortalQuote(row pgx.Row) (*PortalQuoteDTO, error) {
	var q PortalQuoteDTO
	var state string
	err := row.Scan(
		&q.ID, &state, &q.ProjectID, &q.ProjectName,
		&q.Notes, &q.TotalAmount, &q.FreightAmount, &q.DeliveryType,
		&q.ExpiresAt, &q.SentAt, &q.AcceptedAt, &q.RejectedAt,
		&q.CreatedAt, &q.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	q.ERPState = state
	q.Status = portalQuoteStatus(state)
	// Priced is derived from the LIFECYCLE, not from the total. A quote the
	// dealer priced at exactly $0.00 (all-inclusive, goodwill, warranty
	// replacement) is still priced.
	q.Priced = q.Status != QuoteStatusRequested
	q.Lines = make([]PortalQuoteLineDTO, 0)
	return &q, nil
}

// ListPortalQuotes returns the calling customer's quotes, newest first.
func (r *PostgresRepository) ListPortalQuotes(ctx context.Context, customerID uuid.UUID) ([]PortalQuoteDTO, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx,
		portalQuoteSelect+` WHERE q.customer_id = $1 ORDER BY q.created_at DESC LIMIT 100`,
		customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list quotes: %w", err)
	}
	defer rows.Close()

	quotes := make([]PortalQuoteDTO, 0)
	for rows.Next() {
		q, err := scanPortalQuote(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan quote: %w", err)
		}
		quotes = append(quotes, *q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("quote rows error: %w", err)
	}

	for i := range quotes {
		lines, err := r.getPortalQuoteLines(ctx, quotes[i].ID)
		if err != nil {
			return nil, err
		}
		quotes[i].Lines = lines
	}
	return quotes, nil
}

// GetPortalQuote returns one quote scoped to a customer. The customer_id
// predicate is in the WHERE clause, not a post-fetch comparison, so a quote
// belonging to another customer is indistinguishable from one that does not
// exist.
func (r *PostgresRepository) GetPortalQuote(ctx context.Context, quoteID, customerID uuid.UUID) (*PortalQuoteDTO, error) {
	row := r.db.GetExecutor(ctx).QueryRow(ctx,
		portalQuoteSelect+` WHERE q.id = $1 AND q.customer_id = $2`,
		quoteID, customerID)

	q, err := scanPortalQuote(row)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, ErrQuoteNotFound
		}
		return nil, fmt.Errorf("failed to get quote: %w", err)
	}

	lines, err := r.getPortalQuoteLines(ctx, quoteID)
	if err != nil {
		return nil, err
	}
	q.Lines = lines
	return q, nil
}

func (r *PostgresRepository) getPortalQuoteLines(ctx context.Context, quoteID uuid.UUID) ([]PortalQuoteLineDTO, error) {
	rows, err := r.db.GetExecutor(ctx).Query(ctx, `
		SELECT ql.id, ql.product_id, COALESCE(ql.sku, ''), COALESCE(ql.description, ''),
		       COALESCE(ql.customer_note, ''), ql.quantity::float8, ql.uom::text,
		       ql.unit_price::float8, ql.line_total::float8
		FROM quote_lines ql
		WHERE ql.quote_id = $1
		ORDER BY ql.created_at ASC
	`, quoteID)
	if err != nil {
		return nil, fmt.Errorf("failed to list quote lines: %w", err)
	}
	defer rows.Close()

	lines := make([]PortalQuoteLineDTO, 0)
	for rows.Next() {
		var l PortalQuoteLineDTO
		if err := rows.Scan(&l.ID, &l.ProductID, &l.ProductSKU, &l.Description,
			&l.CustomerNote, &l.Quantity, &l.UOM, &l.UnitPrice, &l.LineTotal); err != nil {
			return nil, fmt.Errorf("failed to scan quote line: %w", err)
		}
		l.IsSpecialOrder = l.ProductID == nil
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("quote line rows error: %w", err)
	}
	return lines, nil
}
