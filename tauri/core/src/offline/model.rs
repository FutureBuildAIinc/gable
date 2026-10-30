// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! Data models for the offline counter snapshot, keychain custody records,
//! signed sale envelope, and queue state (ADR 0011 sections 3, 4, and 5).

use serde::{Deserialize, Serialize};
use zeroize::Zeroize;

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct OfflineSnapshot {
    pub snapshot_id: String,
    pub register_id: String,
    pub branch_id: String,
    pub device_epoch: i64,
    pub issued_at: String,
    pub till_session: SnapshotTillSession,
    pub policy: SnapshotPolicy,
    pub tax: SnapshotTax,
    pub walk_in_customer: SnapshotWalkIn,
    pub products: Vec<SnapshotProduct>,
    pub price_sheet: Vec<SnapshotPriceRow>,
    pub accounts: Vec<SnapshotAccount>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SnapshotTillSession {
    pub id: String,
    pub cashier_id: String,
    pub opened_at: String,
    pub business_date: String,
    pub opening_float_cents: i64,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SnapshotPolicy {
    pub enabled: bool,
    pub max_sale_cents: i64,
    pub max_check_cents: i64,
    pub default_check_limit_cents: Option<i64>,
    pub account_cap_cents: i64,
    pub max_queue_count: i64,
    pub max_queue_total_cents: i64,
    pub allowed_tenders: Vec<String>,
    pub require_id_for_tenders: Vec<String>,
    #[serde(default)]
    pub allow_price_override: bool,
    #[serde(default)]
    pub allow_discount: bool,
    /// The branch's cash rounding rule (CP-18, `locations.cash_rounding_mode`):
    /// the core rounds the cash leg under it, as the live counter does.
    #[serde(default)]
    pub cash_rounding_mode: CashRoundingMode,
}

/// A branch's cash rounding rule, the twin of `pos.RoundingMode`.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub enum CashRoundingMode {
    /// The cash is taken exactly as tendered.
    #[default]
    #[serde(rename = "OFF")]
    Off,
    /// The cash leg rounds to the nearest five cents, symmetrically.
    #[serde(rename = "NEAREST_5_CENTS")]
    Nearest5Cents,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SnapshotTax {
    pub branch_rate: String,
    pub provider_configured: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SnapshotWalkIn {
    pub id: String,
    pub currency: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SnapshotProduct {
    pub id: String,
    pub sku: String,
    pub barcode: Option<String>,
    pub description: String,
    pub category: String,
    pub taxable: bool,
    pub is_kit: bool,
    pub random_length: bool,
    pub board_thickness_in: Option<String>,
    pub board_width_in: Option<String>,
    pub stock_uom: String,
    pub sale_uom: String,
    pub price_uom: String,
    pub in_stock: String,
    pub unit_set: Vec<SnapshotUnitRow>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SnapshotUnitRow {
    pub uom: String,
    pub unit_qty: String,
    pub stock_qty: String,
    pub sell: bool,
    pub price: bool,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SnapshotPriceRow {
    pub product_id: String,
    pub price_key: String,
    pub price_uom: String,
    pub uom_qty: String,
    pub price_uom_qty: String,
    pub unit_price_ten_thousandths: i64,
    pub price_source: String,
    pub price_basis: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SnapshotAccount {
    pub customer_id: String,
    pub account_number: String,
    pub name: String,
    pub currency: String,
    pub tax_exempt: bool,
    pub po_required: bool,
    pub buyer_roster_required: bool,
    pub credit_hold: bool,
    pub credit_limit_cents: i64,
    pub open_receivable_cents: i64,
    pub offline_room_cents: i64,
    pub check_limit_cents: Option<i64>,
    pub price_key: String,
    pub level_key: Option<String>,
    pub jobs: Vec<SnapshotJob>,
    pub buyers: Vec<SnapshotBuyer>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SnapshotJob {
    pub id: String,
    pub code: String,
    pub name: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SnapshotBuyer {
    pub contact_id: String,
    pub name: String,
    pub per_purchase_cap_cents: Option<i64>,
}

/// Keychain enrolment record (`offline-counter:enrolment`). The private
/// Ed25519 signing seed stays inside the Rust core and never crosses IPC.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct EnrolmentRecord {
    pub register_id: String,
    pub device_epoch: i64,
    pub signing_seed_hex: String,
    pub public_key_hex: String,
    pub gable_url: String,
    /// The epoch this enrolment replaced and the last sequence the device
    /// assigned under it, kept for the unknown tail report (ADR 0011 5.1).
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub previous_epoch: Option<PriorEpochMark>,
}

/// The keychain mark of the epoch a re-enrolment replaced.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct PriorEpochMark {
    pub device_epoch: i64,
    pub last_seq: i64,
    pub confirmed_seq: i64,
}

impl Drop for EnrolmentRecord {
    fn drop(&mut self) {
        self.signing_seed_hex.zeroize();
    }
}

/// Public enrolment facts safe to return to the webview or shell setup.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct EnrolmentPublic {
    pub register_id: String,
    pub device_epoch: i64,
    pub public_key_hex: String,
    pub gable_url: String,
    pub last_seq: i64,
    pub confirmed_seq: i64,
    pub chain_head: String,
}

/// Keychain high-water mark (`offline-counter:mark`) used to detect a missing
/// or rolled-back SQLite store under a live enrolment (ADR 0011 section 5.1).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct HighWaterMark {
    pub register_id: String,
    pub device_epoch: i64,
    pub last_seq: i64,
    pub chain_head: String,
    pub confirmed_seq: i64,
}

/// Sealed store metadata row (`store_meta`) held inside the SQLite queue file.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct StoreMeta {
    pub register_id: String,
    pub device_epoch: i64,
    pub last_seq: i64,
    pub chain_head: String,
    pub confirmed_seq: i64,
}

/// Request to sign and queue one offline sale in the Rust core (ADR 0011
/// sections 4.1 and 5.1). Any request that names `device_seq` or `prev_digest`
/// or whose asserted price, tax figure, price key, price unit, or customer
/// differs from what the core derives from its own snapshot copy is refused.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct SignSaleRequest {
    pub client_id: String,
    pub snapshot_id: String,
    pub till_session_id: String,
    pub cashier_id: String,
    pub customer_id: Option<String>,
    pub buyer_contact_id: Option<String>,
    pub customer_po: Option<String>,
    pub job_id: Option<String>,
    pub picked_up_by: Option<String>,
    pub business_date: String,
    pub sold_at: String,
    pub lines: Vec<SignLineRequest>,
    pub tenders: Vec<SignTenderRequest>,
    /// The open server cart this sale replaces, or the result unknown cart it
    /// defaults to (ADR 0011 section 5.3). Signed when present.
    #[serde(default)]
    pub replaces_sale_id: Option<String>,
    /// The clerk's declaration that the sale is for another customer than the
    /// result unknown cart's; it then names that cart in `unknown_cart_id`.
    #[serde(default)]
    pub different_customer: bool,
    #[serde(default)]
    pub unknown_cart_id: Option<String>,

    #[serde(default)]
    pub device_seq: Option<i64>,
    #[serde(default)]
    pub prev_digest: Option<String>,
    #[serde(default)]
    pub subtotal_cents: Option<i64>,
    #[serde(default)]
    pub tax_cents: Option<i64>,
    #[serde(default)]
    pub total_cents: Option<i64>,
    #[serde(default)]
    pub tax_rate: Option<String>,
    #[serde(default)]
    pub tax_exempt: Option<bool>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct SignLineRequest {
    pub id: String,
    pub product_id: String,
    #[serde(default)]
    pub line_type: Option<String>,
    #[serde(default)]
    pub charge_code: Option<String>,
    #[serde(default)]
    pub quantity: Option<String>,
    pub uom: String,
    #[serde(default)]
    pub tally: Option<TallyRequestInput>,
    #[serde(default)]
    pub override_reason: Option<String>,
    #[serde(default)]
    pub discount_percent: Option<String>,
    #[serde(default)]
    pub discount_cents: Option<i64>,

    #[serde(default)]
    pub price_key: Option<String>,
    #[serde(default)]
    pub price_uom: Option<String>,
    #[serde(default)]
    pub uom_qty: Option<String>,
    #[serde(default)]
    pub price_uom_qty: Option<String>,
    #[serde(default)]
    pub unit_price_ten_thousandths: Option<i64>,
    #[serde(default)]
    pub line_total_cents: Option<i64>,
    #[serde(default)]
    pub taxable: Option<bool>,
    #[serde(default)]
    pub stock_uom: Option<String>,
    #[serde(default)]
    pub stock_quantity: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct TallyRequestInput {
    pub rows: Vec<TallyRowRequestInput>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct TallyRowRequestInput {
    pub pieces: i64,
    pub length_ft: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SignTenderRequest {
    pub method: String,
    pub amount_cents: i64,
    #[serde(default)]
    pub reference: Option<String>,
    #[serde(default)]
    pub id_checked: bool,
    #[serde(default)]
    pub id_type: Option<String>,
}

/// The signed offline sale envelope (ADR 0011 section 4.1).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SignedOfflineSale {
    pub client_id: String,
    pub register_id: String,
    pub device_epoch: i64,
    pub device_seq: i64,
    pub prev_digest: String,
    pub offline_ref: String,
    pub snapshot_id: String,
    pub till_session_id: String,
    pub cashier_id: String,
    pub customer_id: Option<String>,
    pub buyer_contact_id: Option<String>,
    pub customer_po: Option<String>,
    pub job_id: Option<String>,
    pub picked_up_by: Option<String>,
    pub business_date: String,
    pub sold_at: String,
    pub currency: String,
    pub subtotal_cents: i64,
    pub tax_cents: i64,
    pub total_cents: i64,
    pub change_cents: i64,
    pub tax_rate: String,
    pub tax_exempt: bool,
    pub queue_count: i64,
    pub queue_total_cents: i64,
    pub confirmed_mark: i64,
    pub lines: Vec<SignedSaleLine>,
    pub tenders: Vec<SignedSaleTender>,
    /// The replaced cart and the declaration (ADR 0011 section 5.3), signed
    /// fields when present: the server builds the canonical form from the
    /// item as received, so absent ones are left out here too.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub replaces_sale_id: Option<String>,
    #[serde(default, skip_serializing_if = "is_false")]
    pub different_customer: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub unknown_cart_id: Option<String>,
    pub device_sig: String,
}

fn is_false(v: &bool) -> bool {
    !*v
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SignedSaleLine {
    pub id: String,
    pub position: usize,
    pub product_id: String,
    pub sku: String,
    pub description: String,
    pub quantity: String,
    pub uom: String,
    pub price_uom: String,
    pub uom_qty: String,
    pub price_uom_qty: String,
    pub stock_uom: String,
    pub stock_quantity: String,
    pub price_key: String,
    pub price_source: String,
    pub price_basis: Option<String>,
    pub level_fallback: bool,
    pub unit_price_ten_thousandths: i64,
    pub line_total_cents: i64,
    pub taxable: bool,
    pub tally: Option<SignedTally>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SignedTally {
    pub thickness_in: Option<String>,
    pub width_in: Option<String>,
    pub rows: Vec<SignedTallyRow>,
    pub linear_feet: String,
    pub board_feet: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SignedTallyRow {
    pub pieces: i64,
    pub length_ft: String,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SignedSaleTender {
    pub method: String,
    pub amount_cents: i64,
    pub reference: Option<String>,
    pub id_checked: bool,
    pub id_type: Option<String>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum QueueItemState {
    Signing,
    Waiting,
    Pending,
    Synced,
    Review,
}

impl QueueItemState {
    pub fn as_str(self) -> &'static str {
        match self {
            QueueItemState::Signing => "signing",
            QueueItemState::Waiting => "waiting",
            QueueItemState::Pending => "pending",
            QueueItemState::Synced => "synced",
            QueueItemState::Review => "review",
        }
    }

    pub fn is_unsettled(self) -> bool {
        !matches!(self, QueueItemState::Synced)
    }
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct QueuedSaleEntry {
    pub device_seq: i64,
    pub state: QueueItemState,
    pub reason_code: Option<String>,
    pub reason_message: Option<String>,
    pub invoice_id: Option<String>,
    pub invoice_number: Option<String>,
    pub sale: SignedOfflineSale,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct QueueStatusView {
    pub register_id: String,
    pub device_epoch: i64,
    pub last_seq: i64,
    pub confirmed_seq: i64,
    pub chain_head: String,
    pub waiting_count: i64,
    pub waiting_total_cents: i64,
    pub pending_count: i64,
    pub review_count: i64,
    pub synced_count: i64,
    pub items: Vec<QueuedSaleEntry>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SyncOutcomeItem {
    pub client_id: String,
    pub device_seq: i64,
    pub status: QueueItemState,
    pub invoice_id: Option<String>,
    pub invoice_number: Option<String>,
    pub reason_code: Option<String>,
    pub reason_message: Option<String>,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SyncBatchOutcome {
    pub confirmed_seq: i64,
    pub items: Vec<SyncOutcomeItem>,
}

/// The core's price of a cart the clerk is building (ADR 0011 5.3): every
/// figure the counter shows, derived from the core's own snapshot copy by the
/// sign path's own code; nothing is written, nothing is signed.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct CartPreview {
    pub snapshot_id: String,
    pub currency: String,
    pub tax_rate: String,
    pub tax_exempt: bool,
    pub lines: Vec<SignedSaleLine>,
    pub subtotal_cents: i64,
    pub tax_cents: i64,
    pub total_cents: i64,
    /// The change the tenders leave, when tenders were named and they pass.
    pub change_cents: Option<i64>,
    /// Why the named tenders would be refused, when they would.
    pub tender_refusal: Option<OfflineError>,
    /// The room left on the named cached account on this register.
    pub account_room_cents: Option<i64>,
    /// What the queue caps still allow: sales and cents.
    pub queue_room_count: i64,
    pub queue_room_cents: i64,
}

/// What a snapshot refresh did.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SnapshotRefresh {
    /// `updated` (a new copy is stored) or `unchanged` (the same one).
    pub status: String,
    pub snapshot_id: String,
    pub issued_at: String,
}

/// What one sync of the queue did (ADR 0011 section 4: "Sent 12, 1 needs a
/// manager").
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SyncReport {
    pub sent: i64,
    pub posted: i64,
    pub refused: i64,
    pub held: i64,
    pub resolved: i64,
    pub errors: i64,
    pub confirmed_seq: i64,
    /// The register's exception rows waiting on a manager, as the server
    /// counts them.
    pub open_exception_count: i64,
    pub queue: QueueStatusView,
}

#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct OfflineError {
    pub code: String,
    pub message: String,
}

impl OfflineError {
    pub fn new(code: &'static str, message: impl Into<String>) -> Self {
        Self {
            code: code.to_string(),
            message: message.into(),
        }
    }

    pub fn re_enrol_required() -> Self {
        Self::new(
            "re_enrol_required",
            "This register must be enrolled again before it can sell offline.",
        )
    }

    pub fn store_rolled_back() -> Self {
        Self::new(
            "store_rolled_back",
            "This register must be enrolled again before it can sell offline.",
        )
    }

    pub fn keychain_unavailable() -> Self {
        Self::new("keychain_unavailable", "the OS keychain is unavailable")
    }

    pub fn origin_denied() -> Self {
        Self::new(
            "origin_denied",
            "the caller's origin is not the Gable URL this shell loaded",
        )
    }

    pub fn embedded_bundle_refused() -> Self {
        Self::new(
            "embedded_bundle_refused",
            "offline counter commands are refused when the shell loads the embedded bundle",
        )
    }
}
