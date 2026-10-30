// SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// Package emaildelivery records every customer or vendor facing email the
// ERP sends (recipient, subject, the document carried, the sender's answer and
// any error) and serves the record, walled by the carried document's branch.
package emaildelivery

import (
	"context"

	"github.com/gablelbm/gable/internal/platform/httpx"
	"github.com/google/uuid"
)

// The document kinds a delivery can carry, and the statuses a row moves
// through: queued the moment a sending route answers 202, then sent (the SMTP
// server took it), logged (a log sender took it: nothing left the process) or
// failed (the send errored).
const (
	DocInvoice       = "invoice"
	DocStatement     = "statement"
	DocQuote         = "quote"
	DocPurchaseOrder = "purchase_order"

	StatusQueued = "queued"
	StatusSent   = "sent"
	StatusLogged = "logged"
	StatusFailed = "failed"
)

// Delivery is one recorded send, as the wire carries it.
type Delivery struct {
	ID             uuid.UUID       `json:"id"`
	CreatedAt      httpx.Timestamp `json:"created_at"`
	BranchID       *uuid.UUID      `json:"branch_id"`
	Recipient      string          `json:"recipient"`
	Subject        string          `json:"subject"`
	DocumentType   string          `json:"document_type"`
	DocumentID     *uuid.UUID      `json:"document_id"`
	DocumentNumber *string         `json:"document_number"`
	Status         string          `json:"status"`
	SenderAnswer   *string         `json:"sender_answer"`
	Error          *string         `json:"error"`
	SentBy         *string         `json:"sent_by"`
}

// Entry is what a sending route hands to Record.
type Entry struct {
	BranchID       *uuid.UUID
	Recipient      string
	Subject        string
	DocumentType   string
	DocumentID     *uuid.UUID
	DocumentNumber string
	Status         string
	SenderAnswer   string
	Error          string
}

// Recorder is what a sending route needs: the queued row its 202 names, and
// the one outcome the send resolves that row to.
type Recorder interface {
	Record(ctx context.Context, e Entry) (*Delivery, error)
	SetOutcome(ctx context.Context, id uuid.UUID, status, senderAnswer, sendError string) error
}

// Filter narrows the list.
type Filter struct {
	DocumentType string
	Status       string
	DocumentID   *uuid.UUID
}
