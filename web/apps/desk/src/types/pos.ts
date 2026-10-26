// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The counter module on the wire contract (ADR 0005 section 14.2 C2-5): one
// status field in lowercase, every amount an integer of cents, prices at
// scale 4, quantities decimal strings, and human readable POS- and RTN-
// numbers. Money stays integers until the display helper.

export type SaleStatus = 'open' | 'held' | 'completed' | 'voided';
export type TenderMethod = 'cash' | 'check' | 'card' | 'account' | 'store_credit';
/** The kind of ID looked at beside a check or a charge; its number is never taken. */
export type IdType = 'drivers_license' | 'state_id' | 'passport' | 'military_id' | 'other';
export const ID_TYPES: Array<{ value: IdType; label: string }> = [
    { value: 'drivers_license', label: 'Driver\u2019s licence' },
    { value: 'state_id', label: 'State or provincial ID' },
    { value: 'passport', label: 'Passport' },
    { value: 'military_id', label: 'Military ID' },
    { value: 'other', label: 'Other ID' },
];
export type RefundMethod = 'cash' | 'card' | 'account';
export type TillSessionStatus = 'open' | 'pending_signoff' | 'closed';
export type LineType = 'product' | 'kit' | 'component' | 'charge' | 'text';
export type PriceSource = 'price_list' | 'quote' | 'override' | 'manual' | 'none';

/** One line of a counter sale: the shared salesdoc shape (ADR 0005 2.2). */
export interface SaleLine {
    id: string;
    position: number;
    line_type: LineType;
    parent_line_id: string | null;
    product_id: string | null;
    charge_code_id: string | null;
    charge_code: string | null;
    sku: string | null;
    description: string;
    quantity: string | null;
    uom: string | null;
    price_uom: string | null;
    uom_qty: string | null;
    price_uom_qty: string | null;
    unit_price_ten_thousandths: number | null;
    priced_unit_price_ten_thousandths: number | null;
    price_source: PriceSource;
    override_reason: string | null;
    discount_percent: string | null;
    discount_cents: number | null;
    discount_reason: string | null;
    price_adjusted_by: string | null;
    line_total_cents: number | null;
    taxable: boolean;
    revenue_account_code: string | null;
    is_special_order: boolean;
    vendor_id: string | null;
    special_order_unit_cost_ten_thousandths: number | null;
    created_at: string;
}

/** One payment taken at the counter, stored net of change. */
export interface Tender {
    id: string;
    sale_id: string;
    method: TenderMethod;
    amount_cents: number;
    payment_id: string | null;
    reference: string | null;
    card_last4: string | null;
    card_brand: string | null;
    gateway_tx_id: string | null;
    auth_code: string | null;
    id_checked: boolean;
    id_type: IdType | null;
    created_at: string;
}

export interface Sale {
    id: string;
    number: string;
    revision: number;
    branch_id: string;
    register_id: string;
    cashier_id: string;
    customer_id: string | null;
    currency: string;
    subtotal_cents: number;
    tax_cents: number;
    total_cents: number;
    change_cents: number;
    till_session_id: string | null;
    status: SaleStatus;
    invoice_id: string | null;
    completed_at: string | null;
    created_at: string;
    /** The purchase order and job a charge sale names (null until named). */
    customer_po: string | null;
    job_id: string | null;
    /** The roster buyer a sale on account names. */
    buyer_contact_id: string | null;
    /** How many times the receipt has been printed. */
    print_count: number;
    exchange_return_id: string | null;
    lines: SaleLine[];
    tenders: Tender[];
}

export type AccountStanding = 'good' | 'no_limit' | 'no_credit' | 'over_limit';

export interface CounterJob {
    id: string;
    name: string;
}

/** A customer as the counter's lookup shows it, the account's standing beside it. */
export interface CounterCustomer {
    id: string;
    account_number: string;
    name: string;
    po_required: boolean;
    credit_limit_cents: number | null;
    open_receivable_cents: number;
    available_credit_cents: number | null;
    standing: AccountStanding;
    jobs: CounterJob[];
}

// --- Can this go on account? (GET /api/v1/pos/customers/{id}/standing) ---

export type VerdictReason = '' | 'credit_hold' | 'credit_limit' | 'cash_only_terms' | 'manager_override';

export interface WayForward {
    kind: 'pay_down' | 'manager_override' | 'switch_tender';
    available: boolean;
    /** For a pay down, what paying would clear. */
    amount_cents?: number;
    message: string;
}

export interface OnAccountVerdict {
    verdict: 'allowed' | 'blocked';
    reason: VerdictReason;
    message: string;
    over_cents: number | null;
    ways_forward: WayForward[];
}

export interface Buyer {
    id: string;
    name: string;
    cap_cents: number | null;
}

/** The account as the counter reads it before a sale goes on it. */
export interface CounterStanding {
    customer_id: string;
    name: string;
    currency: string;
    credit_limit_cents: number | null;
    open_receivable_cents: number;
    /** The customer's open store credit, positive. */
    open_credit_memo_cents: number;
    available_credit_cents: number | null;
    on_hold: boolean;
    terms_kind: 'net_days' | 'day_of_month' | 'due_on_receipt';
    named_buyer_required: boolean;
    buyers: Buyer[];
    check_limit_cents: number | null;
    payment_options: TenderMethod[];
    on_account: OnAccountVerdict;
}

export interface SaleSummary {
    id: string;
    number: string;
    revision: number;
    branch_id: string;
    register_id: string;
    cashier_id: string;
    customer_id: string | null;
    currency: string;
    total_cents: number;
    status: SaleStatus;
    invoice_id: string | null;
    completed_at: string | null;
    created_at: string;
    item_count: number;
}

export interface SalePage {
    items: SaleSummary[];
    next_cursor: string | null;
    limit: number;
    total?: number;
}

// --- Returns ---

export interface ReturnLine {
    id: string;
    position: number;
    product_id: string | null;
    description: string;
    quantity: string | null;
    uom: string | null;
    unit_price_ten_thousandths: number | null;
    line_total_cents: number | null;
    restock: boolean;
    reason_code: ReturnReasonCode;
    restocking_fee_cents: number;
}

/** The closed lowercase list of return reasons; an absent reason is other. */
export type ReturnReasonCode = 'defective' | 'damaged' | 'wrong_item' | 'ordered_wrong' | 'changed_mind' | 'overbought' | 'other';

export interface ReturnReason {
    code: ReturnReasonCode;
    label: string;
    /** The fee withheld from the line's refund, in basis points. */
    restocking_fee_bps: number;
    needs_note: boolean;
}

export interface CounterReturn {
    id: string;
    number: string;
    revision: number;
    branch_id: string | null;
    register_id: string;
    till_session_id: string | null;
    original_sale_id: string | null;
    customer_id: string | null;
    cashier_id: string;
    currency: string;
    subtotal_cents: number;
    tax_cents: number;
    total_cents: number;
    refund_method: RefundMethod;
    reason: string;
    credit_memo_id: string | null;
    created_at: string;
    /** The fee withheld from the refund; the subtotal and total already carry it. */
    restocking_fee_cents: number;
    exchange_sale_id: string | null;
    lines: ReturnLine[];
}

/** What an exchange comes to: the credit the return is worth, the replacement and the difference. */
export interface ExchangeFigures {
    returned_cents: number;
    restocking_fee_cents: number;
    replacement_cents: number;
    /** Replacement less return: positive is what the customer pays, negative what goes back. */
    difference_cents: number;
}

export interface Exchange extends ExchangeFigures {
    return: CounterReturn;
    sale: Sale;
    refunded_cents: number;
}

/** A sale's sealed signature (the image is never answered). */
export interface SealedSignature {
    sale_id: string;
    signer_name: string;
    signed_at: string;
    total_cents: number;
    image_sha256: string;
    seal: string;
    captured_by: string;
}

/** A manager's one act for one sale. */
export interface CounterOverride {
    id: string;
    sale_id: string;
    kind: 'account' | 'check';
    approved_by: string;
    reason: string;
    cap_cents: number;
    created_at: string;
    consumed_at: string | null;
}

// --- Till sessions (drawer lifecycle) ---

export interface TillSession {
    id: string;
    register_id: string;
    branch_id: string | null;
    cashier_id: string;
    status: TillSessionStatus;
    opening_float_cents: number;
    opened_at: string;
    closed_at: string | null;
    expected_by_method: Record<string, number>;
    counted_by_method: Record<string, number>;
    over_short_cents: number | null;
    gl_entry_id: string | null;
    notes: string;
    /** Who counted and closed the drawer. */
    closed_by: string | null;
    /** The branch's variance threshold in force at the close. */
    variance_threshold_cents: number | null;
    /** The manager's release of a close that passed the threshold. */
    sign_off: TillSignOff | null;
}

export interface TillSignOff {
    signed_off_by: string;
    reason: string;
    signed_off_at: string;
}

export interface TillReport {
    session: TillSession;
    sale_count: number;
    sales_total_cents: number;
    tax_total_cents: number;
    change_cents: number;
    tendered_by_method: Record<string, number>;
    expected_by_method: Record<string, number>;
    /** What the session refunded per method (CASH out of the drawer, CARD back to the card). */
    refunds_by_method?: Record<string, number>;
    movements: TillMovement[];
    movement_totals_by_kind: Record<string, number>;
}

/** The immutable end of day snapshot written once when a session closes; its payload is the frozen closing report. */
export interface ZReport {
    id: string;
    till_session_id: string;
    register_id: string;
    branch_id: string | null;
    over_short_cents: number;
    payload: TillReport;
    generated_at: string;
}

// --- Till movements, variance sign off and the branch day (CP-08, CP-09) ---

export type MovementKind = 'safe_drop' | 'pickup' | 'paid_in' | 'paid_out';
export type MovementReason =
    | 'supplies' | 'freight' | 'fuel' | 'postage' | 'meals' | 'repairs'
    | 'fee_received' | 'vendor_refund' | 'other';

export interface TillMovement {
    id: string;
    till_session_id: string;
    register_id: string;
    branch_id: string;
    kind: MovementKind;
    amount_cents: number;
    reason_code: MovementReason | null;
    note: string;
    actor: string;
    taken_by: string | null;
    gl_entry_id: string | null;
    created_at: string;
}

export interface MovementRequest {
    kind: MovementKind;
    amount_cents: number;
    reason_code?: MovementReason;
    note?: string;
    taken_by?: string;
    /** A manager's step up approval: the manager it names is the taker. */
    approval_token?: string;
}

/** A manager the step up picker offers: active, a PIN set, a role the act admits. */
export interface StepUpManager {
    id: string;
    full_name: string;
    staff_no: string;
    role: string;
}

/** The single use approval a verified step up mints; one act spends it. */
export interface StepUpApproval {
    token: string;
    manager_name: string;
    expires_at: string;
}

export interface DayFigures {
    session_count: number;
    sale_count: number;
    sales_total_cents: number;
    tendered_by_method: Record<string, number>;
    movement_totals_by_kind: Record<string, number>;
    expected_by_method: Record<string, number>;
    counted_by_method: Record<string, number>;
    over_short_cents: number;
    cash_to_deposit_cents: number;
    checks_to_deposit_cents: number;
    card_total_cents: number;
}

export interface DaySession {
    id: string;
    status: TillSessionStatus;
    cashier_id: string;
    closed_by: string | null;
    opened_at: string;
    closed_at: string | null;
    opening_float_cents: number;
    sale_count: number;
    sales_total_cents: number;
    tendered_by_method: Record<string, number>;
    movement_totals_by_kind: Record<string, number>;
    expected_by_method: Record<string, number>;
    counted_by_method: Record<string, number>;
    over_short_cents: number;
    signed_off_by: string | null;
}

export interface DayRegister extends DayFigures {
    register_id: string;
    sessions: DaySession[];
}

export interface DepositMatch {
    bank_transaction_id: string;
    bank_amount_cents: number;
    bank_transaction_date: string;
    bank_description: string;
    difference_cents: number;
    matched_by: string;
    matched_at: string;
}

export interface DayClose {
    id: string;
    branch_id: string;
    business_date: string;
    deposit_slip_ref: string;
    closed_by: string;
    closed_at: string;
    cash_to_deposit_cents: number;
    checks_to_deposit_cents: number;
    card_total_cents: number;
    bank_account_id: string | null;
    gl_entry_id: string | null;
    deposit_match: DepositMatch | null;
}

export interface BranchDay {
    branch_id: string;
    business_date: string;
    registers: DayRegister[];
    totals: DayFigures;
    open_session_count: number;
    pending_signoff_count: number;
    ready_to_close: boolean;
    collections_net_cents: number;
    close: DayClose | null;
}

export interface DepositCandidate {
    bank_transaction_id: string;
    transaction_date: string;
    amount_cents: number;
    description: string;
    reference: string;
}

// --- Typeahead and offline catalog ---

export interface QuickSearchResult {
    product_id: string;
    sku: string;
    description: string;
    unit_price_cents: number;
    uom: string;
    in_stock: string;
}

/** A register the caller may use, with its branch (GET /api/v1/pos/registers). */
export interface CounterRegister {
    id: string;
    name: string;
    branch_id: string;
    branch_code: string;
    branch_name: string;
}

// --- The card terminal's intents (CP-05) ---

/** One card leg's own record at the terminal (GET /api/v1/pos/terminal-intents/{id}). */
export interface TerminalIntent {
    id: string;
    transaction_id: string;
    leg_key: string;
    kind: 'sale' | 'refund' | 'void';
    state: 'claimed' | 'pending_at_device' | 'authorized' | 'declined' | 'lost';
    amount_cents: number;
    provider_ref: string;
    answer_verdict: '' | 'authorized' | 'declined';
}

/** One open item of the manager's lost answer queue (GET /api/v1/pos/terminal-lost-items). */
export interface TerminalLostItem {
    id: string;
    intent_id: string;
    branch_id: string;
    register_id: string;
    transaction_id: string;
    kind: 'sale' | 'refund' | 'void';
    amount_cents: number;
    device_id: string;
    provider_ref: string;
    state: 'open' | 'resolving' | 'resolved_authorized' | 'resolved_declined';
    /** The device's own late verdict, when its answer came after the sweep. */
    late_verdict: '' | 'authorized' | 'declined';
    late_provider_ref: string;
}

/** What one sweep pass closed and queued (POST /api/v1/pos/terminal-intents/sweep). */
export interface TerminalSweepResult {
    claimed_closed: number;
    dispatched_lost: number;
}
