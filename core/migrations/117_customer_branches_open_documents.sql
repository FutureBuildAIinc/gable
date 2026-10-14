-- SPDX-License-Identifier: LicenseRef-OpenLBM-Commons-1.0
-- SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

-- Migration: 117_customer_branches_open_documents
-- Description: ruling A (ADR 0007 section 5.8) makes customer_branches the
-- record of the yards a customer trades at, and every create from now on adds
-- the link with its first document. Documents written before that never did,
-- so a customer with open work at a yard it holds no link to is missing from
-- that yard's walled reads. This adds the missing link for OPEN documents
-- only: quotes in DRAFT or SENT, orders not fulfilled or cancelled, invoices
-- with an open balance, credit memos that are drafts or hold an open amount,
-- and payments with unapplied cash. The yard is the document's own branch.
-- Closed history needs nothing. It is idempotent (a link it has set is never
-- visited again), moves no money, deletes nothing and writes one audit row per
-- link with the system actor, naming the document that made it. A document
-- with a customer and no branch is left alone and counted by a NOTICE.

DO $$
DECLARE
    set_count    INTEGER;
    audit_count  INTEGER;
    no_branch    INTEGER;
BEGIN
    WITH open_docs AS (
        SELECT customer_id, branch_id, 'quote' AS document_type, id::text AS document_id
          FROM quotes WHERE state IN ('DRAFT', 'SENT')
        UNION ALL
        SELECT customer_id, branch_id, 'order', id::text
          FROM orders WHERE status NOT IN ('FULFILLED', 'CANCELLED')
        UNION ALL
        SELECT customer_id, branch_id, 'invoice', id::text
          FROM invoices
         WHERE status NOT IN ('VOID', 'WRITTEN_OFF', 'PAID')
           AND COALESCE(amount_open, total_amount) > 0
        UNION ALL
        SELECT customer_id, branch_id, 'credit_memo', id::text
          FROM credit_memos
         WHERE status <> 'VOID' AND (status = 'DRAFT' OR COALESCE(amount_open, 0) <> 0)
        UNION ALL
        SELECT customer_id, branch_id, 'payment', id::text
          FROM payments WHERE status = 'POSTED' AND COALESCE(amount_unapplied, 0) > 0
    ),
    picked AS (
        SELECT DISTINCT ON (d.customer_id, d.branch_id) d.customer_id, d.branch_id, d.document_type, d.document_id
          FROM open_docs d
         WHERE d.customer_id IS NOT NULL AND d.branch_id IS NOT NULL
           AND NOT EXISTS (SELECT 1 FROM customer_branches cb
                            WHERE cb.customer_id = d.customer_id AND cb.branch_id = d.branch_id)
         ORDER BY d.customer_id, d.branch_id, d.document_type, d.document_id
    ),
    linked AS (
        INSERT INTO customer_branches (customer_id, branch_id)
        SELECT customer_id, branch_id FROM picked
        ON CONFLICT DO NOTHING
        RETURNING customer_id, branch_id
    ),
    audited AS (
        INSERT INTO audit_log (action, entity_type, entity_id, user_id, changes, actor_kind, actor_id)
        SELECT 'customer.branch_linked', 'customer', p.customer_id, 'system',
               jsonb_build_object('branch_id', p.branch_id::text, 'document_type', p.document_type,
                                  'document_id', p.document_id, 'source', 'migration 117'),
               'user', 'system'
          FROM picked p JOIN linked l ON l.customer_id = p.customer_id AND l.branch_id = p.branch_id
        RETURNING 1
    )
    SELECT (SELECT COUNT(*) FROM linked), (SELECT COUNT(*) FROM audited) INTO set_count, audit_count;

    SELECT COUNT(*) INTO no_branch FROM (
        SELECT 1 FROM quotes WHERE state IN ('DRAFT', 'SENT') AND customer_id IS NOT NULL AND branch_id IS NULL
        UNION ALL
        SELECT 1 FROM orders WHERE status NOT IN ('FULFILLED', 'CANCELLED') AND customer_id IS NOT NULL AND branch_id IS NULL
        UNION ALL
        SELECT 1 FROM invoices WHERE status NOT IN ('VOID', 'WRITTEN_OFF', 'PAID')
           AND COALESCE(amount_open, total_amount) > 0 AND customer_id IS NOT NULL AND branch_id IS NULL
        UNION ALL
        SELECT 1 FROM credit_memos WHERE status <> 'VOID' AND (status = 'DRAFT' OR COALESCE(amount_open, 0) <> 0)
           AND customer_id IS NOT NULL AND branch_id IS NULL
        UNION ALL
        SELECT 1 FROM payments WHERE status = 'POSTED' AND COALESCE(amount_unapplied, 0) > 0
           AND customer_id IS NOT NULL AND branch_id IS NULL
    ) x;

    RAISE NOTICE '117: set % customer_branches link(s) for open documents, % audit row(s) written', set_count, audit_count;
    IF no_branch > 0 THEN
        RAISE NOTICE '117: % open document(s) name a customer and no branch and were left as they are', no_branch;
    END IF;
END $$;
