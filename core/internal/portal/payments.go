// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package portal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/gablelbm/gable/internal/account"
	"github.com/gablelbm/gable/internal/payment"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// All payment.PaymentGateway usage for the contractor portal lives in this
// single file so any future M29 gateway work touches only one portal file.

type stubPaymentGateway struct{}

// NewStubPaymentGateway returns the deterministic test/stub PaymentGateway
// used by the contractor portal. It never makes external network calls.
// Charges are approved unless AmountCents <= 0 or AmountCents % 100 == 13.
func NewStubPaymentGateway() payment.PaymentGateway {
	return &stubPaymentGateway{}
}

func defaultPortalPaymentGateway() payment.PaymentGateway {
	return NewStubPaymentGateway()
}

// WithPaymentGateway overrides the portal's PaymentGateway (for tests or
// custom stub wiring).
func (s *Service) WithPaymentGateway(gw payment.PaymentGateway) *Service {
	s.paymentGateway = gw
	return s
}

func (s *Service) gateway() payment.PaymentGateway {
	if gw, ok := s.paymentGateway.(payment.PaymentGateway); ok && gw != nil {
		return gw
	}
	return NewStubPaymentGateway()
}

func (g *stubPaymentGateway) Charge(_ context.Context, req payment.ChargeRequest) (*payment.GatewayResult, error) {
	if req.AmountCents <= 0 || req.AmountCents%100 == 13 {
		return &payment.GatewayResult{
			Status:      payment.GatewayStatusDeclined,
			AmountCents: req.AmountCents,
		}, nil
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s", req.CustomerID, req.TokenID, req.AmountCents, req.InvoiceID)))
	txID := "stub_tx_" + hex.EncodeToString(sum[:8])
	return &payment.GatewayResult{
		TransactionID: txID,
		Status:        payment.GatewayStatusApproved,
		AuthCode:      "STUBOK",
		CardBrand:     "VISA",
		CardLast4:     "4242",
		AmountCents:   req.AmountCents,
	}, nil
}

func (g *stubPaymentGateway) Capture(_ context.Context, gatewayTxID string, amountCents int64) (*payment.GatewayResult, error) {
	return &payment.GatewayResult{
		TransactionID: gatewayTxID,
		Status:        payment.GatewayStatusApproved,
		AmountCents:   amountCents,
	}, nil
}

func (g *stubPaymentGateway) Void(_ context.Context, gatewayTxID string) (*payment.GatewayResult, error) {
	return &payment.GatewayResult{
		TransactionID: gatewayTxID,
		Status:        payment.GatewayStatusVoided,
	}, nil
}

func (g *stubPaymentGateway) Refund(_ context.Context, gatewayTxID string, amountCents int64) (*payment.GatewayResult, error) {
	return &payment.GatewayResult{
		TransactionID: gatewayTxID,
		Status:        payment.GatewayStatusRefunded,
		AmountCents:   amountCents,
	}, nil
}

// --- DTOs -------------------------------------------------------------------

// PortalPaymentMethodDTO is a saved payment method on a contractor account.
// GatewayToken carries json:"-" so a vault token can never be serialized back.
type PortalPaymentMethodDTO struct {
	ID           uuid.UUID `json:"id"`
	CustomerID   uuid.UUID `json:"customer_id"`
	Gateway      string    `json:"gateway"`
	GatewayToken string    `json:"-"`
	MethodType   string    `json:"method_type"`
	Brand        string    `json:"brand,omitempty"`
	Last4        string    `json:"last4"`
	ExpMonth     *int      `json:"exp_month,omitempty"`
	ExpYear      *int      `json:"exp_year,omitempty"`
	IsDefault    bool      `json:"is_default"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
}

type CreatePortalPaymentMethodRequest struct {
	GatewayToken string `json:"gateway_token"`
	MethodType   string `json:"method_type"`
	Brand        string `json:"brand"`
	Last4        string `json:"last4"`
	ExpMonth     *int   `json:"exp_month,omitempty"`
	ExpYear      *int   `json:"exp_year,omitempty"`
	IsDefault    bool   `json:"is_default"`
}

type CreatePortalPaymentRequest struct {
	InvoiceIDs      []uuid.UUID `json:"invoice_ids"`
	PaymentMethodID *uuid.UUID  `json:"payment_method_id,omitempty"`
	Token           string      `json:"token,omitempty"`
	IdempotencyKey  string      `json:"idempotency_key,omitempty"`
}

// PortalPaymentAllocationDTO is one invoice allocation of a portal payment,
// with all money in integer cents and zero internal staff attribution.
type PortalPaymentAllocationDTO struct {
	ID            uuid.UUID `json:"id"`
	InvoiceID     uuid.UUID `json:"invoice_id"`
	InvoiceNumber string    `json:"invoice_number"`
	AmountCents   int64     `json:"amount_cents"`
	AppliedOn     string    `json:"applied_on"`
	CreatedAt     time.Time `json:"created_at"`
}

// PortalPaymentDTO is a customer-facing payment with its invoice allocations.
type PortalPaymentDTO struct {
	ID             uuid.UUID                    `json:"id"`
	Number         string                       `json:"number"`
	CustomerID     uuid.UUID                    `json:"customer_id"`
	Currency       string                       `json:"currency"`
	Method         string                       `json:"method"`
	Status         string                       `json:"status"`
	AmountCents    int64                        `json:"amount_cents"`
	UnappliedCents int64                        `json:"unapplied_cents"`
	Reference      string                       `json:"reference,omitempty"`
	CardBrand      string                       `json:"card_brand,omitempty"`
	CardLast4      string                       `json:"card_last4,omitempty"`
	ReceivedOn     string                       `json:"received_on"`
	CreatedAt      time.Time                    `json:"created_at"`
	Allocations    []PortalPaymentAllocationDTO `json:"allocations"`
}

// --- Raw Card / Bank Account Guard ------------------------------------------

var forbiddenRawCredentialFields = []string{
	"card_number",
	"cardnumber",
	"pan",
	"card_pan",
	"account_number",
	"accountnumber",
	"bank_account",
	"routing_number",
	"routingnumber",
	"aba_routing",
	"cvv",
	"cvc",
	"security_code",
}

// rejectRawCardOrBankJSON inspects raw JSON keys to ensure no caller sends raw
// card PANs, CVVs, or bank account/routing numbers.
func rejectRawCardOrBankJSON(rawBody []byte) error {
	if len(rawBody) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(rawBody, &m); err != nil {
		return nil
	}
	for k := range m {
		lower := strings.ToLower(strings.TrimSpace(k))
		for _, bad := range forbiddenRawCredentialFields {
			if lower == bad {
				return ErrRawCardOrBankRefused
			}
		}
	}
	return nil
}

// validateGatewayToken refuses any string that looks like a raw card number
// (PAN) or raw bank account/routing number instead of a gateway token.
func validateGatewayToken(tok string) error {
	t := strings.TrimSpace(tok)
	if t == "" {
		return fmt.Errorf("%w: gateway token is required", ErrInvalidRequest)
	}
	// Strip spaces and hyphens and inspect digit runs.
	var digitsOnly strings.Builder
	hasLetter := false
	consecDigits := 0
	maxConsecDigits := 0
	for _, r := range t {
		if unicode.IsLetter(r) {
			hasLetter = true
			consecDigits = 0
		} else if unicode.IsDigit(r) {
			digitsOnly.WriteRune(r)
			consecDigits++
			if consecDigits > maxConsecDigits {
				maxConsecDigits = consecDigits
			}
		} else if r != '-' && r != ' ' {
			consecDigits = 0
		}
	}
	// A token with no letters (only digits/separators) or 8+ consecutive digits
	// (ignoring spaces/hyphens) looks like a raw PAN or bank account/routing number.
	if !hasLetter || maxConsecDigits >= 8 || digitsOnly.Len() >= 12 {
		return ErrRawCardOrBankRefused
	}
	return nil
}

// --- Saved Payment Methods --------------------------------------------------

func normalizePaymentMethodStatus(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "ACTIVE":
		return "ACTIVE"
	default:
		return "REVOKED"
	}
}

func (s *Service) ListPaymentMethods(ctx context.Context, customerID uuid.UUID) ([]PortalPaymentMethodDTO, error) {
	pgRepo, ok := s.pgRepo()
	if !ok || pgRepo == nil {
		return []PortalPaymentMethodDTO{}, nil
	}
	rows, err := pgRepo.db.GetExecutor(ctx).Query(ctx, `
		SELECT id, customer_id, gateway, method_type, COALESCE(brand, ''), last4,
		       exp_month, exp_year, is_default, status, created_at
		FROM portal_payment_methods
		WHERE customer_id = $1 AND status IN ('ACTIVE', 'active')
		ORDER BY is_default DESC, created_at DESC
	`, customerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list payment methods: %w", err)
	}
	defer rows.Close()

	out := make([]PortalPaymentMethodDTO, 0)
	for rows.Next() {
		var pm PortalPaymentMethodDTO
		if err := rows.Scan(
			&pm.ID, &pm.CustomerID, &pm.Gateway, &pm.MethodType,
			&pm.Brand, &pm.Last4, &pm.ExpMonth, &pm.ExpYear,
			&pm.IsDefault, &pm.Status, &pm.CreatedAt,
		); err != nil {
			return nil, err
		}
		pm.Status = normalizePaymentMethodStatus(pm.Status)
		out = append(out, pm)
	}
	return out, rows.Err()
}

func (s *Service) CreatePaymentMethod(ctx context.Context, customerID uuid.UUID, createdBy *uuid.UUID, rawBody []byte, req CreatePortalPaymentMethodRequest) (*PortalPaymentMethodDTO, error) {
	if err := rejectRawCardOrBankJSON(rawBody); err != nil {
		return nil, err
	}
	if err := validateGatewayToken(req.GatewayToken); err != nil {
		return nil, err
	}
	last4 := strings.TrimSpace(req.Last4)
	if len(last4) < 1 || len(last4) > 4 {
		return nil, fmt.Errorf("%w: last4 must be 1 to 4 characters", ErrInvalidRequest)
	}
	for _, r := range last4 {
		if !unicode.IsDigit(r) {
			return nil, fmt.Errorf("%w: last4 must contain only digits", ErrInvalidRequest)
		}
	}
	mType := strings.ToUpper(strings.TrimSpace(req.MethodType))
	if mType == "" {
		mType = "CARD"
	}
	if mType != "CARD" && mType != "BANK" && mType != "ACH" {
		return nil, fmt.Errorf("%w: method_type must be CARD, ACH, or BANK", ErrInvalidRequest)
	}

	pgRepo, ok := s.pgRepo()
	if !ok || pgRepo == nil {
		return nil, ErrPaymentMethodNotFound
	}

	var out PortalPaymentMethodDTO
	err := pgRepo.InTx(ctx, func(txCtx context.Context) error {
		exec := pgRepo.db.GetExecutor(txCtx)
		var existingCount int
		if err := exec.QueryRow(txCtx, `
			SELECT COUNT(*) FROM portal_payment_methods WHERE customer_id = $1 AND status IN ('ACTIVE', 'active')
		`, customerID).Scan(&existingCount); err != nil {
			return err
		}
		makeDefault := req.IsDefault || existingCount == 0
		if makeDefault {
			if _, err := exec.Exec(txCtx, `
				UPDATE portal_payment_methods SET is_default = FALSE, updated_at = NOW()
				WHERE customer_id = $1 AND status IN ('ACTIVE', 'active') AND is_default = TRUE
			`, customerID); err != nil {
				return err
			}
		}
		var creator *uuid.UUID
		if createdBy != nil && *createdBy != uuid.Nil {
			creator = createdBy
		}
		if err := exec.QueryRow(txCtx, `
			INSERT INTO portal_payment_methods (
				customer_id, created_by, gateway, gateway_token, method_type,
				brand, last4, exp_month, exp_year, is_default, status
			) VALUES ($1, $2, 'stub', $3, $4, $5, $6, $7, $8, $9, 'ACTIVE')
			RETURNING id, customer_id, gateway, method_type, COALESCE(brand, ''), last4,
			          exp_month, exp_year, is_default, status, created_at
		`, customerID, creator, strings.TrimSpace(req.GatewayToken), mType,
			strings.TrimSpace(req.Brand), last4, req.ExpMonth, req.ExpYear, makeDefault).Scan(
			&out.ID, &out.CustomerID, &out.Gateway, &out.MethodType,
			&out.Brand, &out.Last4, &out.ExpMonth, &out.ExpYear,
			&out.IsDefault, &out.Status, &out.CreatedAt,
		); err != nil {
			return err
		}
		out.Status = normalizePaymentMethodStatus(out.Status)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to save payment method: %w", err)
	}
	return &out, nil
}

func (s *Service) DeletePaymentMethod(ctx context.Context, customerID, methodID uuid.UUID) error {
	pgRepo, ok := s.pgRepo()
	if !ok || pgRepo == nil {
		return ErrPaymentMethodNotFound
	}
	tag, err := pgRepo.db.GetExecutor(ctx).Exec(ctx, `
		UPDATE portal_payment_methods
		SET status = 'REVOKED', is_default = FALSE, updated_at = NOW()
		WHERE id = $1 AND customer_id = $2 AND status IN ('ACTIVE', 'active')
	`, methodID, customerID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrPaymentMethodNotFound
	}
	return nil
}

func (s *Service) SetDefaultPaymentMethod(ctx context.Context, customerID, methodID uuid.UUID) (*PortalPaymentMethodDTO, error) {
	pgRepo, ok := s.pgRepo()
	if !ok || pgRepo == nil {
		return nil, ErrPaymentMethodNotFound
	}
	var out PortalPaymentMethodDTO
	err := pgRepo.InTx(ctx, func(txCtx context.Context) error {
		exec := pgRepo.db.GetExecutor(txCtx)
		var exists bool
		if err := exec.QueryRow(txCtx, `
			SELECT EXISTS(SELECT 1 FROM portal_payment_methods WHERE id = $1 AND customer_id = $2 AND status IN ('ACTIVE', 'active'))
		`, methodID, customerID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrPaymentMethodNotFound
		}
		if _, err := exec.Exec(txCtx, `
			UPDATE portal_payment_methods SET is_default = FALSE, updated_at = NOW()
			WHERE customer_id = $1 AND status IN ('ACTIVE', 'active') AND is_default = TRUE
		`, customerID); err != nil {
			return err
		}
		if err := exec.QueryRow(txCtx, `
			UPDATE portal_payment_methods
			SET is_default = TRUE, updated_at = NOW()
			WHERE id = $1 AND customer_id = $2
			RETURNING id, customer_id, gateway, method_type, COALESCE(brand, ''), last4,
			          exp_month, exp_year, is_default, status, created_at
		`, methodID, customerID).Scan(
			&out.ID, &out.CustomerID, &out.Gateway, &out.MethodType,
			&out.Brand, &out.Last4, &out.ExpMonth, &out.ExpYear,
			&out.IsDefault, &out.Status, &out.CreatedAt,
		); err != nil {
			return err
		}
		out.Status = normalizePaymentMethodStatus(out.Status)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// --- Single-Flight Invoice Payment & Allocation History ---------------------

func computePaymentIntentKey(customerID uuid.UUID, invoiceIDs []uuid.UUID, methodID *uuid.UUID, token, explicitKey string) string {
	sorted := make([]string, len(invoiceIDs))
	for i, id := range invoiceIDs {
		sorted[i] = id.String()
	}
	sort.Strings(sorted)
	mStr := ""
	if methodID != nil {
		mStr = methodID.String()
	}
	raw := fmt.Sprintf("portal_pay|%s|%s|%s|%s|%s",
		customerID.String(), strings.Join(sorted, ","), mStr, token, strings.TrimSpace(explicitKey))
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// PayInvoices pays one or more open invoices belonging to customerID through
// the configured PaymentGateway and records the receipt + applications via
// account.Service.RecordPayment, guarded by a durable single-flight row in
// portal_payment_intents so concurrent retries or unknown gateway outcomes
// never charge twice.
func (s *Service) PayInvoices(ctx context.Context, customerID uuid.UUID, actor string, rawBody []byte, req CreatePortalPaymentRequest) (*PortalPaymentDTO, bool, error) {
	if err := rejectRawCardOrBankJSON(rawBody); err != nil {
		return nil, false, err
	}
	if len(req.InvoiceIDs) == 0 {
		return nil, false, fmt.Errorf("%w: at least one invoice_id is required", ErrInvalidRequest)
	}
	seen := make(map[uuid.UUID]bool, len(req.InvoiceIDs))
	for _, id := range req.InvoiceIDs {
		if id == uuid.Nil || seen[id] {
			return nil, false, fmt.Errorf("%w: duplicate or invalid invoice_id", ErrInvalidRequest)
		}
		seen[id] = true
	}

	pgRepo, ok := s.pgRepo()
	if !ok || pgRepo == nil || s.accountSvc == nil {
		return nil, false, fmt.Errorf("payment persistence is not configured")
	}
	exec := pgRepo.db.GetExecutor(ctx)

	var (
		token      string
		methodType = "CARD"
		brand      string
		last4      string
	)
	if req.PaymentMethodID != nil {
		err := exec.QueryRow(ctx, `
			SELECT gateway_token, method_type, COALESCE(brand, ''), last4
			FROM portal_payment_methods
			WHERE id = $1 AND customer_id = $2 AND status IN ('ACTIVE', 'active')
		`, *req.PaymentMethodID, customerID).Scan(&token, &methodType, &brand, &last4)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, ErrPaymentMethodNotFound
		}
		if err != nil {
			return nil, false, err
		}
	} else if strings.TrimSpace(req.Token) != "" {
		if err := validateGatewayToken(req.Token); err != nil {
			return nil, false, err
		}
		token = strings.TrimSpace(req.Token)
	} else {
		return nil, false, fmt.Errorf("%w: provide payment_method_id or token", ErrInvalidRequest)
	}

	idemKey := computePaymentIntentKey(customerID, req.InvoiceIDs, req.PaymentMethodID, token, req.IdempotencyKey)

	type invFact struct {
		id       uuid.UUID
		branchID uuid.UUID
		currency string
		status   string
		open     int64
	}
	var (
		intentID   uuid.UUID
		replayed   *PortalPaymentDTO
		totalCents int64
		branchID   uuid.UUID
		currency   = "USD"
		applyLines []account.ApplyLine
	)

	// Atomically lock target invoices in deterministic order, verify ownership,
	// replay settled idempotent intents, block overlapping active intents (P1-1),
	// verify open/payable status, uniform currency, and absence of pending void
	// holds (P2-1), and claim the CHARGING intent before calling the gateway.
	err := pgRepo.InTx(ctx, func(txCtx context.Context) error {
		txExec := pgRepo.db.GetExecutor(txCtx)

		rows, err := txExec.Query(txCtx, `
			SELECT id, customer_id, branch_id, COALESCE(currency, 'USD'), status,
			       ROUND(COALESCE(amount_open, total_amount) * 100)::bigint
			FROM invoices
			WHERE id = ANY($1)
			ORDER BY id ASC
			FOR UPDATE
		`, req.InvoiceIDs)
		if err != nil {
			return fmt.Errorf("failed to read invoices: %w", err)
		}
		facts := make(map[uuid.UUID]invFact, len(req.InvoiceIDs))
		for rows.Next() {
			var f invFact
			var ownerID uuid.UUID
			if err := rows.Scan(&f.id, &ownerID, &f.branchID, &f.currency, &f.status, &f.open); err != nil {
				rows.Close()
				return err
			}
			if ownerID != customerID {
				rows.Close()
				return ErrInvoiceNotFound
			}
			facts[f.id] = f
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(facts) != len(req.InvoiceIDs) {
			return ErrInvoiceNotFound
		}

		var existingStatus string
		var existingPaymentID *uuid.UUID
		err = txExec.QueryRow(txCtx, `
			SELECT status, payment_id
			FROM portal_payment_intents
			WHERE idempotency_key = $1
			FOR UPDATE
		`, idemKey).Scan(&existingStatus, &existingPaymentID)
		if err == nil {
			switch existingStatus {
			case "SETTLED":
				if existingPaymentID != nil {
					dto, gErr := s.getPortalPaymentByID(txCtx, customerID, *existingPaymentID)
					if gErr == nil {
						replayed = dto
						return nil
					}
				}
			case "CHARGING", "PENDING":
				return ErrPaymentInFlight
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		// Refuse if any target invoice is already covered by another active
		// CHARGING or PENDING intent for this customer (e.g. a concurrent
		// request or a retry after a gateway timeout with a new Idempotency-Key,
		// payment method, or overlapping invoice subset).
		var overlapInFlight bool
		if err := txExec.QueryRow(txCtx, `
			SELECT EXISTS(
				SELECT 1
				FROM portal_payment_intents
				WHERE customer_id = $1
				  AND status IN ('CHARGING', 'PENDING')
				  AND idempotency_key <> $2
				  AND invoice_ids && $3
			)
		`, customerID, idemKey, req.InvoiceIDs).Scan(&overlapInFlight); err != nil {
			return err
		}
		if overlapInFlight {
			return ErrPaymentInFlight
		}

		// Refuse if any target invoice is held by a pending POS void claim.
		var heldByVoid bool
		if err := txExec.QueryRow(txCtx, `
			SELECT EXISTS(
				SELECT 1
				FROM pos_refund_intents i
				JOIN pos_transactions t ON t.id = i.sale_id
				WHERE t.invoice_id = ANY($1)
				  AND i.act = 'void'
				  AND i.state = 'PENDING'
			)
		`, req.InvoiceIDs).Scan(&heldByVoid); err != nil {
			return err
		}
		if heldByVoid {
			return ErrInvoiceNotPayable
		}

		totalCents = 0
		applyLines = make([]account.ApplyLine, 0, len(req.InvoiceIDs))
		for i, id := range req.InvoiceIDs {
			f := facts[id]
			if (f.status != "UNPAID" && f.status != "PARTIAL") || f.open <= 0 {
				return ErrInvoiceNotPayable
			}
			if i == 0 {
				branchID = f.branchID
				currency = f.currency
			} else if f.currency != currency {
				return fmt.Errorf("%w: all invoices in a payment must share the same currency", ErrInvalidRequest)
			}
			totalCents += f.open
			applyLines = append(applyLines, account.ApplyLine{
				InvoiceID:   f.id,
				AmountCents: f.open,
			})
		}

		err = txExec.QueryRow(txCtx, `
			INSERT INTO portal_payment_intents (
				idempotency_key, customer_id, amount_cents, invoice_ids, status, gateway, attempts
			) VALUES ($1, $2, $3, $4, 'CHARGING', 'stub', 1)
			ON CONFLICT (idempotency_key) DO UPDATE
			SET status = 'CHARGING',
			    attempts = portal_payment_intents.attempts + 1,
			    updated_at = NOW()
			WHERE portal_payment_intents.status IN ('FAILED', 'ABANDONED')
			RETURNING id
		`, idemKey, customerID, totalCents, req.InvoiceIDs).Scan(&intentID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrPaymentInFlight
		}
		if err != nil {
			return fmt.Errorf("failed to claim payment intent: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if replayed != nil {
		return replayed, true, nil
	}

	// Charge the gateway outside the DB transaction.
	gwRes, gwErr := s.gateway().Charge(ctx, payment.ChargeRequest{
		TokenID:     token,
		AmountCents: totalCents,
		Currency:    currency,
		Description: fmt.Sprintf("Portal payment (%d invoices)", len(req.InvoiceIDs)),
		InvoiceID:   req.InvoiceIDs[0].String(),
		CustomerID:  customerID.String(),
	})
	if gwErr != nil {
		// Unknown outcome: keep status = 'CHARGING' so a blind retry is refused
		// with 409 rather than double-charging the customer.
		_, _ = exec.Exec(ctx, `
			UPDATE portal_payment_intents
			SET last_error = $2, updated_at = NOW()
			WHERE id = $1
		`, intentID, gwErr.Error())
		return nil, false, fmt.Errorf("%w: %v", ErrPaymentGatewayUnavailable, gwErr)
	}
	if gwRes.Status != payment.GatewayStatusApproved {
		_, _ = exec.Exec(ctx, `
			UPDATE portal_payment_intents
			SET status = 'FAILED', last_error = $2, updated_at = NOW()
			WHERE id = $1
		`, intentID, string(gwRes.Status))
		return nil, false, ErrPaymentDeclined
	}

	_, _ = exec.Exec(ctx, `
		UPDATE portal_payment_intents
		SET gateway_ref = $2, updated_at = NOW()
		WHERE id = $1
	`, intentID, gwRes.TransactionID)

	if brand == "" {
		brand = gwRes.CardBrand
	}
	if last4 == "" {
		last4 = gwRes.CardLast4
	}
	arMethod := "CARD"
	if methodType == "BANK" || methodType == "ACH" {
		arMethod = "ACH"
	}

	var paymentID uuid.UUID
	err = pgRepo.InTx(ctx, func(txCtx context.Context) error {
		on, err := s.accountSvc.LocalDate(txCtx, branchID, time.Now().UTC())
		if err != nil {
			on = time.Now().UTC()
		}
		id, fx, err := s.accountSvc.RecordPayment(txCtx, account.RecordPaymentIn{
			CustomerID:  customerID,
			BranchID:    branchID,
			Currency:    currency,
			Method:      arMethod,
			AmountCents: totalCents,
			Reference:   "Portal:" + gwRes.TransactionID,
			ReceivedOn:  on,
			Card: &account.CardFacts{
				GatewayTxID:   gwRes.TransactionID,
				GatewayStatus: string(gwRes.Status),
				TokenID:       token,
				Last4:         last4,
				Brand:         brand,
				AuthCode:      gwRes.AuthCode,
			},
			Actor:        actor,
			Applications: applyLines,
		})
		if err != nil {
			return err
		}
		if err := s.accountSvc.RecordEvents(txCtx, fx.Events()); err != nil {
			return err
		}
		paymentID = id
		_, err = pgRepo.db.GetExecutor(txCtx).Exec(txCtx, `
			UPDATE portal_payment_intents
			SET status = 'SETTLED', payment_id = $2, updated_at = NOW()
			WHERE id = $1
		`, intentID, paymentID)
		return err
	})
	if err != nil {
		return nil, false, fmt.Errorf("failed to record settled payment: %w", err)
	}

	dto, err := s.getPortalPaymentByID(ctx, customerID, paymentID)
	if err != nil {
		return nil, false, err
	}
	return dto, false, nil
}

func (s *Service) getPortalPaymentByID(ctx context.Context, customerID, paymentID uuid.UUID) (*PortalPaymentDTO, error) {
	list, err := s.listPortalPaymentsWhere(ctx, customerID, &paymentID)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("payment not found")
	}
	return &list[0], nil
}

// ListPortalPayments returns the customer's payment history and invoice
// allocations in integer cents, omitting internal staff attribution.
func (s *Service) ListPortalPayments(ctx context.Context, customerID uuid.UUID) ([]PortalPaymentDTO, error) {
	return s.listPortalPaymentsWhere(ctx, customerID, nil)
}

func (s *Service) listPortalPaymentsWhere(ctx context.Context, customerID uuid.UUID, onlyPaymentID *uuid.UUID) ([]PortalPaymentDTO, error) {
	pgRepo, ok := s.pgRepo()
	if !ok || pgRepo == nil {
		return []PortalPaymentDTO{}, nil
	}
	exec := pgRepo.db.GetExecutor(ctx)

	rows, err := exec.Query(ctx, `
		SELECT
			p.id,
			COALESCE(p.number, ''),
			p.customer_id,
			COALESCE(p.currency, 'USD'),
			p.method,
			p.status,
			ROUND(p.amount * 100)::bigint,
			ROUND(p.amount_unapplied * 100)::bigint,
			COALESCE(p.reference, ''),
			COALESCE(p.card_brand, ''),
			COALESCE(p.card_last4, ''),
			p.received_on::text,
			p.created_at
		FROM payments p
		WHERE p.customer_id = $1
		  AND ($2::uuid IS NULL OR p.id = $2)
		ORDER BY p.created_at DESC
	`, customerID, onlyPaymentID)
	if err != nil {
		return nil, fmt.Errorf("failed to list payments: %w", err)
	}
	defer rows.Close()

	out := make([]PortalPaymentDTO, 0)
	byID := make(map[uuid.UUID]int)
	var ids []uuid.UUID
	for rows.Next() {
		var p PortalPaymentDTO
		if err := rows.Scan(
			&p.ID, &p.Number, &p.CustomerID, &p.Currency, &p.Method, &p.Status,
			&p.AmountCents, &p.UnappliedCents, &p.Reference,
			&p.CardBrand, &p.CardLast4, &p.ReceivedOn, &p.CreatedAt,
		); err != nil {
			return nil, err
		}
		p.Allocations = []PortalPaymentAllocationDTO{}
		byID[p.ID] = len(out)
		ids = append(ids, p.ID)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return out, nil
	}

	appRows, err := exec.Query(ctx, `
		SELECT
			a.id,
			a.payment_id,
			a.invoice_id,
			COALESCE(i.number, ''),
			ROUND(a.amount * 100)::bigint,
			a.applied_on::text,
			a.created_at
		FROM ar_applications a
		LEFT JOIN invoices i ON i.id = a.invoice_id
		WHERE a.customer_id = $1
		  AND a.payment_id = ANY($2)
		  AND a.reversed_at IS NULL
		ORDER BY a.created_at ASC
	`, customerID, ids)
	if err != nil {
		return nil, fmt.Errorf("failed to read payment allocations: %w", err)
	}
	defer appRows.Close()

	for appRows.Next() {
		var payID uuid.UUID
		var al PortalPaymentAllocationDTO
		if err := appRows.Scan(
			&al.ID, &payID, &al.InvoiceID, &al.InvoiceNumber,
			&al.AmountCents, &al.AppliedOn, &al.CreatedAt,
		); err != nil {
			return nil, err
		}
		if idx, exists := byID[payID]; exists {
			out[idx].Allocations = append(out[idx].Allocations, al)
		}
	}
	return out, appRows.Err()
}
