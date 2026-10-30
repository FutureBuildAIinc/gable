// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! Offline counter engine for the work shell (ADR 0011): exact scale-4/scale-6
//! arithmetic, OS keychain custody of the AEAD and Ed25519 signing keys,
//! AEAD-sealed SQLite queue and snapshot store, rollback and crash recovery,
//! and canonical JSON Ed25519 signing.

pub mod arithmetic;
pub mod model;
pub mod server;
pub mod store;

pub use arithmetic::{
    complete_tally, convert_stock, extend, format_decimal_quantity, format_wire_quantity,
    parse_quantity, parse_tax_rate, resolve_line_pair, resolve_stock_move, round_cash,
    settle_tenders, tax_at, CompletedTally, InexactStockError, OfflineTenderCalc, Pair,
    StockMoveError, TallyRowCalc, TenderRefusal, TenderSettlement, UnitSetEntry, ONE_QTY,
    QUANTITY_MAX, QUANTITY_SCALE, TAX_RATE_SCALE,
};
pub use model::{
    CartPreview, CashRoundingMode, EnrolmentPublic, EnrolmentRecord, HighWaterMark, OfflineError,
    OfflineSnapshot, PriorEpochMark, QueueItemState, QueueStatusView, QueuedSaleEntry,
    SignLineRequest, SignSaleRequest, SignTenderRequest, SignedOfflineSale, SignedSaleLine,
    SignedSaleTender, SignedTally, SignedTallyRow, SnapshotAccount, SnapshotBuyer, SnapshotJob,
    SnapshotPolicy, SnapshotPriceRow, SnapshotProduct, SnapshotRefresh, SnapshotTax,
    SnapshotTillSession, SnapshotUnitRow, SnapshotWalkIn, StoreMeta, SyncBatchOutcome,
    SyncOutcomeItem, SyncReport, TallyRequestInput, TallyRowRequestInput,
};
pub use server::{HttpOfflineServer, OfflineServer, ServerAnswer, TokenSource};
pub use store::{
    canonical_sale_bytes, sha256_hex, verify_sale_signature, CrashPoint, OfflineCounter,
};

#[cfg(test)]
mod tests {
    use super::*;
    use crate::custody::MemoryCustody;
    use crate::fence::OriginFence;
    use serde::Deserialize;
    use std::path::PathBuf;
    use std::sync::{Arc, Mutex};
    use uuid::Uuid;

    const GABLE_ORIGIN: &str = "https://gable.yard.internal";
    const PROFILE_ID: &str = "ai.futurebuild.gable.work";

    fn temp_db_path(label: &str) -> PathBuf {
        std::env::temp_dir().join(format!("gable-offline-{label}-{}.sqlite", Uuid::new_v4()))
    }

    fn sample_snapshot() -> OfflineSnapshot {
        OfflineSnapshot {
            snapshot_id: "11111111-1111-1111-1111-111111111111".to_string(),
            register_id: "REG-01".to_string(),
            branch_id: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb".to_string(),
            device_epoch: 3,
            issued_at: "2026-04-19T13:05:00Z".to_string(),
            till_session: SnapshotTillSession {
                id: "cccccccc-cccc-cccc-cccc-cccccccccccc".to_string(),
                cashier_id: "dddddddd-dddd-dddd-dddd-dddddddddddd".to_string(),
                opened_at: "2026-04-19T13:00:00Z".to_string(),
                business_date: "2026-04-19".to_string(),
                opening_float_cents: 20_000,
            },
            policy: SnapshotPolicy {
                enabled: true,
                max_sale_cents: 250_000,
                max_check_cents: 100_000,
                default_check_limit_cents: Some(75_000),
                account_cap_cents: 150_000,
                max_queue_count: 3,
                max_queue_total_cents: 300_000,
                allowed_tenders: vec![
                    "cash".to_string(),
                    "check".to_string(),
                    "account".to_string(),
                ],
                require_id_for_tenders: vec!["check".to_string(), "account".to_string()],
                allow_price_override: false,
                allow_discount: false,
                cash_rounding_mode: CashRoundingMode::Off,
            },
            tax: SnapshotTax {
                branch_rate: "0.082500".to_string(),
                provider_configured: false,
            },
            walk_in_customer: SnapshotWalkIn {
                id: "00000000-0000-0000-0000-000000000001".to_string(),
                currency: "USD".to_string(),
            },
            products: vec![
                SnapshotProduct {
                    id: "aaaa0001-0000-0000-0000-000000000001".to_string(),
                    sku: "2X4-8-SPF".to_string(),
                    barcode: Some("012345678905".to_string()),
                    description: "2x4x8 SPF Stud".to_string(),
                    category: "Dimensional Lumber".to_string(),
                    taxable: true,
                    is_kit: false,
                    random_length: false,
                    board_thickness_in: Some("2".to_string()),
                    board_width_in: Some("4".to_string()),
                    stock_uom: "EA".to_string(),
                    sale_uom: "EA".to_string(),
                    price_uom: "EA".to_string(),
                    in_stock: "420".to_string(),
                    unit_set: vec![
                        SnapshotUnitRow {
                            uom: "EA".to_string(),
                            unit_qty: "1".to_string(),
                            stock_qty: "1".to_string(),
                            sell: true,
                            price: true,
                        },
                        SnapshotUnitRow {
                            uom: "BDL".to_string(),
                            unit_qty: "1".to_string(),
                            stock_qty: "24".to_string(),
                            sell: true,
                            price: true,
                        },
                    ],
                },
                SnapshotProduct {
                    id: "aaaa0002-0000-0000-0000-000000000002".to_string(),
                    sku: "2X4-RL-SPF".to_string(),
                    barcode: None,
                    description: "2x4 Random Length SPF".to_string(),
                    category: "Dimensional Lumber".to_string(),
                    taxable: true,
                    is_kit: false,
                    random_length: true,
                    board_thickness_in: Some("2".to_string()),
                    board_width_in: Some("4".to_string()),
                    stock_uom: "BF".to_string(),
                    sale_uom: "LF".to_string(),
                    price_uom: "BF".to_string(),
                    in_stock: "1000".to_string(),
                    unit_set: vec![
                        SnapshotUnitRow {
                            uom: "BF".to_string(),
                            unit_qty: "1".to_string(),
                            stock_qty: "1".to_string(),
                            sell: true,
                            price: true,
                        },
                        SnapshotUnitRow {
                            uom: "LF".to_string(),
                            unit_qty: "3".to_string(),
                            stock_qty: "2".to_string(),
                            sell: true,
                            price: true,
                        },
                    ],
                },
                SnapshotProduct {
                    id: "aaaa0003-0000-0000-0000-000000000003".to_string(),
                    sku: "DECK-KIT-1".to_string(),
                    barcode: None,
                    description: "Complete Deck Framing Kit".to_string(),
                    category: "Kits".to_string(),
                    taxable: true,
                    is_kit: true,
                    random_length: false,
                    board_thickness_in: None,
                    board_width_in: None,
                    stock_uom: "EA".to_string(),
                    sale_uom: "EA".to_string(),
                    price_uom: "EA".to_string(),
                    in_stock: "10".to_string(),
                    unit_set: vec![SnapshotUnitRow {
                        uom: "EA".to_string(),
                        unit_qty: "1".to_string(),
                        stock_qty: "1".to_string(),
                        sell: true,
                        price: true,
                    }],
                },
            ],
            price_sheet: vec![
                SnapshotPriceRow {
                    product_id: "aaaa0001-0000-0000-0000-000000000001".to_string(),
                    price_key: "walkin".to_string(),
                    price_uom: "EA".to_string(),
                    uom_qty: "1".to_string(),
                    price_uom_qty: "1".to_string(),
                    unit_price_ten_thousandths: 38_500,
                    price_source: "list".to_string(),
                    price_basis: None,
                },
                SnapshotPriceRow {
                    product_id: "aaaa0001-0000-0000-0000-000000000001".to_string(),
                    price_key: "customer:22222222-2222-2222-2222-222222222222".to_string(),
                    price_uom: "EA".to_string(),
                    uom_qty: "1".to_string(),
                    price_uom_qty: "1".to_string(),
                    unit_price_ten_thousandths: 34_000,
                    price_source: "contract".to_string(),
                    price_basis: None,
                },
                SnapshotPriceRow {
                    product_id: "aaaa0001-0000-0000-0000-000000000001".to_string(),
                    price_key: "level:tier-a".to_string(),
                    price_uom: "EA".to_string(),
                    uom_qty: "1".to_string(),
                    price_uom_qty: "1".to_string(),
                    unit_price_ten_thousandths: 36_000,
                    price_source: "level".to_string(),
                    price_basis: Some("list_discount".to_string()),
                },
                SnapshotPriceRow {
                    product_id: "aaaa0002-0000-0000-0000-000000000002".to_string(),
                    price_key: "walkin".to_string(),
                    price_uom: "BF".to_string(),
                    uom_qty: "3".to_string(),
                    price_uom_qty: "2".to_string(),
                    unit_price_ten_thousandths: 12_000,
                    price_source: "list".to_string(),
                    price_basis: None,
                },
                SnapshotPriceRow {
                    product_id: "aaaa0002-0000-0000-0000-000000000002".to_string(),
                    price_key: "level:tier-a".to_string(),
                    price_uom: "BF".to_string(),
                    uom_qty: "3".to_string(),
                    price_uom_qty: "2".to_string(),
                    unit_price_ten_thousandths: 11_250,
                    price_source: "level".to_string(),
                    price_basis: Some("cost_markup".to_string()),
                },
            ],
            accounts: vec![SnapshotAccount {
                customer_id: "22222222-2222-2222-2222-222222222222".to_string(),
                account_number: "CUST-0042".to_string(),
                name: "Ridge Framing LLC".to_string(),
                currency: "USD".to_string(),
                tax_exempt: false,
                po_required: true,
                buyer_roster_required: true,
                credit_hold: false,
                credit_limit_cents: 2_500_000,
                open_receivable_cents: 840_000,
                offline_room_cents: 150_000,
                check_limit_cents: Some(80_000),
                price_key: "customer:22222222-2222-2222-2222-222222222222".to_string(),
                level_key: Some("level:tier-a".to_string()),
                jobs: vec![SnapshotJob {
                    id: "33333333-3333-3333-3333-333333333333".to_string(),
                    code: "LOT-14".to_string(),
                    name: "Lot 14 Framing".to_string(),
                }],
                buyers: vec![SnapshotBuyer {
                    contact_id: "44444444-4444-4444-4444-444444444444".to_string(),
                    name: "Dana Builder".to_string(),
                    per_purchase_cap_cents: Some(120_000),
                }],
            }],
        }
    }

    fn walk_in_sale_request(client_id: &str, qty: &str, cash_cents: i64) -> SignSaleRequest {
        SignSaleRequest {
            client_id: client_id.to_string(),
            snapshot_id: "11111111-1111-1111-1111-111111111111".to_string(),
            till_session_id: "cccccccc-cccc-cccc-cccc-cccccccccccc".to_string(),
            cashier_id: "dddddddd-dddd-dddd-dddd-dddddddddddd".to_string(),
            customer_id: None,
            buyer_contact_id: None,
            customer_po: None,
            job_id: None,
            picked_up_by: Some("Self".to_string()),
            business_date: "2026-04-19".to_string(),
            sold_at: "2026-04-19T14:22:11Z".to_string(),
            lines: vec![SignLineRequest {
                id: "55555555-5555-5555-5555-555555555555".to_string(),
                product_id: "aaaa0001-0000-0000-0000-000000000001".to_string(),
                quantity: Some(qty.to_string()),
                uom: "EA".to_string(),
                ..Default::default()
            }],
            tenders: vec![SignTenderRequest {
                method: "cash".to_string(),
                amount_cents: cash_cents,
                reference: None,
                id_checked: false,
                id_type: None,
            }],
            ..Default::default()
        }
    }

    #[derive(Deserialize)]
    struct SharedArithmeticFixture {
        extensions: Vec<ExtensionCase>,
        taxes: Vec<TaxCase>,
        stock_conversions: Vec<StockConversionCase>,
        stock_restatements: Vec<StockRestatementCase>,
        tallies: Vec<TallyCase>,
        tenders: Vec<TenderCase>,
    }

    #[derive(Deserialize)]
    struct ExtensionCase {
        name: String,
        qty: String,
        uom_qty: String,
        price_uom_qty: String,
        unit_price_ten_thousandths: i64,
        expected_cents: i64,
    }

    #[derive(Deserialize)]
    struct TaxCase {
        name: String,
        taxable_cents: i64,
        rate: String,
        rate_scaled: i64,
        expected_tax_cents: i64,
    }

    #[derive(Deserialize)]
    struct StockConversionCase {
        name: String,
        qty: String,
        uom: String,
        stock_uom: String,
        unit_qty: String,
        stock_qty: String,
        refused: bool,
        expected_stock_qty: Option<String>,
        nearest_below: Option<String>,
        nearest_above: Option<String>,
        expected_error: Option<String>,
    }

    #[derive(Deserialize)]
    struct StockRestatementUnitRow {
        uom: String,
        unit_qty: String,
        stock_qty: String,
        sell: bool,
        price: bool,
    }

    #[derive(Deserialize)]
    struct StockRestatementCase {
        name: String,
        line_index: usize,
        recorded_stock_uom: String,
        recorded_stock_qty: String,
        current_stock_uom: String,
        unit_set: Vec<StockRestatementUnitRow>,
        refused: bool,
        expected_stock_uom: Option<String>,
        expected_stock_qty: Option<String>,
        expected_code: Option<String>,
    }

    #[derive(Deserialize)]
    struct TallyRowFixture {
        pieces: i64,
        length_ft: String,
    }

    #[derive(Deserialize)]
    struct TallyCase {
        name: String,
        thickness_in: String,
        width_in: String,
        rows: Vec<TallyRowFixture>,
        expected_linear_feet: String,
        expected_board_feet: String,
    }

    #[derive(Deserialize)]
    struct TenderCase {
        name: String,
        total_cents: i64,
        tenders: Vec<OfflineTenderCalc>,
        cash_rounding_mode: CashRoundingMode,
        refused: bool,
        expected_tendered_cents: i64,
        expected_cash_cents: i64,
        expected_account_cents: i64,
        expected_change_cents: i64,
        expected_rounding_cents: i64,
        expected_code: Option<String>,
    }

    // RULE (ADR 0011 section 7): the Rust offline core's arithmetic (`extend`,
    // `tax_at`, stock conversion, stock unit restatement, tally linear/board
    // feet, and tender sums/change) matches the shared fixture in
    // `core/internal/salesdoc/testdata/offline-arithmetic.json`.
    #[test]
    fn the_rust_offline_arithmetic_matches_the_shared_salesdoc_fixture() {
        let raw =
            include_str!("../../../../core/internal/salesdoc/testdata/offline-arithmetic.json");
        let fix: SharedArithmeticFixture = serde_json::from_str(raw).expect("valid fixture JSON");

        for tc in &fix.extensions {
            let qty = parse_quantity(&tc.qty).expect(&tc.name);
            let uq = parse_quantity(&tc.uom_qty).expect(&tc.name);
            let pq = parse_quantity(&tc.price_uom_qty).expect(&tc.name);
            let got = extend(qty, uq, pq, tc.unit_price_ten_thousandths).expect(&tc.name);
            assert_eq!(got, tc.expected_cents, "extend/{}", tc.name);
        }

        for tc in &fix.taxes {
            let scaled = parse_tax_rate(&tc.rate).expect(&tc.name);
            assert_eq!(scaled, tc.rate_scaled, "tax_rate/{}", tc.name);
            let got = tax_at(tc.taxable_cents, scaled);
            assert_eq!(got, tc.expected_tax_cents, "tax/{}", tc.name);
        }

        for tc in &fix.stock_conversions {
            let qty = parse_quantity(&tc.qty).expect(&tc.name);
            let uq = parse_quantity(&tc.unit_qty).expect(&tc.name);
            let sq = parse_quantity(&tc.stock_qty).expect(&tc.name);
            let res = convert_stock(qty, &tc.uom, &tc.stock_uom, uq, sq);
            if tc.refused {
                let err = res.expect_err(&tc.name);
                if let Some(below) = &tc.nearest_below {
                    assert_eq!(&format_wire_quantity(err.below), below, "below/{}", tc.name);
                }
                if let Some(above) = &tc.nearest_above {
                    assert_eq!(&format_wire_quantity(err.above), above, "above/{}", tc.name);
                }
                if let Some(msg) = &tc.expected_error {
                    assert_eq!(&err.message(), msg, "error/{}", tc.name);
                }
            } else {
                let got = res.expect(&tc.name);
                assert_eq!(
                    Some(format_wire_quantity(got)).as_deref(),
                    tc.expected_stock_qty.as_deref(),
                    "stock/{}",
                    tc.name
                );
            }
        }

        for tc in &fix.stock_restatements {
            let rec_qty = parse_quantity(&tc.recorded_stock_qty).expect(&tc.name);
            let set: Vec<UnitSetEntry> = tc
                .unit_set
                .iter()
                .map(|r| UnitSetEntry {
                    uom: r.uom.clone(),
                    unit_qty: parse_quantity(&r.unit_qty).unwrap(),
                    stock_qty: parse_quantity(&r.stock_qty).unwrap(),
                    sell: r.sell,
                    price: r.price,
                })
                .collect();
            let res = resolve_stock_move(
                tc.line_index,
                &tc.recorded_stock_uom,
                rec_qty,
                &tc.current_stock_uom,
                &set,
            );
            if tc.refused {
                let err = res.expect_err(&tc.name);
                assert_eq!(
                    Some(err.code),
                    tc.expected_code.as_deref(),
                    "restatement_code/{}",
                    tc.name
                );
            } else {
                let (uom, qty) = res.expect(&tc.name);
                assert_eq!(
                    Some(uom.as_str()),
                    tc.expected_stock_uom.as_deref(),
                    "restatement_uom/{}",
                    tc.name
                );
                assert_eq!(
                    Some(format_wire_quantity(qty)).as_deref(),
                    tc.expected_stock_qty.as_deref(),
                    "restatement_qty/{}",
                    tc.name
                );
            }
        }

        for tc in &fix.tallies {
            let thick = parse_quantity(&tc.thickness_in).expect(&tc.name);
            let width = parse_quantity(&tc.width_in).expect(&tc.name);
            let rows: Vec<TallyRowCalc> = tc
                .rows
                .iter()
                .map(|r| TallyRowCalc {
                    pieces: r.pieces,
                    length_ft: parse_quantity(&r.length_ft).unwrap(),
                })
                .collect();
            let got = complete_tally(&rows, Some(thick), Some(width)).expect(&tc.name);
            assert_eq!(
                format_wire_quantity(got.linear_feet),
                tc.expected_linear_feet,
                "tally_lf/{}",
                tc.name
            );
            assert_eq!(
                got.board_feet.map(format_wire_quantity).as_deref(),
                Some(tc.expected_board_feet.as_str()),
                "tally_bf/{}",
                tc.name
            );
        }

        for tc in &fix.tenders {
            let res = settle_tenders(tc.total_cents, &tc.tenders, tc.cash_rounding_mode);
            if tc.refused {
                let err = res.expect_err(&tc.name);
                assert_eq!(
                    Some(err.code),
                    tc.expected_code.as_deref(),
                    "tender_code/{}",
                    tc.name
                );
            } else {
                let got = res.expect(&tc.name);
                assert_eq!(
                    got.tendered_cents, tc.expected_tendered_cents,
                    "{}",
                    tc.name
                );
                assert_eq!(got.cash_cents, tc.expected_cash_cents, "{}", tc.name);
                assert_eq!(got.account_cents, tc.expected_account_cents, "{}", tc.name);
                assert_eq!(got.change_cents, tc.expected_change_cents, "{}", tc.name);
                assert_eq!(
                    got.rounding_cents, tc.expected_rounding_cents,
                    "rounding/{}",
                    tc.name
                );
            }
        }
    }

    // RULE (ADR 0011 section 7): the store's AEAD key and device signing key
    // come from the OS keychain (`tauri/core/src/custody.rs`), every row in the
    // SQLite store is sealed with AEAD, and the store fails closed when the
    // keychain is unavailable.
    #[test]
    fn store_keys_come_from_keychain_and_fail_closed_when_unavailable() {
        let db_path = temp_db_path("custody");
        let custody = Arc::new(MemoryCustody::new());
        let fence = OriginFence::from_starts(&[GABLE_ORIGIN]).unwrap();
        let counter =
            OfflineCounter::new(PROFILE_ID, custody.clone(), &db_path, Some(fence.clone()));

        let enrolled = counter
            .enrol_register(Some(GABLE_ORIGIN), "REG-01", 3)
            .expect("enrol succeeds");
        assert_eq!(enrolled.public_key_hex.len(), 64);

        // Verify the three offline slots are filed under the profile's keychain service.
        let pairs = custody.snapshot_pairs();
        assert!(pairs.contains(&(
            PROFILE_ID.to_string(),
            "offline-counter:aead-key".to_string()
        )));
        assert!(pairs.contains(&(
            PROFILE_ID.to_string(),
            "offline-counter:enrolment".to_string()
        )));
        assert!(pairs.contains(&(PROFILE_ID.to_string(), "offline-counter:mark".to_string())));

        counter
            .install_test_snapshot(sample_snapshot())
            .expect("snapshot stored");
        let signed = counter
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-777777777701", "10", 5_000),
            )
            .expect("sale signed");
        verify_sale_signature(&enrolled.public_key_hex, &signed)
            .expect("Ed25519 signature verifies");

        // Confirm raw SQLite file bytes contain no plaintext SKU or customer name.
        let raw_sqlite = std::fs::read(&db_path).expect("read sqlite file");
        let raw_lossy = String::from_utf8_lossy(&raw_sqlite);
        assert!(!raw_lossy.contains("2X4-8-SPF"));
        assert!(!raw_lossy.contains("Ridge Framing LLC"));

        // Fail closed when the keychain is unavailable.
        let unavailable_custody = Arc::new(MemoryCustody::unavailable());
        let closed_counter =
            OfflineCounter::new(PROFILE_ID, unavailable_custody, &db_path, Some(fence));
        assert_eq!(
            closed_counter
                .enrol_register(Some(GABLE_ORIGIN), "REG-01", 4)
                .unwrap_err()
                .code,
            "keychain_unavailable"
        );
        assert_eq!(
            closed_counter
                .install_test_snapshot(sample_snapshot())
                .unwrap_err()
                .code,
            "keychain_unavailable"
        );
        assert_eq!(
            closed_counter
                .load_snapshot(Some(GABLE_ORIGIN))
                .unwrap_err()
                .code,
            "keychain_unavailable"
        );
        assert_eq!(
            closed_counter
                .sign_and_queue_sale(
                    Some(GABLE_ORIGIN),
                    walk_in_sale_request("77777777-7777-7777-7777-777777777702", "10", 5_000),
                )
                .unwrap_err()
                .code,
            "keychain_unavailable"
        );
        assert_eq!(
            closed_counter
                .queue_status(Some(GABLE_ORIGIN))
                .unwrap_err()
                .code,
            "keychain_unavailable"
        );

        let _ = std::fs::remove_file(&db_path);
    }

    // RULE (ADR 0011 section 5.1 and section 7): missing store under a live
    // enrolment refuses offline selling with "This register must be enrolled
    // again before it can sell offline." until the device is re-enrolled.
    #[test]
    fn missing_store_under_live_enrolment_refuses_offline_selling() {
        let db_path = temp_db_path("missing");
        let custody = Arc::new(MemoryCustody::new());
        let fence = OriginFence::from_starts(&[GABLE_ORIGIN]).unwrap();
        let counter = OfflineCounter::new(PROFILE_ID, custody, &db_path, Some(fence));

        counter
            .enrol_register(Some(GABLE_ORIGIN), "REG-01", 3)
            .unwrap();
        counter.install_test_snapshot(sample_snapshot()).unwrap();

        // Remove the SQLite store while the keychain still holds the live enrolment.
        std::fs::remove_file(&db_path).unwrap();

        let err = counter
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-777777777703", "10", 5_000),
            )
            .unwrap_err();
        assert_eq!(err.code, "re_enrol_required");
        assert_eq!(
            err.message,
            "This register must be enrolled again before it can sell offline."
        );

        let status_err = counter.queue_status(Some(GABLE_ORIGIN)).unwrap_err();
        assert_eq!(status_err.code, "re_enrol_required");

        // Re-enrolling at epoch 4 restores offline selling.
        counter
            .enrol_register(Some(GABLE_ORIGIN), "REG-01", 4)
            .unwrap();
        let mut snap4 = sample_snapshot();
        snap4.device_epoch = 4;
        counter.install_test_snapshot(snap4).unwrap();
        let sale = counter
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-777777777704", "10", 5_000),
            )
            .unwrap();
        assert_eq!(sale.device_epoch, 4);
        assert_eq!(sale.device_seq, 1);

        let _ = std::fs::remove_file(&db_path);
    }

    // RULE (ADR 0011 section 5.1 and section 7): a rolled-back store (a copy of
    // the store taken after a sync and put back over the live one) is detected
    // against the keychain's high-water mark so the sequence never restarts
    // under the old epoch.
    #[test]
    fn rolled_back_store_is_detected_against_keychain_high_water_mark() {
        let db_path = temp_db_path("rollback");
        let backup_path = temp_db_path("rollback-backup");
        let custody = Arc::new(MemoryCustody::new());
        let fence = OriginFence::from_starts(&[GABLE_ORIGIN]).unwrap();
        let counter = OfflineCounter::new(PROFILE_ID, custody, &db_path, Some(fence));

        counter
            .enrol_register(Some(GABLE_ORIGIN), "REG-01", 3)
            .unwrap();
        counter.install_test_snapshot(sample_snapshot()).unwrap();

        let sale1 = counter
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-777777777711", "10", 5_000),
            )
            .unwrap();
        assert_eq!(sale1.device_seq, 1);

        // Sync sale 1 so confirmed_seq = 1, then take a copy of the store file.
        counter
            .record_sync_outcomes(
                Some(GABLE_ORIGIN),
                SyncBatchOutcome {
                    confirmed_seq: 1,
                    items: vec![SyncOutcomeItem {
                        client_id: sale1.client_id.clone(),
                        device_seq: 1,
                        status: QueueItemState::Synced,
                        invoice_id: Some("99999999-9999-9999-9999-999999999901".to_string()),
                        invoice_number: Some("INV-0001".to_string()),
                        reason_code: None,
                        reason_message: None,
                    }],
                },
            )
            .unwrap();
        std::fs::copy(&db_path, &backup_path).unwrap();

        // Ring sale 2 on the live store (advancing keychain mark to seq 2).
        let sale2 = counter
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-777777777712", "10", 5_000),
            )
            .unwrap();
        assert_eq!(sale2.device_seq, 2);

        // Overwrite the live store with the post-sync backup at seq 1.
        std::fs::copy(&backup_path, &db_path).unwrap();

        let err = counter
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-777777777713", "10", 5_000),
            )
            .unwrap_err();
        assert_eq!(err.code, "store_rolled_back");
        assert_eq!(
            err.message,
            "This register must be enrolled again before it can sell offline."
        );

        let _ = std::fs::remove_file(&db_path);
        let _ = std::fs::remove_file(&backup_path);
    }

    // RULE (ADR 0011 section 5.1 and section 7): a crash between queue write
    // and receipt print recovers the sale on restart and never repeats a
    // sequence number.
    #[test]
    fn crash_between_queue_write_and_receipt_print_recovers_sale_without_repeating_sequence() {
        let db_path = temp_db_path("crash");
        let custody = Arc::new(MemoryCustody::new());
        let fence = OriginFence::from_starts(&[GABLE_ORIGIN]).unwrap();
        let counter =
            OfflineCounter::new(PROFILE_ID, custody.clone(), &db_path, Some(fence.clone()));

        let enrolled = counter
            .enrol_register(Some(GABLE_ORIGIN), "REG-01", 3)
            .unwrap();
        let mut snap = sample_snapshot();
        snap.policy.max_queue_count = 10;
        snap.policy.max_queue_total_cents = 1_000_000;
        counter.install_test_snapshot(snap).unwrap();

        // Crash 1: after SQLite write, before keychain mark update.
        counter.set_crash_point(Some(CrashPoint::AfterStoreWriteBeforeKeychainMark));
        let err1 = counter
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-777777777721", "10", 5_000),
            )
            .unwrap_err();
        assert_eq!(err1.code, "simulated_crash");

        // Restart the counter: sale 1 must be recovered at seq 1 with a valid signature.
        let restarted1 =
            OfflineCounter::new(PROFILE_ID, custody.clone(), &db_path, Some(fence.clone()));
        let status1 = restarted1.queue_status(Some(GABLE_ORIGIN)).unwrap();
        assert_eq!(status1.last_seq, 1);
        assert_eq!(status1.items.len(), 1);
        assert_eq!(status1.items[0].device_seq, 1);
        assert_eq!(status1.items[0].state, QueueItemState::Waiting);
        verify_sale_signature(&enrolled.public_key_hex, &status1.items[0].sale).unwrap();

        // Crash 2: after keychain mark update, before Ed25519 sign.
        restarted1.set_crash_point(Some(CrashPoint::AfterKeychainMarkBeforeSign));
        let err2 = restarted1
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-777777777722", "10", 5_000),
            )
            .unwrap_err();
        assert_eq!(err2.code, "simulated_crash");

        let restarted2 =
            OfflineCounter::new(PROFILE_ID, custody.clone(), &db_path, Some(fence.clone()));
        let status2 = restarted2.queue_status(Some(GABLE_ORIGIN)).unwrap();
        assert_eq!(status2.last_seq, 2);
        assert_eq!(status2.items.len(), 2);
        assert_eq!(status2.items[1].device_seq, 2);
        assert_eq!(status2.items[1].state, QueueItemState::Waiting);
        verify_sale_signature(&enrolled.public_key_hex, &status2.items[1].sale).unwrap();

        // Crash 3: after Ed25519 sign, before receipt print returns.
        restarted2.set_crash_point(Some(CrashPoint::AfterSignBeforePrint));
        let err3 = restarted2
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-777777777723", "10", 5_000),
            )
            .unwrap_err();
        assert_eq!(err3.code, "simulated_crash");

        // Restart and ring sale 4: sale 3 is preserved at seq 3 and the next sale is seq 4.
        let restarted3 = OfflineCounter::new(PROFILE_ID, custody, &db_path, Some(fence));
        let sale4 = restarted3
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-777777777724", "10", 5_000),
            )
            .unwrap();
        assert_eq!(sale4.device_seq, 4);

        let final_status = restarted3.queue_status(Some(GABLE_ORIGIN)).unwrap();
        let seqs: Vec<i64> = final_status.items.iter().map(|i| i.device_seq).collect();
        assert_eq!(seqs, vec![1, 2, 3, 4]);

        let _ = std::fs::remove_file(&db_path);
    }

    // RULE (ADR 0011 section 5.1 and section 7): the core's own snapshot copy
    // refuses a sale whose price, tax figure, price key, price unit, or
    // customer differs from what the core derives, or that names its own
    // sequence or chain link; refuses kit and charge lines; resolves customer
    // and level-fallback price keys; subtracts queued stock and account room;
    // and excludes synced sales from the queue cap.
    #[test]
    fn core_snapshot_refuses_mismatched_price_tax_key_unit_customer_or_caller_sequence() {
        let db_path = temp_db_path("snapshot-rules");
        let custody = Arc::new(MemoryCustody::new());
        let fence = OriginFence::from_starts(&[GABLE_ORIGIN]).unwrap();
        let counter = OfflineCounter::new(PROFILE_ID, custody, &db_path, Some(fence));

        counter
            .enrol_register(Some(GABLE_ORIGIN), "REG-01", 3)
            .unwrap();
        counter.install_test_snapshot(sample_snapshot()).unwrap();

        // 1. Caller naming device_seq or prev_digest is refused.
        let mut with_seq =
            walk_in_sale_request("77777777-7777-7777-7777-777777777731", "10", 5_000);
        with_seq.device_seq = Some(1);
        assert_eq!(
            counter
                .sign_and_queue_sale(Some(GABLE_ORIGIN), with_seq)
                .unwrap_err()
                .code,
            "caller_named_sequence"
        );

        let mut with_digest =
            walk_in_sale_request("77777777-7777-7777-7777-777777777732", "10", 5_000);
        with_digest.prev_digest = Some("GENESIS".to_string());
        assert_eq!(
            counter
                .sign_and_queue_sale(Some(GABLE_ORIGIN), with_digest)
                .unwrap_err()
                .code,
            "caller_named_chain_link"
        );

        // 2. Mismatched unit price, price key, price unit, tax figure, or customer is refused.
        let mut bad_price =
            walk_in_sale_request("77777777-7777-7777-7777-777777777733", "10", 5_000);
        bad_price.lines[0].unit_price_ten_thousandths = Some(10_000);
        assert_eq!(
            counter
                .sign_and_queue_sale(Some(GABLE_ORIGIN), bad_price)
                .unwrap_err()
                .code,
            "price_mismatch"
        );

        let mut bad_key = walk_in_sale_request("77777777-7777-7777-7777-777777777734", "10", 5_000);
        bad_key.lines[0].price_key = Some("level:tier-a".to_string());
        assert_eq!(
            counter
                .sign_and_queue_sale(Some(GABLE_ORIGIN), bad_key)
                .unwrap_err()
                .code,
            "price_key_mismatch"
        );

        let mut bad_puom =
            walk_in_sale_request("77777777-7777-7777-7777-777777777735", "10", 5_000);
        bad_puom.lines[0].price_uom = Some("BDL".to_string());
        assert_eq!(
            counter
                .sign_and_queue_sale(Some(GABLE_ORIGIN), bad_puom)
                .unwrap_err()
                .code,
            "price_uom_mismatch"
        );

        let mut bad_tax = walk_in_sale_request("77777777-7777-7777-7777-777777777736", "10", 5_000);
        bad_tax.tax_cents = Some(0);
        assert_eq!(
            counter
                .sign_and_queue_sale(Some(GABLE_ORIGIN), bad_tax)
                .unwrap_err()
                .code,
            "tax_figure_mismatch"
        );

        let mut bad_customer =
            walk_in_sale_request("77777777-7777-7777-7777-777777777737", "10", 5_000);
        bad_customer.customer_id = Some("99999999-0000-0000-0000-000000000000".to_string());
        assert_eq!(
            counter
                .sign_and_queue_sale(Some(GABLE_ORIGIN), bad_customer)
                .unwrap_err()
                .code,
            "customer_not_in_snapshot"
        );

        // 3. Kit line and charge line are refused offline.
        let mut kit_req = walk_in_sale_request("77777777-7777-7777-7777-777777777738", "1", 5_000);
        kit_req.lines[0].product_id = "aaaa0003-0000-0000-0000-000000000003".to_string();
        assert_eq!(
            counter
                .sign_and_queue_sale(Some(GABLE_ORIGIN), kit_req)
                .unwrap_err()
                .code,
            "kit_not_offline"
        );

        let mut charge_req =
            walk_in_sale_request("77777777-7777-7777-7777-777777777739", "1", 5_000);
        charge_req.lines[0].charge_code = Some("DELIVERY".to_string());
        assert_eq!(
            counter
                .sign_and_queue_sale(Some(GABLE_ORIGIN), charge_req)
                .unwrap_err()
                .code,
            "charge_not_offline"
        );

        // 4. Customer sale on product 2 (random-length tally) falls back to
        // `level:tier-a` (`level_fallback = true`) because the customer has no
        // contract row for product 2, and consumes account room and stock.
        let account_req = SignSaleRequest {
            client_id: "77777777-7777-7777-7777-777777777740".to_string(),
            snapshot_id: "11111111-1111-1111-1111-111111111111".to_string(),
            till_session_id: "cccccccc-cccc-cccc-cccc-cccccccccccc".to_string(),
            cashier_id: "dddddddd-dddd-dddd-dddd-dddddddddddd".to_string(),
            customer_id: Some("22222222-2222-2222-2222-222222222222".to_string()),
            buyer_contact_id: Some("44444444-4444-4444-4444-444444444444".to_string()),
            customer_po: Some("PO-900".to_string()),
            job_id: Some("33333333-3333-3333-3333-333333333333".to_string()),
            picked_up_by: Some("Dana Builder".to_string()),
            business_date: "2026-04-19".to_string(),
            sold_at: "2026-04-19T14:30:00Z".to_string(),
            lines: vec![SignLineRequest {
                id: "55555555-5555-5555-5555-555555555556".to_string(),
                product_id: "aaaa0002-0000-0000-0000-000000000002".to_string(),
                uom: "LF".to_string(),
                tally: Some(TallyRequestInput {
                    rows: vec![TallyRowRequestInput {
                        pieces: 15,
                        length_ft: "10".to_string(),
                    }],
                }),
                ..Default::default()
            }],
            tenders: vec![SignTenderRequest {
                method: "account".to_string(),
                amount_cents: 12_178,
                reference: Some("PO-900".to_string()),
                id_checked: true,
                id_type: Some("drivers_license".to_string()),
            }],
            ..Default::default()
        };
        let signed_acc = counter
            .sign_and_queue_sale(Some(GABLE_ORIGIN), account_req)
            .expect("account tally sale succeeds");
        assert_eq!(signed_acc.lines[0].price_key, "level:tier-a");
        assert!(signed_acc.lines[0].level_fallback);
        assert_eq!(signed_acc.lines[0].quantity, "150");
        assert_eq!(signed_acc.lines[0].stock_uom, "BF");
        assert_eq!(signed_acc.lines[0].stock_quantity, "100");
        assert_eq!(signed_acc.subtotal_cents, 11_250);
        assert_eq!(signed_acc.tax_cents, 928);
        assert_eq!(signed_acc.total_cents, 12_178);

        // Stock figure in `load_snapshot` is reduced by 100 BF (from 1000 to 900).
        let updated_snap = counter.load_snapshot(Some(GABLE_ORIGIN)).unwrap();
        let prod2 = updated_snap
            .products
            .iter()
            .find(|p| p.id == "aaaa0002-0000-0000-0000-000000000002")
            .unwrap();
        assert_eq!(prod2.in_stock, "900");

        let _ = std::fs::remove_file(&db_path);
    }

    // RULE (ADR 0011 section 5.2 and section 7): offline counter commands
    // answer the Gable origin the shell loaded, refuse every other origin, and
    // refuse when the shell loaded the embedded bundle.
    #[test]
    fn offline_counter_commands_enforce_origin_fence_and_refuse_embedded_bundle() {
        let db_path = temp_db_path("fence");
        let custody = Arc::new(MemoryCustody::new());
        let fence = OriginFence::from_starts(&[GABLE_ORIGIN]).unwrap();
        let counter = OfflineCounter::new(PROFILE_ID, custody.clone(), &db_path, Some(fence));

        counter
            .enrol_register(Some(GABLE_ORIGIN), "REG-01", 1)
            .unwrap();
        assert_eq!(
            counter
                .queue_status(Some("https://evil.example"))
                .unwrap_err()
                .code,
            "origin_denied"
        );
        assert_eq!(
            counter.queue_status(None).unwrap_err().code,
            "origin_denied"
        );

        let embedded_counter = OfflineCounter::new(PROFILE_ID, custody, &db_path, None);
        assert_eq!(
            embedded_counter
                .queue_status(Some(GABLE_ORIGIN))
                .unwrap_err()
                .code,
            "embedded_bundle_refused"
        );

        let _ = std::fs::remove_file(&db_path);
    }

    // RULE (CP-18, ADR 0011 section 5.1): on a branch that rounds cash, the
    // core signs the change the live counter gives: 10 studs at 3.85 with tax
    // at 8.25% is 41.68, the cash leg rounds to 41.70, and 50.00 tendered
    // gives 8.30 back, not 8.32.
    #[test]
    fn the_core_signs_the_change_from_the_branchs_rounded_cash_leg() {
        let db_path = temp_db_path("rounding");
        let custody = Arc::new(MemoryCustody::new());
        let fence = OriginFence::from_starts(&[GABLE_ORIGIN]).unwrap();
        let counter = OfflineCounter::new(PROFILE_ID, custody, &db_path, Some(fence));
        counter
            .enrol_register(Some(GABLE_ORIGIN), "REG-01", 3)
            .unwrap();
        let mut snap = sample_snapshot();
        snap.policy.cash_rounding_mode = CashRoundingMode::Nearest5Cents;
        counter.install_test_snapshot(snap).unwrap();
        let signed = counter
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-7777777777c1", "10", 5_000),
            )
            .unwrap();
        let _ = std::fs::remove_file(&db_path);
        assert_eq!(
            signed.total_cents, 4_168,
            "the invoice keeps the exact total"
        );
        assert_eq!(signed.change_cents, 830);
        assert_eq!(round_cash(4_168), (4_170, 2));
    }

    // READER RED 1: a page on the Gable origin must never supply the snapshot
    // the core prices and signs from. No webview command stores one (nor
    // enrols the register, nor writes the sync outcomes), the Gable origin's
    // grant names none, and a counter whose core has fetched no snapshot of
    // its own signs nothing.
    #[test]
    fn reader_red_page_supplied_snapshot_is_refused() {
        const WORK_BUILD: &str = include_str!("../../../gable-work/build.rs");
        const WORK_LIB: &str = include_str!("../../../gable-work/src/lib.rs");
        for page_held in [
            "store_offline_snapshot",
            "enrol_offline_register",
            "record_offline_sync_outcomes",
        ] {
            assert!(
                !WORK_BUILD.contains(&format!("\"{page_held}\"")),
                "{page_held} is declared as a webview command"
            );
            assert!(
                !WORK_LIB.contains(&format!("fn {page_held}")),
                "{page_held} is a webview command"
            );
            let grant = format!("allow-{}", page_held.replace('_', "-"));
            assert!(
                !crate::shell::WORK_REMOTE_PERMISSIONS.contains(&grant.as_str()),
                "{grant} is granted to the Gable origin"
            );
        }

        let db_path = temp_db_path("rr1");
        let custody = Arc::new(MemoryCustody::new());
        let fence = OriginFence::from_starts(&[GABLE_ORIGIN]).unwrap();
        let counter = OfflineCounter::new(PROFILE_ID, custody, &db_path, Some(fence));
        counter
            .enrol_register(Some(GABLE_ORIGIN), "REG-01", 3)
            .unwrap();
        let signed = counter.sign_and_queue_sale(
            Some(GABLE_ORIGIN),
            walk_in_sale_request("77777777-7777-7777-7777-7777777777a1", "10", 100),
        );
        let _ = std::fs::remove_file(&db_path);
        assert_eq!(
            signed.map(|s| s.total_cents).unwrap_err().code,
            "snapshot_missing",
            "the core signed with no snapshot of its own"
        );
    }

    // READER RED 2: a re-enrolment never wipes an unsynced queue, never reuses
    // the epoch, never takes its URL from an argument, and keeps the replaced
    // epoch's mark for the unknown tail report.
    #[test]
    fn reader_red_page_reenrol_cannot_wipe_unsynced_queue_or_repoint_shell() {
        use crate::custody::{Address, Custody, OfflineSlot};

        let db_path = temp_db_path("rr2");
        let custody = Arc::new(MemoryCustody::new());
        let fence = OriginFence::from_starts(&[GABLE_ORIGIN]).unwrap();
        let counter = OfflineCounter::new(PROFILE_ID, custody.clone(), &db_path, Some(fence));
        counter
            .enrol_register(Some(GABLE_ORIGIN), "REG-01", 3)
            .unwrap();
        counter.install_test_snapshot(sample_snapshot()).unwrap();
        let sold = counter
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-7777777777a2", "10", 5_000),
            )
            .unwrap();
        assert_eq!(
            counter
                .queue_status(Some(GABLE_ORIGIN))
                .unwrap()
                .waiting_count,
            1
        );

        let same_epoch = counter.enrol_register(Some(GABLE_ORIGIN), "REG-01", 3);
        let unsynced = counter.enrol_register(Some(GABLE_ORIGIN), "REG-01", 4);
        let elsewhere = counter.enrol_register(Some("https://attacker.example/"), "REG-01", 4);
        let url = counter.enrolled_gable_url().unwrap();
        let waiting = counter
            .queue_status(Some(GABLE_ORIGIN))
            .map(|q| q.waiting_count)
            .unwrap_or(-1);
        assert_eq!(same_epoch.unwrap_err().code, "epoch_not_newer");
        assert_eq!(unsynced.unwrap_err().code, "queue_not_drained");
        assert_eq!(elsewhere.unwrap_err().code, "origin_denied");
        assert_eq!(url.as_deref(), Some("https://gable.yard.internal/"));
        assert_eq!(waiting, 1, "the unsynced sale is still queued");

        // Once the server has settled the queue, a new epoch enrols and the
        // replaced epoch's mark is kept for the unknown tail.
        counter
            .record_sync_outcomes(
                Some(GABLE_ORIGIN),
                SyncBatchOutcome {
                    confirmed_seq: sold.device_seq,
                    items: vec![SyncOutcomeItem {
                        client_id: sold.client_id.clone(),
                        device_seq: sold.device_seq,
                        status: QueueItemState::Synced,
                        invoice_id: None,
                        invoice_number: None,
                        reason_code: None,
                        reason_message: None,
                    }],
                },
            )
            .unwrap();
        counter
            .enrol_register(Some(GABLE_ORIGIN), "REG-01", 4)
            .unwrap();
        let addr = Address::offline(PROFILE_ID, OfflineSlot::Enrolment).unwrap();
        let rec: EnrolmentRecord =
            serde_json::from_str(&custody.get(&addr).unwrap().unwrap()).unwrap();
        let _ = std::fs::remove_file(&db_path);
        assert_eq!(
            rec.previous_epoch,
            Some(PriorEpochMark {
                device_epoch: 3,
                last_seq: 1,
                confirmed_seq: 1
            })
        );
    }

    // READER RED 3: two concurrent sign calls (the commands run on
    // spawn_blocking threads) must never assign one sequence twice.
    #[test]
    fn reader_red_concurrent_signs_never_share_a_sequence() {
        let db_path = temp_db_path("rr3");
        let custody = Arc::new(MemoryCustody::new());
        let fence = OriginFence::from_starts(&[GABLE_ORIGIN]).unwrap();
        let counter = Arc::new(OfflineCounter::new(
            PROFILE_ID,
            custody,
            &db_path,
            Some(fence),
        ));
        counter
            .enrol_register(Some(GABLE_ORIGIN), "REG-01", 3)
            .unwrap();
        let mut snap = sample_snapshot();
        snap.policy.max_queue_count = 100_000;
        snap.policy.max_queue_total_cents = i64::MAX / 4;
        counter.install_test_snapshot(snap).unwrap();
        let mut seqs = Vec::new();
        let mut errors = Vec::new();
        for round in 0..40 {
            let barrier = Arc::new(std::sync::Barrier::new(2));
            let hs: Vec<_> = (0..2)
                .map(|k| {
                    let c = counter.clone();
                    let b = barrier.clone();
                    std::thread::spawn(move || {
                        b.wait();
                        c.sign_and_queue_sale(
                            Some(GABLE_ORIGIN),
                            walk_in_sale_request(
                                &format!("77777777-7777-7777-{:04}-{:012}", k, round),
                                "1",
                                5_000,
                            ),
                        )
                    })
                })
                .collect();
            for h in hs {
                match h.join().unwrap() {
                    Ok(s) => seqs.push(s.device_seq),
                    Err(e) => errors.push(e.code),
                }
            }
        }
        let queued = counter
            .queue_status(Some(GABLE_ORIGIN))
            .map(|q| q.items.len())
            .unwrap_or(0);
        let mut uniq = seqs.clone();
        uniq.sort();
        uniq.dedup();
        let _ = std::fs::remove_file(&db_path);
        assert_eq!(
            uniq.len(),
            seqs.len(),
            "signed {} sales, {} distinct sequences, {} in the store; errors {:?}",
            seqs.len(),
            uniq.len(),
            queued,
            &errors[..errors.len().min(5)]
        );
        assert_eq!(
            queued,
            seqs.len(),
            "receipts signed {} but store holds {}",
            seqs.len(),
            queued
        );
        assert!(
            errors.is_empty(),
            "one counter's own signs collided: {errors:?}"
        );
    }

    // RULE (ADR 0011 section 5.1): two handles on one store file (two shell
    // processes) never assign one sequence twice: the write lock is taken
    // before the meta is read again, and a taken sequence fails its insert
    // rather than overwriting the signed sale that holds it.
    #[test]
    fn two_handles_on_one_store_never_share_a_sequence() {
        let db_path = temp_db_path("handles");
        let custody = Arc::new(MemoryCustody::new());
        let fence = OriginFence::from_starts(&[GABLE_ORIGIN]).unwrap();
        let first = Arc::new(OfflineCounter::new(
            PROFILE_ID,
            custody.clone(),
            &db_path,
            Some(fence.clone()),
        ));
        let second = Arc::new(OfflineCounter::new(
            PROFILE_ID,
            custody,
            &db_path,
            Some(fence),
        ));
        first
            .enrol_register(Some(GABLE_ORIGIN), "REG-01", 3)
            .unwrap();
        let mut snap = sample_snapshot();
        snap.policy.max_queue_count = 100_000;
        snap.policy.max_queue_total_cents = i64::MAX / 4;
        first.install_test_snapshot(snap).unwrap();
        let mut seqs = Vec::new();
        for round in 0..40 {
            let barrier = Arc::new(std::sync::Barrier::new(2));
            let hs: Vec<_> = [first.clone(), second.clone()]
                .into_iter()
                .enumerate()
                .map(|(k, c)| {
                    let b = barrier.clone();
                    std::thread::spawn(move || {
                        b.wait();
                        c.sign_and_queue_sale(
                            Some(GABLE_ORIGIN),
                            walk_in_sale_request(
                                &format!("77777777-7777-7777-{k:04}-{round:012}"),
                                "1",
                                5_000,
                            ),
                        )
                    })
                })
                .collect();
            for h in hs {
                if let Ok(s) = h.join().unwrap() {
                    seqs.push(s.device_seq);
                }
            }
        }
        let stored: Vec<i64> = first
            .queue_status(Some(GABLE_ORIGIN))
            .map(|q| q.items.iter().map(|i| i.sale.device_seq).collect())
            .unwrap_or_default();
        let _ = std::fs::remove_file(&db_path);
        let mut uniq = seqs.clone();
        uniq.sort();
        uniq.dedup();
        assert_eq!(uniq.len(), seqs.len(), "a sequence was signed twice");
        assert_eq!(uniq, stored, "every signed sale is the one the store holds");
    }

    // READER RED 4: an account sale that posted is not in the snapshot's open
    // receivable, so the core must keep counting it against the room of the
    // snapshot it was sold under.
    #[test]
    fn reader_red_posted_account_sale_still_counts_against_snapshot_room() {
        let db_path = temp_db_path("rr4");
        let custody = Arc::new(MemoryCustody::new());
        let fence = OriginFence::from_starts(&[GABLE_ORIGIN]).unwrap();
        let counter = OfflineCounter::new(PROFILE_ID, custody, &db_path, Some(fence));
        counter
            .enrol_register(Some(GABLE_ORIGIN), "REG-01", 3)
            .unwrap();
        counter.install_test_snapshot(sample_snapshot()).unwrap();
        let req = |cid: &str, amount: i64| SignSaleRequest {
            client_id: cid.to_string(),
            snapshot_id: "11111111-1111-1111-1111-111111111111".to_string(),
            till_session_id: "cccccccc-cccc-cccc-cccc-cccccccccccc".to_string(),
            cashier_id: "dddddddd-dddd-dddd-dddd-dddddddddddd".to_string(),
            customer_id: Some("22222222-2222-2222-2222-222222222222".to_string()),
            buyer_contact_id: Some("44444444-4444-4444-4444-444444444444".to_string()),
            customer_po: Some("PO-900".to_string()),
            business_date: "2026-04-19".to_string(),
            sold_at: "2026-04-19T14:30:00Z".to_string(),
            lines: vec![SignLineRequest {
                id: "55555555-5555-5555-5555-555555555556".to_string(),
                product_id: "aaaa0002-0000-0000-0000-000000000002".to_string(),
                uom: "LF".to_string(),
                tally: Some(TallyRequestInput {
                    rows: vec![TallyRowRequestInput {
                        pieces: 120,
                        length_ft: "10".to_string(),
                    }],
                }),
                ..Default::default()
            }],
            tenders: vec![SignTenderRequest {
                method: "account".to_string(),
                amount_cents: amount,
                reference: None,
                id_checked: true,
                id_type: Some("drivers_license".to_string()),
            }],
            ..Default::default()
        };
        let probe = counter
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                req("77777777-7777-7777-7777-7777777777b0", 1),
            )
            .unwrap_err();
        let total: i64 = probe
            .message
            .split_whitespace()
            .find_map(|w| w.parse().ok())
            .expect(&probe.message);
        assert!(total * 2 > 150_000 && total <= 120_000, "total {total}");
        let first = counter
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                req("77777777-7777-7777-7777-7777777777b1", total),
            )
            .unwrap();
        counter
            .record_sync_outcomes(
                Some(GABLE_ORIGIN),
                SyncBatchOutcome {
                    confirmed_seq: first.device_seq,
                    items: vec![SyncOutcomeItem {
                        client_id: first.client_id.clone(),
                        device_seq: first.device_seq,
                        status: QueueItemState::Synced,
                        invoice_id: None,
                        invoice_number: None,
                        reason_code: None,
                        reason_message: None,
                    }],
                },
            )
            .unwrap();
        // The posted sale is not in the snapshot's on hand either: 1200 LF is
        // 800 BF of the 1000 the snapshot was issued with.
        let in_stock = counter
            .load_snapshot(Some(GABLE_ORIGIN))
            .unwrap()
            .products
            .into_iter()
            .find(|p| p.id == "aaaa0002-0000-0000-0000-000000000002")
            .unwrap()
            .in_stock;
        assert_eq!(
            parse_quantity(&in_stock),
            parse_quantity("200"),
            "on hand {in_stock}"
        );
        let second = counter.sign_and_queue_sale(
            Some(GABLE_ORIGIN),
            req("77777777-7777-7777-7777-7777777777b2", total),
        );
        let _ = std::fs::remove_file(&db_path);
        assert!(
            second.is_err(),
            "two account sales of {total} each signed against a room of 150000 on one snapshot"
        );
    }

    // ---- slice C2: the core's own snapshot fetch, preview and sync ----

    struct FakeServer {
        snapshot: Mutex<Option<ServerAnswer>>,
        sync: Mutex<Option<Box<dyn Fn(&serde_json::Value) -> ServerAnswer + Send>>>,
        calls: Mutex<Vec<(String, String)>>,
    }

    impl FakeServer {
        fn new() -> Self {
            Self {
                snapshot: Mutex::new(None),
                sync: Mutex::new(None),
                calls: Mutex::new(vec![]),
            }
        }
        fn answering_snapshot(self, status: u16, body: String) -> Self {
            *self.snapshot.lock().unwrap() = Some(ServerAnswer { status, body });
            self
        }
        fn answering_sync(
            self,
            f: impl Fn(&serde_json::Value) -> ServerAnswer + Send + 'static,
        ) -> Self {
            *self.sync.lock().unwrap() = Some(Box::new(f));
            self
        }
        fn calls(&self) -> Vec<(String, String)> {
            self.calls.lock().unwrap().clone()
        }
    }

    impl OfflineServer for FakeServer {
        fn get_snapshot(
            &self,
            register_id: &str,
            high_seq: i64,
        ) -> Result<ServerAnswer, OfflineError> {
            self.calls
                .lock()
                .unwrap()
                .push(("snapshot".into(), format!("{register_id}:{high_seq}")));
            match self.snapshot.lock().unwrap().as_ref() {
                Some(a) => Ok(ServerAnswer {
                    status: a.status,
                    body: a.body.clone(),
                }),
                None => Err(server::unreachable()),
            }
        }
        fn post_sync(&self, body: &str, key: &str) -> Result<ServerAnswer, OfflineError> {
            self.calls
                .lock()
                .unwrap()
                .push(("sync".into(), body.to_string()));
            let v: serde_json::Value = serde_json::from_str(body).unwrap();
            assert_eq!(
                v["batch_id"].as_str(),
                Some(key),
                "the idempotency key is the batch id"
            );
            match self.sync.lock().unwrap().as_ref() {
                Some(f) => Ok(f(&v)),
                None => Err(server::unreachable()),
            }
        }
    }

    fn enrolled_counter(label: &str) -> (OfflineCounter, PathBuf) {
        let db_path = temp_db_path(label);
        let custody = Arc::new(MemoryCustody::new());
        let fence = OriginFence::from_starts(&[GABLE_ORIGIN]).unwrap();
        let counter = OfflineCounter::new(PROFILE_ID, custody, &db_path, Some(fence));
        counter
            .enrol_register(Some(GABLE_ORIGIN), "REG-01", 3)
            .unwrap();
        (counter, db_path)
    }

    fn posted_answer(v: &serde_json::Value, outcome: &str) -> ServerAnswer {
        let items: Vec<serde_json::Value> = v["items"]
            .as_array()
            .unwrap()
            .iter()
            .map(|it| {
                serde_json::json!({
                    "client_id": it["client_id"], "device_epoch": it["device_epoch"],
                    "device_seq": it["device_seq"], "outcome": outcome,
                    "invoice_id": null, "invoice_number": if outcome == "posted" { serde_json::json!("IN-000101") } else { serde_json::Value::Null },
                    "reason_code": if outcome == "refused" { serde_json::json!("insufficient_stock") } else { serde_json::Value::Null },
                    "reason_message": null,
                })
            })
            .collect();
        let high = v["items"]
            .as_array()
            .unwrap()
            .iter()
            .map(|it| it["device_seq"].as_i64().unwrap())
            .max()
            .unwrap();
        ServerAnswer {
            status: 200,
            body: serde_json::json!({
                "batch_id": v["batch_id"], "register_id": v["register_id"], "device_epoch": v["device_epoch"],
                "confirmed_seq": high, "open_exception_count": if outcome == "refused" { 1 } else { 0 },
                "items": items,
            })
            .to_string(),
        }
    }

    // C2 (ADR 0011 5.3): the counter's figures are the core's. The preview
    // prices through the sign path's own code, so the total it shows is the
    // total the sale is signed with, and it writes nothing.
    #[test]
    fn the_preview_answers_the_sign_paths_figures_and_writes_nothing() {
        let (counter, db_path) = enrolled_counter("c2-preview");
        counter.install_test_snapshot(sample_snapshot()).unwrap();
        let mut req = walk_in_sale_request("77777777-7777-7777-7777-7777777777c1", "3", 0);
        req.tenders.clear();
        let preview = counter.price_cart(Some(GABLE_ORIGIN), req.clone()).unwrap();
        assert_eq!(preview.change_cents, None);
        assert_eq!(preview.tender_refusal, None);
        assert_eq!(
            counter
                .queue_status(Some(GABLE_ORIGIN))
                .unwrap()
                .items
                .len(),
            0
        );
        req.tenders = vec![SignTenderRequest {
            method: "cash".into(),
            amount_cents: preview.total_cents + 500,
            reference: None,
            id_checked: false,
            id_type: None,
        }];
        let tendered = counter.price_cart(Some(GABLE_ORIGIN), req.clone()).unwrap();
        assert_eq!(tendered.change_cents, Some(500));
        let signed = counter
            .sign_and_queue_sale(Some(GABLE_ORIGIN), req)
            .unwrap();
        let _ = std::fs::remove_file(&db_path);
        assert_eq!(signed.total_cents, preview.total_cents);
        assert_eq!(signed.tax_cents, preview.tax_cents);
        assert_eq!(signed.lines, preview.lines);
        assert_eq!(signed.change_cents, 500);
    }

    // C2 (ADR 0011 sections 1 and 4): a tender the branch does not take
    // offline is answered as a refusal on the preview, a kit line is refused
    // from the core's own kit flag, and the queue's room is the caps less
    // what waits.
    #[test]
    fn the_preview_names_the_refusals_and_the_room() {
        let (counter, db_path) = enrolled_counter("c2-preview-refusals");
        let mut snap = sample_snapshot();
        snap.products[0].is_kit = false;
        counter.install_test_snapshot(snap.clone()).unwrap();
        let mut card = walk_in_sale_request("77777777-7777-7777-7777-7777777777c2", "1", 0);
        card.tenders[0].method = "card".into();
        card.tenders[0].amount_cents = 1;
        let preview = counter.price_cart(Some(GABLE_ORIGIN), card).unwrap();
        assert!(
            preview.tender_refusal.is_some(),
            "a card tender offline is refused"
        );
        assert_eq!(preview.queue_room_count, snap.policy.max_queue_count);
        assert_eq!(preview.queue_room_cents, snap.policy.max_queue_total_cents);
        snap.products[0].is_kit = true;
        counter.install_test_snapshot(snap).unwrap();
        let kit = counter
            .price_cart(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-7777777777c3", "1", 0),
            )
            .unwrap_err();
        let _ = std::fs::remove_file(&db_path);
        assert_eq!(kit.code, "kit_not_offline");
    }

    // C2 (ADR 0011 5.3): a sale declared for a different customer names the
    // result unknown cart and replaces nothing; the declaration and the
    // replaced cart are signed fields, absent when not given.
    #[test]
    fn the_declaration_is_checked_and_signed() {
        let (counter, db_path) = enrolled_counter("c2-declaration");
        counter.install_test_snapshot(sample_snapshot()).unwrap();
        let cart = "99999999-9999-4999-8999-999999999999".to_string();
        let mut bad = walk_in_sale_request("77777777-7777-7777-7777-7777777777d1", "1", 100_000);
        bad.different_customer = true;
        let err = counter
            .sign_and_queue_sale(Some(GABLE_ORIGIN), bad)
            .unwrap_err();
        assert_eq!(err.code, "declaration_invalid");
        let mut both = walk_in_sale_request("77777777-7777-7777-7777-7777777777d2", "1", 100_000);
        both.different_customer = true;
        both.unknown_cart_id = Some(cart.clone());
        both.replaces_sale_id = Some(cart.clone());
        assert_eq!(
            counter
                .sign_and_queue_sale(Some(GABLE_ORIGIN), both)
                .unwrap_err()
                .code,
            "declaration_invalid"
        );
        let mut replaces =
            walk_in_sale_request("77777777-7777-7777-7777-7777777777d3", "1", 100_000);
        replaces.replaces_sale_id = Some(cart.clone());
        let signed = counter
            .sign_and_queue_sale(Some(GABLE_ORIGIN), replaces)
            .unwrap();
        let json = serde_json::to_value(&signed).unwrap();
        assert_eq!(json["replaces_sale_id"].as_str(), Some(cart.as_str()));
        assert!(json.get("different_customer").is_none());
        assert!(json.get("unknown_cart_id").is_none());
        let enrol = counter.enrolment_status(Some(GABLE_ORIGIN)).unwrap();
        verify_sale_signature(&enrol.public_key_hex, &signed).unwrap();
        let mut declared =
            walk_in_sale_request("77777777-7777-7777-7777-7777777777d4", "1", 100_000);
        declared.different_customer = true;
        declared.unknown_cart_id = Some(cart.clone());
        let signed = counter
            .sign_and_queue_sale(Some(GABLE_ORIGIN), declared)
            .unwrap();
        let _ = std::fs::remove_file(&db_path);
        let json = serde_json::to_value(&signed).unwrap();
        assert_eq!(json["different_customer"].as_bool(), Some(true));
        assert!(json.get("replaces_sale_id").is_none());
        verify_sale_signature(&enrol.public_key_hex, &signed).unwrap();
    }

    // C2 (ADR 0011 5.4 and 6.2): the core fetches its own snapshot, only once
    // every queued sale is answered, naming its high sequence; it keeps the
    // copy only when it is the register's, of its epoch, since the session.
    #[test]
    fn the_snapshot_refresh_waits_for_the_drain_and_checks_the_copy() {
        let (counter, db_path) = enrolled_counter("c2-refresh");
        let mut fresh = sample_snapshot();
        fresh.snapshot_id = "11111111-1111-1111-1111-1111111111aa".into();
        let server = FakeServer::new()
            .answering_snapshot(200, serde_json::to_string(&fresh).unwrap())
            .answering_sync(|v| posted_answer(v, "posted"));
        // first fetch on an empty queue
        let r = counter
            .refresh_snapshot(Some(GABLE_ORIGIN), &server)
            .unwrap();
        assert_eq!(r.status, "updated");
        assert_eq!(
            counter
                .load_snapshot(Some(GABLE_ORIGIN))
                .unwrap()
                .snapshot_id,
            fresh.snapshot_id
        );
        let mut req = walk_in_sale_request("77777777-7777-7777-7777-7777777777e1", "1", 100_000);
        req.snapshot_id = fresh.snapshot_id.clone();
        counter
            .sign_and_queue_sale(Some(GABLE_ORIGIN), req)
            .unwrap();
        // a waiting sale holds the refresh, and the server is not asked
        let before = server.calls().len();
        let held = counter
            .refresh_snapshot(Some(GABLE_ORIGIN), &server)
            .unwrap_err();
        assert_eq!(held.code, "queue_not_drained");
        assert!(held.message.contains("REG-01-3-000001"), "{}", held.message);
        assert_eq!(server.calls().len(), before);
        // drained, the refresh names the high sequence and the same copy is unchanged
        counter.sync_queue(Some(GABLE_ORIGIN), &server).unwrap();
        let r = counter
            .refresh_snapshot(Some(GABLE_ORIGIN), &server)
            .unwrap();
        assert_eq!(r.status, "unchanged");
        assert_eq!(server.calls().last().unwrap().1, "REG-01:1");
        // another epoch's copy is refused and the held copy stays
        let mut other = fresh.clone();
        other.device_epoch = 2;
        other.snapshot_id = "11111111-1111-1111-1111-1111111111bb".into();
        let server2 =
            FakeServer::new().answering_snapshot(200, serde_json::to_string(&other).unwrap());
        assert_eq!(
            counter
                .refresh_snapshot(Some(GABLE_ORIGIN), &server2)
                .unwrap_err()
                .code,
            "snapshot_unknown"
        );
        // the server's refusal passes through with its words
        let server3 = FakeServer::new().answering_snapshot(
            409,
            r#"{"error":{"code":"till_not_open","message":"open the till first"}}"#.into(),
        );
        let refused = counter
            .refresh_snapshot(Some(GABLE_ORIGIN), &server3)
            .unwrap_err();
        let kept = counter
            .load_snapshot(Some(GABLE_ORIGIN))
            .unwrap()
            .snapshot_id;
        let _ = std::fs::remove_file(&db_path);
        assert_eq!(refused.code, "till_not_open");
        assert_eq!(refused.message, "open the till first");
        assert_eq!(kept, fresh.snapshot_id);
    }

    // C2 (ADR 0011 sections 3, 5.1 and 6.2): the core sends its own queue
    // with its own mark, and only the server's answer settles an item: posted
    // and duplicate settle, refused and held stay with a manager (sent again
    // next time), resolved settles, an error waits for the next sync; the
    // confirmed mark moves to the server's figure.
    #[test]
    fn the_sync_sends_the_queue_and_records_only_the_servers_answer() {
        let (counter, db_path) = enrolled_counter("c2-sync");
        counter.install_test_snapshot(sample_snapshot()).unwrap();
        let a = counter
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-7777777777f1", "1", 100_000),
            )
            .unwrap();
        let b = counter
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-7777777777f2", "1", 100_000),
            )
            .unwrap();
        let refuse_b = b.client_id.clone();
        let server = FakeServer::new().answering_sync(move |v| {
            let mut ans = posted_answer(v, "posted");
            let mut doc: serde_json::Value = serde_json::from_str(&ans.body).unwrap();
            for it in doc["items"].as_array_mut().unwrap() {
                if it["client_id"] == refuse_b.as_str() {
                    it["outcome"] = "refused".into();
                    it["reason_code"] = "insufficient_stock".into();
                }
            }
            doc["open_exception_count"] = 1.into();
            ans.body = doc.to_string();
            ans
        });
        let report = counter.sync_queue(Some(GABLE_ORIGIN), &server).unwrap();
        let (_, body) = server.calls().last().unwrap().clone();
        let sent: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert_eq!(sent["register_id"], "REG-01");
        assert_eq!(sent["device_epoch"], 3);
        assert_eq!(sent["reported_seq"], 2);
        let enrol = counter.enrolment_status(Some(GABLE_ORIGIN)).unwrap();
        for it in sent["items"].as_array().unwrap() {
            let sale: SignedOfflineSale = serde_json::from_value(it.clone()).unwrap();
            verify_sale_signature(&enrol.public_key_hex, &sale).unwrap();
        }
        assert_eq!((report.sent, report.posted, report.refused), (2, 1, 1));
        assert_eq!(report.open_exception_count, 1);
        assert_eq!(report.confirmed_seq, 2);
        assert_eq!(report.queue.confirmed_seq, 2);
        assert_eq!(report.queue.review_count, 1);
        let state_of = |q: &QueueStatusView, id: &str| {
            q.items
                .iter()
                .find(|e| e.sale.client_id == id)
                .unwrap()
                .state
        };
        assert_eq!(
            state_of(&report.queue, &a.client_id),
            QueueItemState::Synced
        );
        assert_eq!(
            state_of(&report.queue, &b.client_id),
            QueueItemState::Review
        );
        // the refused one is sent again, alone; resolved settles it
        let server = FakeServer::new().answering_sync(|v| posted_answer(v, "resolved"));
        let report = counter.sync_queue(Some(GABLE_ORIGIN), &server).unwrap();
        let (_, body) = server.calls().last().unwrap().clone();
        let sent: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert_eq!(sent["items"].as_array().unwrap().len(), 1);
        assert_eq!(report.resolved, 1);
        assert_eq!(
            state_of(&report.queue, &b.client_id),
            QueueItemState::Synced
        );
        // an item the server could not judge waits for the next sync
        let c = counter
            .sign_and_queue_sale(
                Some(GABLE_ORIGIN),
                walk_in_sale_request("77777777-7777-7777-7777-7777777777f3", "1", 100_000),
            )
            .unwrap();
        let server = FakeServer::new().answering_sync(|v| {
            let mut ans = posted_answer(v, "error");
            let mut doc: serde_json::Value = serde_json::from_str(&ans.body).unwrap();
            doc["confirmed_seq"] = 2.into();
            ans.body = doc.to_string();
            ans
        });
        let report = counter.sync_queue(Some(GABLE_ORIGIN), &server).unwrap();
        assert_eq!(report.errors, 1);
        assert_eq!(
            state_of(&report.queue, &c.client_id),
            QueueItemState::Pending
        );
        // a refusal of the whole batch changes nothing
        let server = FakeServer::new().answering_sync(|_| ServerAnswer {
            status: 409,
            body: r#"{"error":{"code":"money_never_queues","message":"not at this branch"}}"#
                .into(),
        });
        let err = counter.sync_queue(Some(GABLE_ORIGIN), &server).unwrap_err();
        let q = counter.queue_status(Some(GABLE_ORIGIN)).unwrap();
        // the server being away changes nothing either
        let away = FakeServer::new();
        let gone = counter.sync_queue(Some(GABLE_ORIGIN), &away).unwrap_err();
        let _ = std::fs::remove_file(&db_path);
        assert_eq!(err.code, "money_never_queues");
        assert_eq!(state_of(&q, &c.client_id), QueueItemState::Pending);
        assert_eq!(gone.code, "server_unreachable");
    }

    // C2 (ADR 0011 6.1, the shared signing vector O3 asked for): a sale the
    // core signs with a fixed seed has a fixed signature, and the Go sync
    // verifies the same file (core/internal/pos/testdata/offline-signed-sale.json).
    #[test]
    fn the_shared_signing_vector_is_the_cores_signature() {
        #[derive(Deserialize)]
        struct Vector {
            seed_hex: String,
            public_key_hex: String,
            sale: SignedOfflineSale,
        }
        let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("../../core/internal/pos/testdata/offline-signed-sale.json");
        let raw = std::fs::read_to_string(&path).expect("the shared signing vector");
        let v: Vector = serde_json::from_str(&raw).unwrap();
        let mut unsigned = v.sale.clone();
        unsigned.device_sig = String::new();
        let canonical = canonical_sale_bytes(&unsigned).unwrap();
        let sig = store::sign_canonical_for_vector(&v.seed_hex, &canonical).unwrap();
        assert_eq!(
            sig, v.sale.device_sig,
            "the core's signature over the vector moved"
        );
        verify_sale_signature(&v.public_key_hex, &v.sale).unwrap();
    }
}
