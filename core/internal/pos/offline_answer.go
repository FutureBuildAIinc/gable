// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

package pos

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/gablelbm/gable/pkg/audit"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// The clerk's answer on reconnect (ADR 0011 5.3, slice C2): an offline sale
// that kept the default link to the cart whose result was unknown, and
// abandoned it at the sync, is asked about once, "Was this the same customer
// and goods?". The answer is recorded on the offline sale: a no marks it as
// for a different customer, which lists it for the manager on the branch day
// page beside the abandoned cart; either answer is audited.

// ReplacedCartAnswer is the recorded answer.
type ReplacedCartAnswer struct {
	SaleID         uuid.UUID `json:"sale_id"`
	ReplacedSaleID uuid.UUID `json:"replaced_sale_id"`
	SameGoods      bool      `json:"same_goods"`
}

type replacedCartAnswerRequest struct {
	SameGoods *bool `json:"same_goods"`
}

// replacedCartFacts is what the answer reads of the offline sale under its
// row lock.
type replacedCartFacts struct {
	OfflineRef     *string
	ReplacesSaleID *uuid.UUID
	Declared       bool
	ReplacedStatus string
	Answered       bool
}

// ReplacedCartFacts reads an offline sale's link to the cart it replaced,
// that cart's status and whether the answer is already recorded.
func (r *PostgresRepository) ReplacedCartFacts(ctx context.Context, saleID uuid.UUID) (*replacedCartFacts, error) {
	f := &replacedCartFacts{}
	err := r.ex(ctx).QueryRow(ctx, `SELECT t.offline_ref, t.replaces_sale_id, COALESCE(t.different_customer, FALSE),
			COALESCE(c.status, ''),
			EXISTS (SELECT 1 FROM audit_log a WHERE a.action = 'pos.offline_sale.replaced_cart_answered' AND a.entity_id = t.id)
		FROM pos_transactions t LEFT JOIN pos_transactions c ON c.id = t.replaces_sale_id
		WHERE t.id = $1`, saleID).Scan(&f.OfflineRef, &f.ReplacesSaleID, &f.Declared, &f.ReplacedStatus, &f.Answered)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFound("sale not found")
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read the offline sale's replaced cart: %w", err)
	}
	return f, nil
}

// MarkDifferentCustomer records an offline sale as for a different customer
// than the cart it replaced.
func (r *PostgresRepository) MarkDifferentCustomer(ctx context.Context, saleID uuid.UUID) error {
	if _, err := r.ex(ctx).Exec(ctx, `UPDATE pos_transactions SET different_customer = TRUE, updated_at = NOW() WHERE id = $1`, saleID); err != nil {
		return fmt.Errorf("failed to record the answer on the sale: %w", err)
	}
	return nil
}

// AnswerReplacedCart records the clerk's answer for an offline sale that
// replaced and abandoned a cart, once, under the sale's row lock.
func (s *Service) AnswerReplacedCart(ctx context.Context, saleID uuid.UUID, sameGoods bool, actor string) (*ReplacedCartAnswer, error) {
	if err := s.holdSaleBranch(ctx, saleID); err != nil {
		return nil, err
	}
	var out *ReplacedCartAnswer
	err := s.inTx(ctx, func(ctx context.Context) error {
		if err := s.repo.LockSale(ctx, saleID); err != nil {
			return err
		}
		f, err := s.repo.ReplacedCartFacts(ctx, saleID)
		if err != nil {
			return err
		}
		switch {
		case f.OfflineRef == nil || *f.OfflineRef == "" || f.ReplacesSaleID == nil:
			return conflict("not_replacing", "only an offline sale that replaced a cart is asked whether it was the same customer and goods")
		case f.ReplacedStatus != "ABANDONED":
			return conflict("cart_not_abandoned", "the cart this sale named was not abandoned by it, so there is nothing to compare")
		case f.Declared:
			return conflict("already_declared", "this sale was declared for a different customer when it was made")
		case f.Answered:
			return conflict("answer_recorded", "the answer for this sale is already recorded")
		}
		if !sameGoods {
			if err := s.repo.MarkDifferentCustomer(ctx, saleID); err != nil {
				return err
			}
		}
		if s.auditLog != nil {
			if err := s.auditLog.Log(ctx, audit.Entry{
				Action: "pos.offline_sale.replaced_cart_answered", EntityType: "pos_transaction", EntityID: saleID, UserID: actor,
				Changes: map[string]any{"same_goods": sameGoods, "replaced_sale_id": f.ReplacesSaleID.String(),
					"offline_ref": *f.OfflineRef}}); err != nil {
				return fmt.Errorf("failed to write the audit row: %w", err)
			}
		}
		out = &ReplacedCartAnswer{SaleID: saleID, ReplacedSaleID: *f.ReplacesSaleID, SameGoods: sameGoods}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AnswerReplacedCartHandler is POST /pos/transactions/{id}/replaced-cart-answer:
// a person's act at the counter, never a machine key.
func (h *Handler) AnswerReplacedCartHandler(w http.ResponseWriter, r *http.Request) {
	if refuseMachineKey(w, r, "the clerk's answer is a person's act at the counter, never a machine key") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation", httpx.FieldError{Field: "id", Message: "must be a UUID"}))
		return
	}
	var req replacedCartAnswerRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if req.SameGoods == nil {
		httpx.WriteError(w, r, httpx.BadRequest("one or more fields failed validation",
			httpx.FieldError{Field: "same_goods", Message: "is required: true for the same customer and goods, false for another"}))
		return
	}
	out, err := h.service.AnswerReplacedCart(r.Context(), id, *req.SameGoods, h.actorFrom(r))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
