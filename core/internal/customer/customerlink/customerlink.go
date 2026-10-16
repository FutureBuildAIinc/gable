// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package customerlink keeps customer_branches true to where a customer
// trades (ADR 0007 section 5.8, ruling A): the first document created at a
// yard for a customer not yet linked to it adds the link in the same
// transaction, with one audit row naming the actor, the document and the
// yard. It is a package of its own because pkg/audit reaches pkg/middleware,
// which imports internal/customer.
package customerlink

import (
	"context"
	"errors"
	"fmt"

	"github.com/gablelbm/gable/pkg/audit"
	"github.com/gablelbm/gable/pkg/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Action is the audit action of a link written by trade.
const Action = "customer.branch_linked"

// documentTypes are the tables a link may follow, with the name the audit
// row gives the document.
var documentTypes = map[string]string{
	"quotes":           "quote",
	"orders":           "order",
	"invoices":         "invoice",
	"credit_memos":     "credit_memo",
	"payments":         "payment",
	"pos_transactions": "pos_transaction",
}

// Ensure links the customer of the document row (table, id) to the row's
// branch when it holds no link there, and writes the audit row when it did.
// It goes through the caller's executor, so inside a transaction the link
// and its audit row commit or roll back with the document. The insert is the
// arbiter: of any number of concurrent first documents at one yard exactly
// one inserts the row and writes the audit row. A document with no customer
// or no branch adds nothing.
func Ensure(ctx context.Context, db *database.DB, table string, id uuid.UUID) error {
	docType, ok := documentTypes[table]
	if !ok {
		return fmt.Errorf("customerlink: %q is not a document table", table)
	}
	var customer, branch uuid.UUID
	err := db.GetExecutor(ctx).QueryRow(ctx, `
		INSERT INTO customer_branches (customer_id, branch_id)
		SELECT d.customer_id, d.branch_id FROM `+table+` d
		 WHERE d.id = $1 AND d.customer_id IS NOT NULL AND d.branch_id IS NOT NULL
		ON CONFLICT DO NOTHING
		RETURNING customer_id, branch_id`, id).Scan(&customer, &branch)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("failed to link the customer to the yard: %w", err)
	}
	return audit.NewLogger(db).Log(ctx, audit.Entry{
		Action:     Action,
		EntityType: "customer",
		EntityID:   customer,
		Changes: map[string]any{
			"branch_id":     branch.String(),
			"document_type": docType,
			"document_id":   id.String(),
		},
	})
}
