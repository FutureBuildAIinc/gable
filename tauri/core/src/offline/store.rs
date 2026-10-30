// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! SQLite queue store, AEAD sealing, Ed25519 signing, snapshot verification,
//! and rollback/crash recovery for the offline counter (ADR 0011 sections 4,
//! 5.1, and 7).

use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::time::Duration;

use base64::Engine as _;
use ring::aead::{Aad, LessSafeKey, Nonce, UnboundKey, AES_256_GCM, NONCE_LEN};
use ring::digest::{digest, SHA256};
use ring::rand::{SecureRandom, SystemRandom};
use ring::signature::{Ed25519KeyPair, KeyPair, UnparsedPublicKey, ED25519};
use rusqlite::{params, Connection, OptionalExtension, TransactionBehavior};
use url::Url;
use zeroize::Zeroize;

use crate::custody::{Address, Custody, OfflineSlot};
use crate::fence::OriginFence;
use crate::offline::arithmetic::{
    complete_tally, convert_stock, extend, format_wire_quantity, parse_quantity, parse_tax_rate,
    resolve_line_pair, settle_tenders, tax_at, OfflineTenderCalc, Pair, TallyRowCalc, ONE_QTY,
};
use crate::offline::model::{
    CartPreview, EnrolmentPublic, EnrolmentRecord, HighWaterMark, OfflineError, OfflineSnapshot,
    PriorEpochMark, QueueItemState, QueueStatusView, QueuedSaleEntry, SignSaleRequest,
    SignedOfflineSale, SignedSaleLine, SignedSaleTender, SignedTally, SignedTallyRow,
    SignedTaxLine, SnapshotPriceRow, SnapshotProduct, SnapshotRefresh, StoreMeta, SyncBatchOutcome,
    SyncOutcomeItem, SyncReport,
};
use crate::offline::server::{refusal_of, OfflineServer};

const VALID_ID_TYPES: [&str; 5] = [
    "drivers_license",
    "state_id",
    "passport",
    "military_id",
    "other",
];

struct ZeroizingKey([u8; 32]);

impl Drop for ZeroizingKey {
    fn drop(&mut self) {
        self.0.zeroize();
    }
}

/// Fault-injection hook used by unit tests to simulate a crash between the
/// SQLite store write, the keychain mark update, signing, and receipt print.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CrashPoint {
    AfterStoreWriteBeforeKeychainMark,
    AfterKeychainMarkBeforeSign,
    AfterSignBeforePrint,
}

/// The Rust core's offline counter engine: fenced by origin when an origin
/// fence is present, backed by the OS keychain (`Custody`) and a local SQLite
/// file whose every row is sealed with AES-256-GCM.
pub struct OfflineCounter {
    profile_identifier: String,
    custody: Arc<dyn Custody>,
    store_path: PathBuf,
    fence: Mutex<Option<OriginFence>>,
    embedded_bundle: Mutex<bool>,
    crash_point: Mutex<Option<CrashPoint>>,
    /// Held by every operation from its first read of the store to its last
    /// write, so two commands on blocking threads never interleave: one
    /// sequence is never assigned twice and the caps are never checked
    /// against a queue another sale is changing.
    ops: Mutex<()>,
}

impl OfflineCounter {
    pub fn new(
        profile_identifier: &str,
        custody: Arc<dyn Custody>,
        store_path: impl Into<PathBuf>,
        fence: Option<OriginFence>,
    ) -> Self {
        let embedded = fence.is_none();
        Self {
            profile_identifier: profile_identifier.to_string(),
            custody,
            store_path: store_path.into(),
            fence: Mutex::new(fence),
            embedded_bundle: Mutex::new(embedded),
            crash_point: Mutex::new(None),
            ops: Mutex::new(()),
        }
    }

    /// A panic inside an operation leaves the store as a crash would, and
    /// the next open recovers it, so a poisoned lock is taken as it stands.
    fn lock_ops(&self) -> MutexGuard<'_, ()> {
        self.ops.lock().unwrap_or_else(PoisonError::into_inner)
    }

    pub fn set_fence(&self, fence: Option<OriginFence>, embedded: bool) {
        *self.fence.lock().expect("offline fence lock") = fence;
        *self.embedded_bundle.lock().expect("offline embedded lock") = embedded;
    }

    pub fn set_crash_point(&self, point: Option<CrashPoint>) {
        *self.crash_point.lock().expect("offline crash lock") = point;
    }

    pub fn store_path(&self) -> &Path {
        &self.store_path
    }

    fn check_origin(&self, caller_origin: Option<&str>) -> Result<(), OfflineError> {
        if *self.embedded_bundle.lock().expect("offline embedded lock") {
            return Err(OfflineError::embedded_bundle_refused());
        }
        let Some(url) = caller_origin else {
            return Err(OfflineError::origin_denied());
        };
        let guard = self.fence.lock().expect("offline fence lock");
        match guard.as_ref() {
            Some(fence) => {
                if fence.allows(url) {
                    Ok(())
                } else {
                    Err(OfflineError::origin_denied())
                }
            }
            None => Err(OfflineError::embedded_bundle_refused()),
        }
    }

    /// Reads the enrolled Gable URL from the keychain without requiring an
    /// active origin fence (used by `shell.rs` at startup to know which Gable
    /// URL to load into the work shell).
    pub fn enrolled_gable_url(&self) -> Result<Option<String>, OfflineError> {
        let addr = Address::offline(&self.profile_identifier, OfflineSlot::Enrolment)
            .map_err(|_| OfflineError::new("bad_profile", "invalid profile identifier"))?;
        let raw = self
            .custody
            .get(&addr)
            .map_err(|_| OfflineError::keychain_unavailable())?;
        let Some(json_str) = raw else {
            return Ok(None);
        };
        let rec: EnrolmentRecord =
            serde_json::from_str(&json_str).map_err(|_| OfflineError::re_enrol_required())?;
        Ok(Some(rec.gable_url.clone()))
    }

    fn get_or_create_aead_key(&self) -> Result<ZeroizingKey, OfflineError> {
        let addr = Address::offline(&self.profile_identifier, OfflineSlot::AeadKey)
            .map_err(|_| OfflineError::new("bad_profile", "invalid profile identifier"))?;
        if let Some(existing) = self
            .custody
            .get(&addr)
            .map_err(|_| OfflineError::keychain_unavailable())?
        {
            let bytes =
                hex::decode(existing.trim()).map_err(|_| OfflineError::re_enrol_required())?;
            let arr: [u8; 32] = bytes
                .try_into()
                .map_err(|_| OfflineError::re_enrol_required())?;
            return Ok(ZeroizingKey(arr));
        }
        let rng = SystemRandom::new();
        let mut raw = [0u8; 32];
        rng.fill(&mut raw)
            .map_err(|_| OfflineError::new("rng_failed", "failed to generate AEAD key"))?;
        let mut encoded = hex::encode(raw);
        let res = self
            .custody
            .set(&addr, &encoded)
            .map_err(|_| OfflineError::keychain_unavailable());
        encoded.zeroize();
        res?;
        Ok(ZeroizingKey(raw))
    }

    fn read_aead_key(&self) -> Result<ZeroizingKey, OfflineError> {
        let addr = Address::offline(&self.profile_identifier, OfflineSlot::AeadKey)
            .map_err(|_| OfflineError::new("bad_profile", "invalid profile identifier"))?;
        let existing = self
            .custody
            .get(&addr)
            .map_err(|_| OfflineError::keychain_unavailable())?
            .ok_or_else(OfflineError::re_enrol_required)?;
        let bytes = hex::decode(existing.trim()).map_err(|_| OfflineError::re_enrol_required())?;
        let arr: [u8; 32] = bytes
            .try_into()
            .map_err(|_| OfflineError::re_enrol_required())?;
        Ok(ZeroizingKey(arr))
    }

    fn read_enrolment(&self) -> Result<EnrolmentRecord, OfflineError> {
        let addr = Address::offline(&self.profile_identifier, OfflineSlot::Enrolment)
            .map_err(|_| OfflineError::new("bad_profile", "invalid profile identifier"))?;
        let raw = self
            .custody
            .get(&addr)
            .map_err(|_| OfflineError::keychain_unavailable())?
            .ok_or_else(|| {
                OfflineError::new(
                    "not_enrolled",
                    "This register is not enrolled for offline selling.",
                )
            })?;
        serde_json::from_str(&raw).map_err(|_| OfflineError::re_enrol_required())
    }

    fn read_mark(&self) -> Result<HighWaterMark, OfflineError> {
        let addr = Address::offline(&self.profile_identifier, OfflineSlot::Mark)
            .map_err(|_| OfflineError::new("bad_profile", "invalid profile identifier"))?;
        let raw = self
            .custody
            .get(&addr)
            .map_err(|_| OfflineError::keychain_unavailable())?
            .ok_or_else(OfflineError::re_enrol_required)?;
        serde_json::from_str(&raw).map_err(|_| OfflineError::re_enrol_required())
    }

    fn write_mark(&self, mark: &HighWaterMark) -> Result<(), OfflineError> {
        let addr = Address::offline(&self.profile_identifier, OfflineSlot::Mark)
            .map_err(|_| OfflineError::new("bad_profile", "invalid profile identifier"))?;
        let json_str = serde_json::to_string(mark)
            .map_err(|_| OfflineError::new("serialize_failed", "failed to encode mark"))?;
        self.custody
            .set(&addr, &json_str)
            .map_err(|_| OfflineError::keychain_unavailable())
    }

    /// Enrols this register for a new `device_epoch` (ADR 0011 section 5.1):
    /// generates an Ed25519 signing seed inside the keychain, records the
    /// Gable URL, initializes the high-water mark, and creates the
    /// AEAD-sealed SQLite store for that epoch. Returns the public key the
    /// server records for the register.
    ///
    /// Enrolment is an owner or admin act online (6.1), so this is not a
    /// webview command: it runs only on the server's enrolment answer (O2).
    /// Even then the Gable URL is the fenced origin of the caller, never an
    /// argument; the epoch must be above the current one; an enrolment over
    /// sales the server has not confirmed is refused, so nothing unsynced is
    /// wiped; and the replaced epoch's mark is kept for the unknown tail.
    pub fn enrol_register(
        &self,
        caller_origin: Option<&str>,
        register_id: &str,
        device_epoch: i64,
    ) -> Result<EnrolmentPublic, OfflineError> {
        self.check_origin(caller_origin)?;
        let _ops = self.lock_ops();
        let reg = register_id.trim();
        if reg.is_empty() {
            return Err(OfflineError::new(
                "invalid_register_id",
                "register_id is required",
            ));
        }
        if device_epoch <= 0 {
            return Err(OfflineError::new(
                "invalid_epoch",
                "device_epoch must be positive",
            ));
        }
        let gable_url = caller_origin
            .and_then(|u| Url::parse(u).ok())
            .map(|u| format!("{}/", u.origin().ascii_serialization()))
            .ok_or_else(OfflineError::origin_denied)?;
        let previous_epoch = self.replaced_epoch(device_epoch)?;
        let aead_key = self.get_or_create_aead_key()?;
        let rng = SystemRandom::new();
        let mut seed = [0u8; 32];
        rng.fill(&mut seed)
            .map_err(|_| OfflineError::new("rng_failed", "failed to generate signing key"))?;
        let key_pair = Ed25519KeyPair::from_seed_unchecked(&seed)
            .map_err(|_| OfflineError::new("key_failed", "failed to construct Ed25519 key"))?;
        let public_key_hex = hex::encode(key_pair.public_key().as_ref());
        let signing_seed_hex = hex::encode(seed);
        seed.zeroize();

        let enrolment = EnrolmentRecord {
            register_id: reg.to_string(),
            device_epoch,
            signing_seed_hex,
            public_key_hex: public_key_hex.clone(),
            gable_url,
            previous_epoch,
        };
        let mark = HighWaterMark {
            register_id: reg.to_string(),
            device_epoch,
            last_seq: 0,
            chain_head: "GENESIS".to_string(),
            confirmed_seq: 0,
        };

        if let Some(parent) = self.store_path.parent() {
            if !parent.as_os_str().is_empty() {
                std::fs::create_dir_all(parent).map_err(|_| {
                    OfflineError::new("store_io_error", "failed to create store directory")
                })?;
            }
        }
        let conn = Connection::open(&self.store_path)
            .map_err(|_| OfflineError::new("store_io_error", "failed to open SQLite store"))?;
        init_schema(&conn)?;
        conn.execute("DELETE FROM offline_sales", [])
            .map_err(|_| OfflineError::new("store_io_error", "failed to clear old sales"))?;
        conn.execute("DELETE FROM offline_snapshot", [])
            .map_err(|_| OfflineError::new("store_io_error", "failed to clear old snapshot"))?;
        conn.execute("DELETE FROM offline_meta", [])
            .map_err(|_| OfflineError::new("store_io_error", "failed to clear old meta"))?;

        let meta = StoreMeta {
            register_id: reg.to_string(),
            device_epoch,
            last_seq: 0,
            chain_head: "GENESIS".to_string(),
            confirmed_seq: 0,
        };
        write_sealed_meta(&conn, &aead_key, &meta)?;

        let enrol_addr = Address::offline(&self.profile_identifier, OfflineSlot::Enrolment)
            .map_err(|_| OfflineError::new("bad_profile", "invalid profile identifier"))?;
        let mut enrol_json = serde_json::to_string(&enrolment)
            .map_err(|_| OfflineError::new("serialize_failed", "failed to encode enrolment"))?;
        let set_res = self
            .custody
            .set(&enrol_addr, &enrol_json)
            .map_err(|_| OfflineError::keychain_unavailable());
        enrol_json.zeroize();
        set_res?;
        self.write_mark(&mark)?;

        Ok(EnrolmentPublic {
            register_id: reg.to_string(),
            device_epoch,
            public_key_hex,
            gable_url: enrolment.gable_url.clone(),
            last_seq: 0,
            confirmed_seq: 0,
            chain_head: "GENESIS".to_string(),
        })
    }

    /// The mark of the enrolment a new one would replace, or a refusal when
    /// replacing it would lose a sale: an epoch not above the current one,
    /// or a queue holding a sale the server has not confirmed or settled.
    fn replaced_epoch(&self, device_epoch: i64) -> Result<Option<PriorEpochMark>, OfflineError> {
        let addr = Address::offline(&self.profile_identifier, OfflineSlot::Enrolment)
            .map_err(|_| OfflineError::new("bad_profile", "invalid profile identifier"))?;
        if self
            .custody
            .get(&addr)
            .map_err(|_| OfflineError::keychain_unavailable())?
            .is_none()
        {
            return Ok(None);
        }
        let current = self.read_enrolment()?;
        if device_epoch <= current.device_epoch {
            return Err(OfflineError::new(
                "epoch_not_newer",
                format!(
                    "a new enrolment needs an epoch above the current one ({})",
                    current.device_epoch
                ),
            ));
        }
        let not_drained = || {
            OfflineError::new(
                "queue_not_drained",
                "This register holds offline sales the server has not settled: sync them before it is enrolled again.",
            )
        };
        // A store that still opens is read for unsettled sales; a missing or
        // rolled back one leaves the keychain's mark as the only record.
        let mark = match self.open_verified_store() {
            Ok((conn, enrolment, mark, aead_key)) => {
                if read_all_sales(&conn, &aead_key, &enrolment)?
                    .iter()
                    .any(|e| e.state.is_unsettled())
                {
                    return Err(not_drained());
                }
                mark
            }
            Err(_) => self.read_mark()?,
        };
        if mark.last_seq > mark.confirmed_seq {
            return Err(not_drained());
        }
        Ok(Some(PriorEpochMark {
            device_epoch: current.device_epoch,
            last_seq: mark.last_seq,
            confirmed_seq: mark.confirmed_seq,
        }))
    }

    /// Reads the current enrolment and verifies the SQLite store against the
    /// keychain high-water mark, recovering any sale interrupted between store
    /// write and receipt print.
    pub fn enrolment_status(
        &self,
        caller_origin: Option<&str>,
    ) -> Result<EnrolmentPublic, OfflineError> {
        self.check_origin(caller_origin)?;
        let _ops = self.lock_ops();
        let (_, enrolment, mark, _) = self.open_verified_store()?;
        Ok(EnrolmentPublic {
            register_id: enrolment.register_id.clone(),
            device_epoch: enrolment.device_epoch,
            public_key_hex: enrolment.public_key_hex.clone(),
            gable_url: enrolment.gable_url.clone(),
            last_seq: mark.last_seq,
            confirmed_seq: mark.confirmed_seq,
            chain_head: mark.chain_head,
        })
    }

    fn open_verified_store(
        &self,
    ) -> Result<(Connection, EnrolmentRecord, HighWaterMark, ZeroizingKey), OfflineError> {
        let enrolment = self.read_enrolment()?;
        let mut mark = self.read_mark()?;
        let aead_key = self.read_aead_key()?;
        if mark.register_id != enrolment.register_id || mark.device_epoch != enrolment.device_epoch
        {
            return Err(OfflineError::re_enrol_required());
        }
        if !self.store_path.exists() {
            return Err(OfflineError::re_enrol_required());
        }
        let conn =
            Connection::open(&self.store_path).map_err(|_| OfflineError::re_enrol_required())?;
        conn.busy_timeout(Duration::from_secs(5))
            .map_err(|_| OfflineError::re_enrol_required())?;
        let meta = read_sealed_meta(
            &conn,
            &aead_key,
            &enrolment.register_id,
            enrolment.device_epoch,
        )?;
        if meta.register_id != enrolment.register_id || meta.device_epoch != enrolment.device_epoch
        {
            return Err(OfflineError::re_enrol_required());
        }

        // Crash recovery: if a crash happened after the SQLite transaction
        // wrote a `signing` row at `meta.last_seq` (where `meta.last_seq` is
        // either `mark.last_seq + 1` before the keychain update, or
        // `mark.last_seq` after the keychain update but before Ed25519 signing
        // finished), recover that sale in place so its sequence number is
        // never repeated.
        if meta.last_seq > 0
            && (meta.last_seq == mark.last_seq || meta.last_seq == mark.last_seq + 1)
        {
            if let Some(mut entry) = read_sealed_sale_opt(
                &conn,
                &aead_key,
                &enrolment.register_id,
                enrolment.device_epoch,
                meta.last_seq,
            )? {
                if entry.state == QueueItemState::Signing {
                    let canonical = canonical_sale_bytes(&entry.sale)?;
                    let digest_hex = sha256_hex(&canonical);
                    if digest_hex != meta.chain_head {
                        return Err(OfflineError::re_enrol_required());
                    }
                    if mark.last_seq < meta.last_seq {
                        mark.last_seq = meta.last_seq;
                        mark.chain_head = meta.chain_head.clone();
                        self.write_mark(&mark)?;
                    }
                    let sig = sign_canonical_bytes(&enrolment.signing_seed_hex, &canonical)?;
                    entry.sale.device_sig = sig;
                    entry.state = QueueItemState::Waiting;
                    update_sealed_sale(
                        &conn,
                        &aead_key,
                        &enrolment.register_id,
                        enrolment.device_epoch,
                        &entry,
                    )?;
                }
            }
        }

        if meta.last_seq < mark.confirmed_seq || meta.confirmed_seq < mark.confirmed_seq {
            return Err(OfflineError::re_enrol_required());
        }
        if meta.last_seq < mark.last_seq || meta.chain_head != mark.chain_head {
            return Err(OfflineError::store_rolled_back());
        }

        Ok((conn, enrolment, mark, aead_key))
    }

    /// Test seam: stores a register snapshot directly. The shipped shell's
    /// only writer of the snapshot is `refresh_snapshot`, which takes it from
    /// the enrolled server's own answer; the page never supplies one.
    #[cfg(test)]
    pub(crate) fn install_test_snapshot(
        &self,
        snapshot: OfflineSnapshot,
    ) -> Result<(), OfflineError> {
        let _ops = self.lock_ops();
        let (conn, enrolment, _, aead_key) = self.open_verified_store()?;
        store_snapshot(&conn, &aead_key, &enrolment, &snapshot)
    }

    /// Fetches the register's snapshot from the enrolled server and keeps it
    /// sealed beside the queue (ADR 0011 5.4). The core asks only once every
    /// queued sale has been answered (6.2: the device drains before it
    /// refreshes) and names its own high sequence; the copy must be the
    /// register's, of its epoch, and issued since its till session opened.
    pub fn refresh_snapshot(
        &self,
        caller_origin: Option<&str>,
        server: &dyn OfflineServer,
    ) -> Result<SnapshotRefresh, OfflineError> {
        self.check_origin(caller_origin)?;
        let (register, high_seq) = {
            let _ops = self.lock_ops();
            let (conn, enrolment, mark, aead_key) = self.open_verified_store()?;
            let sales = read_all_sales(&conn, &aead_key, &enrolment)?;
            if let Some(waiting) = sales.iter().find(|e| awaits_an_answer(e.state)) {
                return Err(OfflineError::new(
                    "queue_not_drained",
                    format!(
                        "Sale {} has not been answered by the server yet: the register sends it before it takes new prices, and keeps selling on the prices it holds.",
                        waiting.sale.offline_ref
                    ),
                ));
            }
            (enrolment.register_id.clone(), mark.last_seq)
        };
        let answer = server.get_snapshot(&register, high_seq)?;
        if answer.status != 200 {
            return Err(refusal_of(&answer));
        }
        let snapshot: OfflineSnapshot = serde_json::from_str(&answer.body).map_err(|_| {
            OfflineError::new(
                "snapshot_unreadable",
                "the server's snapshot could not be read by this register",
            )
        })?;
        let _ops = self.lock_ops();
        let (conn, enrolment, _, aead_key) = self.open_verified_store()?;
        let previous = read_raw_snapshot(&conn, &aead_key, &enrolment)
            .ok()
            .map(|s| s.snapshot_id);
        store_snapshot(&conn, &aead_key, &enrolment, &snapshot)?;
        Ok(SnapshotRefresh {
            status: if previous.as_deref() == Some(snapshot.snapshot_id.as_str()) {
                "unchanged".to_string()
            } else {
                "updated".to_string()
            },
            snapshot_id: snapshot.snapshot_id,
            issued_at: snapshot.issued_at,
        })
    }

    /// Prices the clerk's cart from the core's own snapshot copy through the
    /// sign path's own code (ADR 0011 5.3): the lines, the tax, the totals,
    /// and, when tenders are named, the change or why they would be refused,
    /// with the account's room and the queue's room. Nothing is written.
    pub fn price_cart(
        &self,
        caller_origin: Option<&str>,
        req: SignSaleRequest,
    ) -> Result<CartPreview, OfflineError> {
        self.check_origin(caller_origin)?;
        let _ops = self.lock_ops();
        let (conn, enrolment, _, aead_key) = self.open_verified_store()?;
        let snapshot = read_raw_snapshot(&conn, &aead_key, &enrolment)?;
        if !snapshot.policy.enabled {
            return Err(OfflineError::new(
                "offline_disabled",
                "offline selling is disabled for this branch",
            ));
        }
        if snapshot.issued_at < snapshot.till_session.opened_at {
            return Err(OfflineError::new(
                "snapshot_stale",
                "the register snapshot predates the open till session",
            ));
        }
        check_declaration(&req)?;
        let priced = price_cart_lines(&snapshot, &req)?;
        let existing = read_all_sales(&conn, &aead_key, &enrolment)?;
        let (waiting_count, waiting_total) = waiting_figures(&existing);
        let account_room_cents = priced
            .account
            .map(|acc| account_room(&snapshot, acc, &existing).max(0));
        let (change_cents, tender_refusal) = if req.tenders.is_empty() {
            (None, None)
        } else {
            match check_tenders(&snapshot, &req, &priced, &existing) {
                Ok(stage) => (Some(stage.change_cents), None),
                Err(e) => (None, Some(e)),
            }
        };
        Ok(CartPreview {
            snapshot_id: snapshot.snapshot_id.clone(),
            currency: priced.currency.clone(),
            tax_rate: priced.tax_rate.clone(),
            tax_exempt: priced.tax_exempt,
            lines: priced.lines.clone(),
            subtotal_cents: priced.subtotal_cents,
            tax_cents: priced.tax_cents,
            tax_lines: priced.tax_lines.clone(),
            total_cents: priced.total_cents,
            change_cents,
            tender_refusal,
            account_room_cents,
            queue_room_count: (snapshot.policy.max_queue_count - waiting_count).max(0),
            queue_room_cents: (snapshot.policy.max_queue_total_cents - waiting_total).max(0),
        })
    }

    /// Sends every sale the server has not settled to the enrolled server's
    /// sync, with the device's own mark, and records the server's answer
    /// (ADR 0011 sections 3 and 6.2). Items with a manager are sent again
    /// each time, so the register learns when one is resolved. The store's
    /// lock is not held while the request runs, so the clerk keeps selling.
    pub fn sync_queue(
        &self,
        caller_origin: Option<&str>,
        server: &dyn OfflineServer,
    ) -> Result<SyncReport, OfflineError> {
        self.check_origin(caller_origin)?;
        let (register, epoch, last_seq, items) = {
            let _ops = self.lock_ops();
            let (conn, enrolment, mark, aead_key) = self.open_verified_store()?;
            let mut items: Vec<SignedOfflineSale> = read_all_sales(&conn, &aead_key, &enrolment)?
                .into_iter()
                .filter(|e| e.state.is_unsettled() && e.state != QueueItemState::Signing)
                .map(|e| e.sale)
                .collect();
            items.sort_by_key(|s| s.device_seq);
            (
                enrolment.register_id.clone(),
                enrolment.device_epoch,
                mark.last_seq,
                items,
            )
        };
        if items.is_empty() {
            let queue = self.queue_status(caller_origin)?;
            return Ok(SyncReport {
                sent: 0,
                posted: 0,
                refused: 0,
                held: 0,
                resolved: 0,
                errors: 0,
                confirmed_seq: queue.confirmed_seq,
                open_exception_count: queue.review_count,
                queue,
            });
        }
        let batch_id = uuid::Uuid::new_v4().to_string();
        let body = serde_json::json!({
            "batch_id": batch_id,
            "register_id": register,
            "device_epoch": epoch,
            "reported_seq": last_seq,
            "items": items,
        })
        .to_string();
        let answer = server.post_sync(&body, &batch_id)?;
        if answer.status != 200 {
            return Err(refusal_of(&answer));
        }
        let wire: WireSyncResult = serde_json::from_str(&answer.body).map_err(|_| {
            OfflineError::new(
                "sync_unreadable",
                "the server's sync answer could not be read by this register",
            )
        })?;
        let mut report = SyncReport {
            sent: items.len() as i64,
            posted: 0,
            refused: 0,
            held: 0,
            resolved: 0,
            errors: 0,
            confirmed_seq: wire.confirmed_seq,
            open_exception_count: wire.open_exception_count,
            queue: QueueStatusView {
                register_id: String::new(),
                device_epoch: 0,
                last_seq: 0,
                confirmed_seq: 0,
                chain_head: String::new(),
                waiting_count: 0,
                waiting_total_cents: 0,
                pending_count: 0,
                review_count: 0,
                synced_count: 0,
                items: vec![],
            },
        };
        let mut outcomes = Vec::with_capacity(wire.items.len());
        for item in wire.items {
            if item.device_epoch != epoch {
                continue;
            }
            let status = match item.outcome.as_str() {
                "posted" | "duplicate" => {
                    report.posted += 1;
                    QueueItemState::Synced
                }
                "resolved" => {
                    report.resolved += 1;
                    QueueItemState::Synced
                }
                "refused" => {
                    report.refused += 1;
                    QueueItemState::Review
                }
                "held" => {
                    report.held += 1;
                    QueueItemState::Review
                }
                _ => {
                    report.errors += 1;
                    QueueItemState::Pending
                }
            };
            outcomes.push(SyncOutcomeItem {
                client_id: item.client_id,
                device_seq: item.device_seq,
                status,
                invoice_id: item.invoice_id,
                invoice_number: item.invoice_number,
                reason_code: item.reason_code,
                reason_message: item.reason_message,
            });
        }
        let _ops = self.lock_ops();
        report.queue = self.record_outcomes_held(SyncBatchOutcome {
            confirmed_seq: wire.confirmed_seq,
            items: outcomes,
        })?;
        Ok(report)
    }

    /// Loads the core's snapshot copy with each product's `in_stock` figure
    /// reduced by the `stock_quantity` of every queued sale line for that
    /// product that the snapshot's figure does not already hold: every sale
    /// sold under this snapshot whatever its state, and every unsettled one
    /// (ADR 0011 sections 5.1 and 6.2).
    pub fn load_snapshot(
        &self,
        caller_origin: Option<&str>,
    ) -> Result<OfflineSnapshot, OfflineError> {
        self.check_origin(caller_origin)?;
        let _ops = self.lock_ops();
        let (conn, enrolment, _, aead_key) = self.open_verified_store()?;
        let mut snapshot = read_raw_snapshot(&conn, &aead_key, &enrolment)?;
        let sales = read_all_sales(&conn, &aead_key, &enrolment)?;
        for product in &mut snapshot.products {
            let mut sold_stock: i64 = 0;
            for entry in &sales {
                if !counts_against_snapshot(entry, &snapshot.snapshot_id) {
                    continue;
                }
                for line in &entry.sale.lines {
                    if line.product_id == product.id {
                        if let Ok(sq) = parse_quantity(&line.stock_quantity) {
                            sold_stock = sold_stock.saturating_add(sq);
                        }
                    }
                }
            }
            if sold_stock > 0 {
                if let Ok(initial) = parse_quantity(&product.in_stock) {
                    product.in_stock = format_wire_quantity(initial - sold_stock);
                }
            }
        }
        Ok(snapshot)
    }

    /// Derives prices, units, stock conversion, tally, tax, change, sequence,
    /// and chain link from the core's own snapshot copy, writes the sealed
    /// sale to SQLite first, updates the keychain's high-water mark second,
    /// signs the canonical JSON payload with Ed25519 third, and returns the
    /// signed sale (ADR 0011 sections 4.1, 5.1, and 7).
    pub fn sign_and_queue_sale(
        &self,
        caller_origin: Option<&str>,
        req: SignSaleRequest,
    ) -> Result<SignedOfflineSale, OfflineError> {
        self.check_origin(caller_origin)?;
        if req.device_seq.is_some() {
            return Err(OfflineError::new(
                "caller_named_sequence",
                "the core assigns device_seq; a request must not name its own sequence",
            ));
        }
        if req.prev_digest.is_some() {
            return Err(OfflineError::new(
                "caller_named_chain_link",
                "the core assigns prev_digest; a request must not name its own chain link",
            ));
        }
        let _ops = self.lock_ops();
        let (mut conn, enrolment, mut mark, aead_key) = self.open_verified_store()?;
        let snapshot = read_raw_snapshot(&conn, &aead_key, &enrolment)?;

        if !snapshot.policy.enabled {
            return Err(OfflineError::new(
                "offline_disabled",
                "offline selling is disabled for this branch",
            ));
        }
        if snapshot.issued_at < snapshot.till_session.opened_at {
            return Err(OfflineError::new(
                "snapshot_stale",
                "the register snapshot predates the open till session",
            ));
        }
        if req.snapshot_id.trim() != snapshot.snapshot_id {
            return Err(OfflineError::new(
                "snapshot_unknown",
                "request snapshot_id does not match the core's snapshot copy",
            ));
        }
        if req.till_session_id.trim() != snapshot.till_session.id {
            return Err(OfflineError::new(
                "session_mismatch",
                "request till_session_id does not match the snapshot's open till session",
            ));
        }
        if req.business_date.trim() != snapshot.till_session.business_date {
            return Err(OfflineError::new(
                "snapshot_stale",
                "sale business_date does not match the till session's business_date",
            ));
        }
        let priced = price_cart_lines(&snapshot, &req)?;
        let existing_sales = read_all_sales(&conn, &aead_key, &enrolment)?;
        let stage = check_tenders(&snapshot, &req, &priced, &existing_sales)?;
        check_declaration(&req)?;

        let device_seq = mark.last_seq + 1;
        let prev_digest = mark.chain_head.clone();
        let offline_ref = format!(
            "{}-{}-{:06}",
            enrolment.register_id, enrolment.device_epoch, device_seq
        );

        let mut sale = SignedOfflineSale {
            client_id: req.client_id,
            register_id: enrolment.register_id.clone(),
            device_epoch: enrolment.device_epoch,
            device_seq,
            prev_digest,
            offline_ref,
            snapshot_id: snapshot.snapshot_id.clone(),
            till_session_id: snapshot.till_session.id.clone(),
            cashier_id: req.cashier_id,
            customer_id: req.customer_id,
            buyer_contact_id: req.buyer_contact_id,
            customer_po: req.customer_po,
            job_id: req.job_id,
            picked_up_by: req.picked_up_by,
            business_date: req.business_date,
            sold_at: req.sold_at,
            currency: priced.currency.clone(),
            subtotal_cents: priced.subtotal_cents,
            tax_cents: priced.tax_cents,
            total_cents: priced.total_cents,
            change_cents: stage.change_cents,
            tax_rate: priced.tax_rate.clone(),
            tax_exempt: priced.tax_exempt,
            tax_lines: priced.tax_lines.clone(),
            queue_count: stage.queue_count,
            queue_total_cents: stage.queue_total_cents,
            confirmed_mark: mark.confirmed_seq,
            lines: priced.lines.clone(),
            tenders: stage.tenders,
            replaces_sale_id: req.replaces_sale_id,
            different_customer: req.different_customer,
            unknown_cart_id: req.unknown_cart_id,
            device_sig: String::new(),
        };

        let canonical_bytes = canonical_sale_bytes(&sale)?;
        let next_chain_head = sha256_hex(&canonical_bytes);

        let mut entry = QueuedSaleEntry {
            device_seq,
            state: QueueItemState::Signing,
            reason_code: None,
            reason_message: None,
            invoice_id: None,
            invoice_number: None,
            sale: sale.clone(),
        };
        let next_meta = StoreMeta {
            register_id: enrolment.register_id.clone(),
            device_epoch: enrolment.device_epoch,
            last_seq: device_seq,
            chain_head: next_chain_head.clone(),
            confirmed_seq: mark.confirmed_seq,
        };

        // Step 1: Write the sealed sale row and updated store_meta to SQLite FIRST.
        {
            // BEGIN IMMEDIATE takes SQLite's write lock before the meta is
            // read, so another handle on the file cannot sign between the
            // read and the write; the plain insert fails on a taken sequence
            // instead of overwriting the sale that holds it.
            let tx = conn
                .transaction_with_behavior(TransactionBehavior::Immediate)
                .map_err(|_| OfflineError::new("store_io_error", "failed to open SQLite tx"))?;
            let current = read_sealed_meta(
                &tx,
                &aead_key,
                &enrolment.register_id,
                enrolment.device_epoch,
            )?;
            if current.last_seq != mark.last_seq || current.chain_head != mark.chain_head {
                return Err(OfflineError::new(
                    "store_busy",
                    "another sale was written to the queue at the same moment: ring this sale again",
                ));
            }
            insert_sealed_sale(
                &tx,
                &aead_key,
                &enrolment.register_id,
                enrolment.device_epoch,
                &entry,
            )?;
            write_sealed_meta(&tx, &aead_key, &next_meta)?;
            tx.commit()
                .map_err(|_| OfflineError::new("store_io_error", "failed to commit SQLite tx"))?;
        }

        let crash = *self.crash_point.lock().expect("offline crash lock");
        if crash == Some(CrashPoint::AfterStoreWriteBeforeKeychainMark) {
            return Err(OfflineError::new(
                "simulated_crash",
                "simulated crash after SQLite write before keychain mark update",
            ));
        }

        // Step 2: Update the keychain's high-water mark SECOND.
        mark.last_seq = device_seq;
        mark.chain_head = next_chain_head;
        self.write_mark(&mark)?;

        if crash == Some(CrashPoint::AfterKeychainMarkBeforeSign) {
            return Err(OfflineError::new(
                "simulated_crash",
                "simulated crash after keychain mark update before Ed25519 sign",
            ));
        }

        // Step 3: Sign the canonical payload with the keychain's Ed25519 key
        // and finalize the sealed row in SQLite.
        let sig = sign_canonical_bytes(&enrolment.signing_seed_hex, &canonical_bytes)?;
        sale.device_sig = sig;
        entry.sale = sale.clone();
        entry.state = QueueItemState::Waiting;
        update_sealed_sale(
            &conn,
            &aead_key,
            &enrolment.register_id,
            enrolment.device_epoch,
            &entry,
        )?;

        if crash == Some(CrashPoint::AfterSignBeforePrint) {
            return Err(OfflineError::new(
                "simulated_crash",
                "simulated crash after signing before receipt print",
            ));
        }

        Ok(sale)
    }

    /// Lists all queued sales and summary counts from the AEAD-sealed SQLite
    /// store.
    pub fn queue_status(
        &self,
        caller_origin: Option<&str>,
    ) -> Result<QueueStatusView, OfflineError> {
        self.check_origin(caller_origin)?;
        let _ops = self.lock_ops();
        self.queue_status_held()
    }

    fn queue_status_held(&self) -> Result<QueueStatusView, OfflineError> {
        let (conn, enrolment, mark, aead_key) = self.open_verified_store()?;
        let items = read_all_sales(&conn, &aead_key, &enrolment)?;
        let mut waiting_count = 0i64;
        let mut waiting_total_cents = 0i64;
        let mut pending_count = 0i64;
        let mut review_count = 0i64;
        let mut synced_count = 0i64;
        for item in &items {
            if item.state.is_unsettled() {
                waiting_count += 1;
                waiting_total_cents += item.sale.total_cents;
            }
            match item.state {
                QueueItemState::Signing | QueueItemState::Waiting => {}
                QueueItemState::Pending => pending_count += 1,
                QueueItemState::Review => review_count += 1,
                QueueItemState::Synced => synced_count += 1,
            }
        }
        Ok(QueueStatusView {
            register_id: enrolment.register_id.clone(),
            device_epoch: enrolment.device_epoch,
            last_seq: mark.last_seq,
            confirmed_seq: mark.confirmed_seq,
            chain_head: mark.chain_head,
            waiting_count,
            waiting_total_cents,
            pending_count,
            review_count,
            synced_count,
            items,
        })
    }

    /// Applies the server's sync batch response to the local queue and advances
    /// `confirmed_seq` in both the SQLite store and the keychain mark. Not a
    /// webview command: a page that wrote the outcomes could free the caps and
    /// the room, so the core takes them only from the server's own answer.
    pub fn record_sync_outcomes(
        &self,
        caller_origin: Option<&str>,
        outcome: SyncBatchOutcome,
    ) -> Result<QueueStatusView, OfflineError> {
        self.check_origin(caller_origin)?;
        let _ops = self.lock_ops();
        self.record_outcomes_held(outcome)
    }

    fn record_outcomes_held(
        &self,
        outcome: SyncBatchOutcome,
    ) -> Result<QueueStatusView, OfflineError> {
        let (mut conn, enrolment, mut mark, aead_key) = self.open_verified_store()?;
        let mut meta = read_sealed_meta(
            &conn,
            &aead_key,
            &enrolment.register_id,
            enrolment.device_epoch,
        )?;
        if outcome.confirmed_seq > mark.last_seq {
            return Err(OfflineError::new(
                "invalid_confirmed_seq",
                "server confirmed_seq exceeds the device's last_seq",
            ));
        }
        {
            let tx = conn
                .transaction_with_behavior(TransactionBehavior::Immediate)
                .map_err(|_| OfflineError::new("store_io_error", "failed to open SQLite tx"))?;
            for item in &outcome.items {
                if let Some(mut entry) = read_sealed_sale_opt(
                    &tx,
                    &aead_key,
                    &enrolment.register_id,
                    enrolment.device_epoch,
                    item.device_seq,
                )? {
                    if entry.sale.client_id == item.client_id {
                        entry.state = item.status;
                        entry.invoice_id = item.invoice_id.clone();
                        entry.invoice_number = item.invoice_number.clone();
                        entry.reason_code = item.reason_code.clone();
                        entry.reason_message = item.reason_message.clone();
                        update_sealed_sale(
                            &tx,
                            &aead_key,
                            &enrolment.register_id,
                            enrolment.device_epoch,
                            &entry,
                        )?;
                    }
                }
            }
            if outcome.confirmed_seq > meta.confirmed_seq {
                meta.confirmed_seq = outcome.confirmed_seq;
                write_sealed_meta(&tx, &aead_key, &meta)?;
            }
            tx.commit()
                .map_err(|_| OfflineError::new("store_io_error", "failed to commit SQLite tx"))?;
        }
        if outcome.confirmed_seq > mark.confirmed_seq {
            mark.confirmed_seq = outcome.confirmed_seq;
            self.write_mark(&mark)?;
        }
        self.queue_status_held()
    }
}

/// A cart priced from the core's snapshot copy.
struct PricedCart<'a> {
    account: Option<&'a crate::offline::model::SnapshotAccount>,
    currency: String,
    tax_exempt: bool,
    tax_rate: String,
    lines: Vec<SignedSaleLine>,
    subtotal_cents: i64,
    tax_cents: i64,
    tax_lines: Vec<SignedTaxLine>,
    total_cents: i64,
}

/// What the tender check answers for a priced cart.
struct TenderStage {
    change_cents: i64,
    tenders: Vec<SignedSaleTender>,
    queue_count: i64,
    queue_total_cents: i64,
}

/// The replaced cart and the "different customer" declaration (ADR 0011
/// 5.3): a declared sale names the result unknown cart it is not, and then
/// replaces nothing; an undeclared sale names no unknown cart.
fn check_declaration(req: &SignSaleRequest) -> Result<(), OfflineError> {
    let named = |v: &Option<String>| v.as_deref().map(str::trim).is_some_and(|s| !s.is_empty());
    if req.different_customer {
        if !named(&req.unknown_cart_id) || named(&req.replaces_sale_id) {
            return Err(OfflineError::new(
                "declaration_invalid",
                "a sale declared for a different customer names the unknown cart and replaces nothing",
            ));
        }
    } else if named(&req.unknown_cart_id) {
        return Err(OfflineError::new(
            "declaration_invalid",
            "only a sale declared for a different customer names the unknown cart",
        ));
    }
    for id in [&req.replaces_sale_id, &req.unknown_cart_id]
        .into_iter()
        .flatten()
    {
        if uuid::Uuid::parse_str(id.trim()).is_err() {
            return Err(OfflineError::new(
                "declaration_invalid",
                "the replaced or unknown cart must be a sale id",
            ));
        }
    }
    Ok(())
}

/// Prices a cart from the core's own snapshot copy: each line's bound price
/// row, its unit pair, its stock figures, its extension and taxable flag, and
/// the sale's subtotal, tax and total (ADR 0011 sections 5.3 and 5.4). Any
/// figure the request asserts must equal what the core derives. Nothing is
/// written: the preview and the sign path both price through here.
fn price_cart_lines<'a>(
    snapshot: &'a OfflineSnapshot,
    req: &SignSaleRequest,
) -> Result<PricedCart<'a>, OfflineError> {
    if req.lines.is_empty() {
        return Err(OfflineError::new(
            "empty_lines",
            "an offline sale requires at least one product line",
        ));
    }

    let account = match req.customer_id.as_deref() {
        None => None,
        Some(cid) => {
            if cid == snapshot.walk_in_customer.id {
                return Err(OfflineError::new(
                    "customer_mismatch",
                    "walk-in sales omit customer_id instead of naming the walk-in row",
                ));
            }
            let found = snapshot
                .accounts
                .iter()
                .find(|a| a.customer_id == cid)
                .ok_or_else(|| {
                    OfflineError::new(
                        "customer_not_in_snapshot",
                        "the named customer is not in the register's snapshot",
                    )
                })?;
            Some(found)
        }
    };

    let currency = match account {
        Some(a) => a.currency.clone(),
        None => snapshot.walk_in_customer.currency.clone(),
    };
    let tax_exempt = account.is_some_and(|a| a.tax_exempt);
    let derived_tax_rate = if tax_exempt {
        "0.000000".to_string()
    } else {
        snapshot.tax.branch_rate.clone()
    };
    let rate_scaled =
        parse_tax_rate(&derived_tax_rate).map_err(|m| OfflineError::new("invalid_tax_rate", m))?;

    let mut signed_lines = Vec::with_capacity(req.lines.len());
    let mut subtotal_cents: i64 = 0;
    let mut taxable_cents: i64 = 0;

    for (idx, line_req) in req.lines.iter().enumerate() {
        if line_req
            .line_type
            .as_deref()
            .is_some_and(|t| t != "product")
            || line_req.charge_code.is_some()
        {
            return Err(OfflineError::new(
                "charge_not_offline",
                "only stocked product lines sell offline: charge lines require the live counter",
            ));
        }
        if line_req.override_reason.is_some() {
            return Err(OfflineError::new(
                "override_not_offline",
                "price overrides are not available offline",
            ));
        }
        if line_req.discount_percent.is_some() || line_req.discount_cents.is_some() {
            return Err(OfflineError::new(
                "discount_not_offline",
                "line discounts are not available offline",
            ));
        }

        let product = snapshot
            .products
            .iter()
            .find(|p| p.id == line_req.product_id)
            .ok_or_else(|| {
                OfflineError::new(
                    "product_not_in_snapshot",
                    format!(
                        "product {} is not in the register snapshot",
                        line_req.product_id
                    ),
                )
            })?;

        if product.is_kit {
            return Err(OfflineError::new(
                "kit_not_offline",
                "Kits do not sell offline: remove the kit line or wait until the counter is online.",
            ));
        }

        let sale_uom = line_req.uom.trim();
        let u_row = product
            .unit_set
            .iter()
            .find(|r| r.uom == sale_uom && r.sell)
            .ok_or_else(|| {
                OfflineError::new(
                    "invalid_sale_uom",
                    format!("{sale_uom} is not a sale unit of product {}", product.sku),
                )
            })?;
        let u_unit_qty = parse_quantity(&u_row.unit_qty)
            .map_err(|m| OfflineError::new("invalid_unit_set", m))?;
        let u_stock_qty = parse_quantity(&u_row.stock_qty)
            .map_err(|m| OfflineError::new("invalid_unit_set", m))?;

        let (qty_scaled, signed_tally) = if let Some(tally_in) = &line_req.tally {
            if !product.random_length {
                return Err(OfflineError::new(
                    "tally_not_random_length",
                    "a tally is allowed on a random length product line only",
                ));
            }
            if sale_uom != "LF" {
                return Err(OfflineError::new(
                    "tally_uom_must_be_lf",
                    "a tallied line's unit must be LF",
                ));
            }
            let mut calc_rows = Vec::with_capacity(tally_in.rows.len());
            let mut wire_rows = Vec::with_capacity(tally_in.rows.len());
            for r in &tally_in.rows {
                let len_scaled = parse_quantity(&r.length_ft)
                    .map_err(|m| OfflineError::new("invalid_tally_length", m))?;
                calc_rows.push(TallyRowCalc {
                    pieces: r.pieces,
                    length_ft: len_scaled,
                });
                wire_rows.push(SignedTallyRow {
                    pieces: r.pieces,
                    length_ft: format_wire_quantity(len_scaled),
                });
            }
            let thick_scaled = match &product.board_thickness_in {
                Some(s) => Some(
                    parse_quantity(s).map_err(|m| OfflineError::new("invalid_cross_section", m))?,
                ),
                None => None,
            };
            let width_scaled = match &product.board_width_in {
                Some(s) => Some(
                    parse_quantity(s).map_err(|m| OfflineError::new("invalid_cross_section", m))?,
                ),
                None => None,
            };
            let completed = complete_tally(&calc_rows, thick_scaled, width_scaled)
                .map_err(|m| OfflineError::new("invalid_tally", m))?;
            if let Some(sent_q) = &line_req.quantity {
                let parsed_sent =
                    parse_quantity(sent_q).map_err(|m| OfflineError::new("invalid_quantity", m))?;
                if parsed_sent != completed.linear_feet {
                    return Err(OfflineError::new(
                        "tally_quantity_mismatch",
                        "line quantity must equal the tally's linear feet",
                    ));
                }
            }
            (
                completed.linear_feet,
                Some(SignedTally {
                    thickness_in: thick_scaled.map(format_wire_quantity),
                    width_in: width_scaled.map(format_wire_quantity),
                    rows: wire_rows,
                    linear_feet: format_wire_quantity(completed.linear_feet),
                    board_feet: completed.board_feet.map(format_wire_quantity),
                }),
            )
        } else {
            let q_str = line_req.quantity.as_deref().ok_or_else(|| {
                OfflineError::new("missing_quantity", "line quantity is required")
            })?;
            let q = parse_quantity(q_str).map_err(|m| OfflineError::new("invalid_quantity", m))?;
            if q <= 0 {
                return Err(OfflineError::new(
                    "invalid_quantity",
                    "line quantity must be positive",
                ));
            }
            (q, None)
        };

        let (price_row, level_fallback) = resolve_bound_price_row(&snapshot, product, account)?;
        let (uom_qty_scaled, price_uom_qty_scaled) =
            resolve_pair_for_line(product, sale_uom, u_unit_qty, u_stock_qty, price_row)?;

        let stock_qty_scaled = convert_stock(
            qty_scaled,
            sale_uom,
            &product.stock_uom,
            u_unit_qty,
            u_stock_qty,
        )
        .map_err(|inexact| OfflineError::new("inexact_stock_conversion", inexact.message()))?;

        let line_total_cents = extend(
            qty_scaled,
            uom_qty_scaled,
            price_uom_qty_scaled,
            price_row.unit_price_ten_thousandths,
        )
        .map_err(|m| OfflineError::new("extension_failed", m))?;

        let uom_qty_wire = format_wire_quantity(uom_qty_scaled);
        let price_uom_qty_wire = format_wire_quantity(price_uom_qty_scaled);
        let stock_qty_wire = format_wire_quantity(stock_qty_scaled);

        // Verify any caller-supplied price, unit, stock, or tax fields
        // against what the core derived from its own snapshot copy.
        if let Some(sent_key) = &line_req.price_key {
            if sent_key != &price_row.price_key {
                return Err(OfflineError::new(
                    "price_key_mismatch",
                    "line price_key differs from the customer's bound price key in the core snapshot",
                ));
            }
        }
        if let Some(sent_puom) = &line_req.price_uom {
            if sent_puom != &price_row.price_uom {
                return Err(OfflineError::new(
                    "price_uom_mismatch",
                    "line price_uom differs from the core snapshot's price row",
                ));
            }
        }
        if let Some(sent_uq) = &line_req.uom_qty {
            if sent_uq != &uom_qty_wire {
                return Err(OfflineError::new(
                    "uom_qty_mismatch",
                    "line uom_qty differs from the core snapshot's canonical pair",
                ));
            }
        }
        if let Some(sent_pq) = &line_req.price_uom_qty {
            if sent_pq != &price_uom_qty_wire {
                return Err(OfflineError::new(
                    "price_uom_qty_mismatch",
                    "line price_uom_qty differs from the core snapshot's canonical pair",
                ));
            }
        }
        if let Some(sent_price) = line_req.unit_price_ten_thousandths {
            if sent_price != price_row.unit_price_ten_thousandths {
                return Err(OfflineError::new(
                    "price_mismatch",
                    "line unit_price_ten_thousandths differs from the core snapshot's price sheet",
                ));
            }
        }
        if let Some(sent_total) = line_req.line_total_cents {
            if sent_total != line_total_cents {
                return Err(OfflineError::new(
                    "line_total_mismatch",
                    "line_total_cents differs from the core's extension",
                ));
            }
        }
        if let Some(sent_taxable) = line_req.taxable {
            if sent_taxable != product.taxable {
                return Err(OfflineError::new(
                    "taxable_mismatch",
                    "line taxable flag differs from the core snapshot",
                ));
            }
        }
        if let Some(sent_suom) = &line_req.stock_uom {
            if sent_suom != &product.stock_uom {
                return Err(OfflineError::new(
                    "stock_uom_mismatch",
                    "line stock_uom differs from the core snapshot",
                ));
            }
        }
        if let Some(sent_sq) = &line_req.stock_quantity {
            if sent_sq != &stock_qty_wire {
                return Err(OfflineError::new(
                    "stock_quantity_mismatch",
                    "line stock_quantity differs from the core's exact stock conversion",
                ));
            }
        }

        subtotal_cents += line_total_cents;
        if product.taxable {
            taxable_cents += line_total_cents;
        }

        signed_lines.push(SignedSaleLine {
            id: line_req.id.clone(),
            position: idx,
            product_id: product.id.clone(),
            sku: product.sku.clone(),
            description: product.description.clone(),
            quantity: format_wire_quantity(qty_scaled),
            uom: sale_uom.to_string(),
            price_uom: price_row.price_uom.clone(),
            uom_qty: uom_qty_wire,
            price_uom_qty: price_uom_qty_wire,
            stock_uom: product.stock_uom.clone(),
            stock_quantity: stock_qty_wire,
            price_key: price_row.price_key.clone(),
            price_source: price_row.price_source.clone(),
            price_basis: price_row.price_basis.clone(),
            level_fallback,
            unit_price_ten_thousandths: price_row.unit_price_ten_thousandths,
            line_total_cents,
            taxable: product.taxable,
            tally: signed_tally,
        });
    }

    // A Canadian branch's parts are each priced and rounded on their own,
    // the tax their sum (CP-14 A1); any other branch keeps the one rounding.
    let mut tax_lines = Vec::new();
    let tax_cents = if tax_exempt {
        0
    } else if snapshot.tax.parts.is_empty() {
        tax_at(taxable_cents, rate_scaled)
    } else {
        let mut sum = 0;
        for part in &snapshot.tax.parts {
            let scaled =
                parse_tax_rate(&part.rate).map_err(|m| OfflineError::new("invalid_tax_rate", m))?;
            let cents = tax_at(taxable_cents, scaled);
            sum += cents;
            tax_lines.push(SignedTaxLine {
                name: part.name.clone(),
                rate: part.rate.clone(),
                label: part.label.clone(),
                cents,
            });
        }
        sum
    };
    let total_cents = subtotal_cents + tax_cents;

    if let Some(sent_sub) = req.subtotal_cents {
        if sent_sub != subtotal_cents {
            return Err(OfflineError::new(
                "subtotal_mismatch",
                "subtotal_cents differs from the core's derived subtotal",
            ));
        }
    }
    if let Some(sent_tax) = req.tax_cents {
        if sent_tax != tax_cents {
            return Err(OfflineError::new(
                "tax_figure_mismatch",
                "tax_cents differs from the core's derived tax figure",
            ));
        }
    }
    if let Some(sent_tot) = req.total_cents {
        if sent_tot != total_cents {
            return Err(OfflineError::new(
                "total_mismatch",
                "total_cents differs from the core's derived total",
            ));
        }
    }
    if let Some(sent_rate) = &req.tax_rate {
        if sent_rate != &derived_tax_rate {
            return Err(OfflineError::new(
                "tax_figure_mismatch",
                "tax_rate differs from the core snapshot's tax rate",
            ));
        }
    }
    if let Some(sent_exempt) = req.tax_exempt {
        if sent_exempt != tax_exempt {
            return Err(OfflineError::new(
                "tax_figure_mismatch",
                "tax_exempt differs from the customer's exemption in the core snapshot",
            ));
        }
    }

    Ok(PricedCart {
        account,
        currency,
        tax_exempt,
        tax_rate: derived_tax_rate,
        lines: signed_lines,
        tax_lines,
        subtotal_cents,
        tax_cents,
        total_cents,
    })
}

/// Checks a priced cart's tenders, caps, account room and queue figures
/// against the core's snapshot copy and the queue it holds (ADR 0011 sections
/// 1, 4 and 6.2), and answers the settlement and the queue figures the signed
/// sale carries.
fn check_tenders(
    snapshot: &OfflineSnapshot,
    req: &SignSaleRequest,
    priced: &PricedCart<'_>,
    existing_sales: &[QueuedSaleEntry],
) -> Result<TenderStage, OfflineError> {
    let account = priced.account;
    let total_cents = priced.total_cents;
    if total_cents > snapshot.policy.max_sale_cents {
        return Err(OfflineError::new(
            "sale_over_cap",
            "sale total exceeds the branch's per-sale offline cap",
        ));
    }

    let calc_tenders: Vec<OfflineTenderCalc> = req
        .tenders
        .iter()
        .map(|t| OfflineTenderCalc {
            method: t.method.clone(),
            amount_cents: t.amount_cents,
        })
        .collect();
    let settlement = settle_tenders(
        total_cents,
        &calc_tenders,
        snapshot.policy.cash_rounding_mode,
    )
    .map_err(|r| OfflineError::new(r.code, r.message))?;

    let mut signed_tenders = Vec::with_capacity(req.tenders.len());
    let mut biggest_check: i64 = 0;
    for t in &req.tenders {
        let method = t.method.trim().to_ascii_lowercase();
        if !snapshot
            .policy
            .allowed_tenders
            .iter()
            .any(|m| m.eq_ignore_ascii_case(&method))
        {
            return Err(OfflineError::new(
                "tender_not_offline",
                format!("tender method {method} is not allowed offline"),
            ));
        }
        if snapshot
            .policy
            .require_id_for_tenders
            .iter()
            .any(|m| m.eq_ignore_ascii_case(&method))
        {
            let valid_id = t.id_checked
                && t.id_type
                    .as_deref()
                    .is_some_and(|kind| VALID_ID_TYPES.contains(&kind));
            if !valid_id {
                return Err(OfflineError::new(
                    "id_required",
                    format!("a {method} tender requires checking a valid ID type"),
                ));
            }
        }
        if method == "check" && t.amount_cents > biggest_check {
            biggest_check = t.amount_cents;
        }
        signed_tenders.push(SignedSaleTender {
            method,
            amount_cents: t.amount_cents,
            reference: t.reference.clone(),
            id_checked: t.id_checked,
            id_type: t.id_type.clone(),
        });
    }

    if biggest_check > 0 {
        if biggest_check > snapshot.policy.max_check_cents {
            return Err(OfflineError::new(
                "check_over_cap",
                "check tender exceeds the branch's offline check cap",
            ));
        }
        let check_limit = account
            .and_then(|a| a.check_limit_cents)
            .or(snapshot.policy.default_check_limit_cents);
        if let Some(limit) = check_limit {
            if biggest_check > limit {
                return Err(OfflineError::new(
                    "check_over_limit",
                    "check tender exceeds the customer's check limit",
                ));
            }
        }
    }

    if settlement.account_cents > 0 {
        let acc = account.ok_or_else(|| {
            OfflineError::new(
                "walk_in_account",
                "an ACCOUNT tender needs a named customer: the walk-in customer buys on the barrelhead",
            )
        })?;
        if acc.credit_hold {
            return Err(OfflineError::new(
                "credit_hold",
                "customer account is on credit hold",
            ));
        }
        if acc.po_required
            && req
                .customer_po
                .as_deref()
                .map(str::trim)
                .unwrap_or("")
                .is_empty()
        {
            return Err(OfflineError::new(
                "po_required",
                "the customer requires a purchase order number",
            ));
        }
        if acc.buyer_roster_required && req.buyer_contact_id.is_none() {
            return Err(OfflineError::new(
                "buyer_required",
                "the customer requires an authorized buyer from its roster",
            ));
        }
        if let Some(buyer_id) = req.buyer_contact_id.as_deref() {
            let buyer = acc
                .buyers
                .iter()
                .find(|b| b.contact_id == buyer_id)
                .ok_or_else(|| {
                    OfflineError::new(
                        "buyer_not_on_roster",
                        "the selected buyer is not on the customer's roster",
                    )
                })?;
            if let Some(cap) = buyer.per_purchase_cap_cents {
                if settlement.account_cents > cap {
                    return Err(OfflineError::new(
                        "buyer_over_cap",
                        "account tender exceeds the buyer's per-purchase cap",
                    ));
                }
            }
        }
        let remaining_room = account_room(snapshot, acc, existing_sales);
        if settlement.account_cents > remaining_room {
            return Err(OfflineError::new(
                "account_over_offline_room",
                "account tender exceeds the customer's remaining offline credit room on this register",
            ));
        }
    }

    // Queue count and total caps count waiting (unsettled) items only;
    // synced items never consume the register's offline queue cap.
    let (waiting_count, waiting_total_cents) = waiting_figures(existing_sales);
    let next_queue_count = waiting_count + 1;
    let next_queue_total = waiting_total_cents + total_cents;
    if next_queue_count > snapshot.policy.max_queue_count {
        return Err(OfflineError::new(
            "queue_count_cap",
            "offline queue has reached the branch's maximum sale count; connect and sync before ringing another sale",
        ));
    }
    if next_queue_total > snapshot.policy.max_queue_total_cents {
        return Err(OfflineError::new(
            "queue_total_cap",
            "offline queue has reached the branch's maximum dollar total; connect and sync before ringing another sale",
        ));
    }

    Ok(TenderStage {
        change_cents: settlement.change_cents,
        tenders: signed_tenders,
        queue_count: next_queue_count,
        queue_total_cents: next_queue_total,
    })
}

/// Seals and stores the core's snapshot copy, after checking it is the
/// enrolled register's, of its epoch, and issued since its session opened.
fn store_snapshot(
    conn: &Connection,
    aead_key: &ZeroizingKey,
    enrolment: &EnrolmentRecord,
    snapshot: &OfflineSnapshot,
) -> Result<(), OfflineError> {
    if snapshot.register_id != enrolment.register_id
        || snapshot.device_epoch != enrolment.device_epoch
    {
        return Err(OfflineError::new(
            "snapshot_unknown",
            "snapshot does not match the enrolled register and device epoch",
        ));
    }
    if snapshot.issued_at < snapshot.till_session.opened_at {
        return Err(OfflineError::new(
            "snapshot_stale",
            "snapshot was issued before the current till session opened",
        ));
    }
    let bytes = serde_json::to_vec(snapshot)
        .map_err(|_| OfflineError::new("serialize_failed", "failed to serialize snapshot"))?;
    let aad = format!(
        "snapshot:{}:{}",
        enrolment.register_id, enrolment.device_epoch
    );
    let sealed = seal_bytes(aead_key, &aad, &bytes)?;
    conn.execute(
        "INSERT INTO offline_snapshot (id, sealed) VALUES (1, ?1)
         ON CONFLICT(id) DO UPDATE SET sealed = excluded.sealed",
        params![sealed],
    )
    .map_err(|_| OfflineError::new("store_io_error", "failed to write snapshot"))?;
    Ok(())
}

/// A sale the server has not answered yet: signed and not sent, or sent and
/// not judged. One with a manager (refused or held) is answered.
fn awaits_an_answer(state: QueueItemState) -> bool {
    matches!(
        state,
        QueueItemState::Signing | QueueItemState::Waiting | QueueItemState::Pending
    )
}

/// The server's sync answer, as the core reads it.
#[derive(serde::Deserialize)]
struct WireSyncResult {
    confirmed_seq: i64,
    #[serde(default)]
    open_exception_count: i64,
    items: Vec<WireSyncItem>,
}

#[derive(serde::Deserialize)]
struct WireSyncItem {
    client_id: String,
    device_epoch: i64,
    device_seq: i64,
    outcome: String,
    invoice_id: Option<String>,
    invoice_number: Option<String>,
    reason_code: Option<String>,
    reason_message: Option<String>,
}

/// The room left on a cached account on this register: the least of the
/// snapshot's offline room, the credit limit less the open receivable and
/// the branch's per account cap, less every account tender this register
/// holds against that snapshot (ADR 0011 sections 1 and 6.2).
fn account_room(
    snapshot: &OfflineSnapshot,
    acc: &crate::offline::model::SnapshotAccount,
    existing_sales: &[QueuedSaleEntry],
) -> i64 {
    let credit_room = (acc.credit_limit_cents - acc.open_receivable_cents).max(0);
    let snapshot_room = acc
        .offline_room_cents
        .min(credit_room)
        .min(snapshot.policy.account_cap_cents)
        .max(0);
    // A sale posted since the snapshot is not in its open receivable,
    // so it keeps counting against that snapshot's room once synced.
    let queued_for_account: i64 = existing_sales
        .iter()
        .filter(|e| counts_against_snapshot(e, &snapshot.snapshot_id))
        .filter(|e| e.sale.customer_id.as_deref() == Some(acc.customer_id.as_str()))
        .flat_map(|e| e.sale.tenders.iter())
        .filter(|t| t.method == "account")
        .map(|t| t.amount_cents)
        .sum();
    snapshot_room - queued_for_account
}

/// The count and total of the sales still waiting on the server; the queue
/// caps count these only, never the synced ones.
fn waiting_figures(existing_sales: &[QueuedSaleEntry]) -> (i64, i64) {
    let mut count = 0i64;
    let mut total = 0i64;
    for entry in existing_sales {
        if entry.state.is_unsettled() {
            count += 1;
            total += entry.sale.total_cents;
        }
    }
    (count, total)
}

/// Whether a queued sale is outside the figures a snapshot was issued with:
/// every sale sold under that snapshot, whatever its state (one posted since
/// is in neither its open receivable nor its on hand), and every unsettled
/// sale, whichever snapshot it names (ADR 0011 section 6.2).
fn counts_against_snapshot(entry: &QueuedSaleEntry, snapshot_id: &str) -> bool {
    entry.state.is_unsettled() || entry.sale.snapshot_id == snapshot_id
}

fn resolve_bound_price_row<'a>(
    snapshot: &'a OfflineSnapshot,
    product: &SnapshotProduct,
    account: Option<&crate::offline::model::SnapshotAccount>,
) -> Result<(&'a SnapshotPriceRow, bool), OfflineError> {
    match account {
        None => {
            let row = snapshot
                .price_sheet
                .iter()
                .find(|r| r.product_id == product.id && r.price_key == "walkin")
                .ok_or_else(|| {
                    OfflineError::new(
                        "price_not_in_snapshot",
                        format!(
                            "no walk-in price row in snapshot for product {}",
                            product.sku
                        ),
                    )
                })?;
            Ok((row, false))
        }
        Some(acc) => {
            if let Some(row) = snapshot
                .price_sheet
                .iter()
                .find(|r| r.product_id == product.id && r.price_key == acc.price_key)
            {
                return Ok((row, false));
            }
            if let Some(level_key) = acc.level_key.as_deref() {
                if let Some(row) = snapshot
                    .price_sheet
                    .iter()
                    .find(|r| r.product_id == product.id && r.price_key == level_key)
                {
                    return Ok((row, true));
                }
            }
            Err(OfflineError::new(
                "price_not_in_snapshot",
                format!(
                    "no price row in snapshot for product {} and customer {}",
                    product.sku, acc.account_number
                ),
            ))
        }
    }
}

fn resolve_pair_for_line(
    product: &SnapshotProduct,
    sale_uom: &str,
    u_unit_qty: i64,
    u_stock_qty: i64,
    price_row: &SnapshotPriceRow,
) -> Result<(i64, i64), OfflineError> {
    if sale_uom == price_row.price_uom {
        return Ok((ONE_QTY, ONE_QTY));
    }
    if sale_uom == product.sale_uom {
        let uq = parse_quantity(&price_row.uom_qty)
            .map_err(|m| OfflineError::new("invalid_price_pair", m))?;
        let pq = parse_quantity(&price_row.price_uom_qty)
            .map_err(|m| OfflineError::new("invalid_price_pair", m))?;
        return Ok((uq, pq));
    }
    let p_row = product
        .unit_set
        .iter()
        .find(|r| r.uom == price_row.price_uom && r.price)
        .ok_or_else(|| {
            OfflineError::new(
                "invalid_price_uom",
                format!(
                    "{} is not a price unit of product {}",
                    price_row.price_uom, product.sku
                ),
            )
        })?;
    let p_unit_qty =
        parse_quantity(&p_row.unit_qty).map_err(|m| OfflineError::new("invalid_unit_set", m))?;
    let p_stock_qty =
        parse_quantity(&p_row.stock_qty).map_err(|m| OfflineError::new("invalid_unit_set", m))?;
    let pair = resolve_line_pair(
        Pair {
            a: u_unit_qty,
            b: u_stock_qty,
        },
        Pair {
            a: p_unit_qty,
            b: p_stock_qty,
        },
    )
    .map_err(|m| OfflineError::new("invalid_price_pair", m))?;
    Ok((pair.a, pair.b))
}

fn init_schema(conn: &Connection) -> Result<(), OfflineError> {
    conn.execute_batch(
        "CREATE TABLE IF NOT EXISTS offline_meta (
            id INTEGER PRIMARY KEY CHECK (id = 1),
            sealed BLOB NOT NULL
        );
        CREATE TABLE IF NOT EXISTS offline_snapshot (
            id INTEGER PRIMARY KEY CHECK (id = 1),
            sealed BLOB NOT NULL
        );
        CREATE TABLE IF NOT EXISTS offline_sales (
            device_seq INTEGER PRIMARY KEY,
            sealed BLOB NOT NULL
        );",
    )
    .map_err(|_| OfflineError::new("store_io_error", "failed to initialize SQLite tables"))
}

fn seal_bytes(key: &ZeroizingKey, aad: &str, plaintext: &[u8]) -> Result<Vec<u8>, OfflineError> {
    let unbound = UnboundKey::new(&AES_256_GCM, &key.0)
        .map_err(|_| OfflineError::new("crypto_error", "failed to initialize AEAD key"))?;
    let aead = LessSafeKey::new(unbound);
    let rng = SystemRandom::new();
    let mut nonce_bytes = [0u8; NONCE_LEN];
    rng.fill(&mut nonce_bytes)
        .map_err(|_| OfflineError::new("rng_failed", "failed to generate AEAD nonce"))?;
    let nonce = Nonce::assume_unique_for_key(nonce_bytes);
    let mut in_out = plaintext.to_vec();
    aead.seal_in_place_append_tag(nonce, Aad::from(aad.as_bytes()), &mut in_out)
        .map_err(|_| OfflineError::new("crypto_error", "failed to seal record"))?;
    let mut buf = Vec::with_capacity(NONCE_LEN + in_out.len());
    buf.extend_from_slice(&nonce_bytes);
    buf.extend_from_slice(&in_out);
    Ok(buf)
}

fn unseal_bytes(key: &ZeroizingKey, aad: &str, sealed: &[u8]) -> Result<Vec<u8>, OfflineError> {
    if sealed.len() < NONCE_LEN + AES_256_GCM.tag_len() {
        return Err(OfflineError::re_enrol_required());
    }
    let unbound =
        UnboundKey::new(&AES_256_GCM, &key.0).map_err(|_| OfflineError::re_enrol_required())?;
    let aead = LessSafeKey::new(unbound);
    let nonce_bytes: [u8; NONCE_LEN] = sealed[..NONCE_LEN]
        .try_into()
        .map_err(|_| OfflineError::re_enrol_required())?;
    let nonce = Nonce::assume_unique_for_key(nonce_bytes);
    let mut in_out = sealed[NONCE_LEN..].to_vec();
    let plain = aead
        .open_in_place(nonce, Aad::from(aad.as_bytes()), &mut in_out)
        .map_err(|_| OfflineError::re_enrol_required())?;
    Ok(plain.to_vec())
}

fn write_sealed_meta(
    conn: &Connection,
    key: &ZeroizingKey,
    meta: &StoreMeta,
) -> Result<(), OfflineError> {
    let aad = format!("meta:{}:{}", meta.register_id, meta.device_epoch);
    let plain = serde_json::to_vec(meta)
        .map_err(|_| OfflineError::new("serialize_failed", "failed to serialize meta"))?;
    let sealed = seal_bytes(key, &aad, &plain)?;
    conn.execute(
        "INSERT INTO offline_meta (id, sealed) VALUES (1, ?1)
         ON CONFLICT(id) DO UPDATE SET sealed = excluded.sealed",
        params![sealed],
    )
    .map_err(|_| OfflineError::new("store_io_error", "failed to write meta"))?;
    Ok(())
}

fn read_sealed_meta(
    conn: &Connection,
    key: &ZeroizingKey,
    register_id: &str,
    device_epoch: i64,
) -> Result<StoreMeta, OfflineError> {
    let sealed: Vec<u8> = conn
        .query_row("SELECT sealed FROM offline_meta WHERE id = 1", [], |row| {
            row.get(0)
        })
        .map_err(|_| OfflineError::re_enrol_required())?;
    let aad = format!("meta:{register_id}:{device_epoch}");
    let plain = unseal_bytes(key, &aad, &sealed)?;
    serde_json::from_slice(&plain).map_err(|_| OfflineError::re_enrol_required())
}

fn seal_sale(
    key: &ZeroizingKey,
    register_id: &str,
    device_epoch: i64,
    entry: &QueuedSaleEntry,
) -> Result<Vec<u8>, OfflineError> {
    let aad = format!("sale:{}:{}:{}", register_id, device_epoch, entry.device_seq);
    let plain = serde_json::to_vec(entry)
        .map_err(|_| OfflineError::new("serialize_failed", "failed to serialize sale"))?;
    seal_bytes(key, &aad, &plain)
}

/// Writes a newly assigned sequence. A plain insert: a sequence already in
/// the store fails the write rather than replacing a signed sale.
fn insert_sealed_sale(
    conn: &Connection,
    key: &ZeroizingKey,
    register_id: &str,
    device_epoch: i64,
    entry: &QueuedSaleEntry,
) -> Result<(), OfflineError> {
    let sealed = seal_sale(key, register_id, device_epoch, entry)?;
    conn.execute(
        "INSERT INTO offline_sales (device_seq, sealed) VALUES (?1, ?2)",
        params![entry.device_seq, sealed],
    )
    .map_err(|_| OfflineError::new("store_io_error", "failed to write sale"))?;
    Ok(())
}

/// Rewrites the sale already stored at its sequence (its signature, its
/// state or its sync outcome).
fn update_sealed_sale(
    conn: &Connection,
    key: &ZeroizingKey,
    register_id: &str,
    device_epoch: i64,
    entry: &QueuedSaleEntry,
) -> Result<(), OfflineError> {
    let sealed = seal_sale(key, register_id, device_epoch, entry)?;
    let changed = conn
        .execute(
            "UPDATE offline_sales SET sealed = ?2 WHERE device_seq = ?1",
            params![entry.device_seq, sealed],
        )
        .map_err(|_| OfflineError::new("store_io_error", "failed to write sale"))?;
    if changed != 1 {
        return Err(OfflineError::new(
            "store_io_error",
            "the sale to update is not in the store",
        ));
    }
    Ok(())
}

fn read_sealed_sale_opt(
    conn: &Connection,
    key: &ZeroizingKey,
    register_id: &str,
    device_epoch: i64,
    device_seq: i64,
) -> Result<Option<QueuedSaleEntry>, OfflineError> {
    let sealed_opt: Option<Vec<u8>> = conn
        .query_row(
            "SELECT sealed FROM offline_sales WHERE device_seq = ?1",
            params![device_seq],
            |row| row.get(0),
        )
        .optional()
        .map_err(|_| OfflineError::re_enrol_required())?;
    let Some(sealed) = sealed_opt else {
        return Ok(None);
    };
    let aad = format!("sale:{register_id}:{device_epoch}:{device_seq}");
    let plain = unseal_bytes(key, &aad, &sealed)?;
    let entry: QueuedSaleEntry =
        serde_json::from_slice(&plain).map_err(|_| OfflineError::re_enrol_required())?;
    Ok(Some(entry))
}

fn read_all_sales(
    conn: &Connection,
    key: &ZeroizingKey,
    enrolment: &EnrolmentRecord,
) -> Result<Vec<QueuedSaleEntry>, OfflineError> {
    let mut stmt = conn
        .prepare("SELECT device_seq, sealed FROM offline_sales ORDER BY device_seq ASC")
        .map_err(|_| OfflineError::re_enrol_required())?;
    let rows = stmt
        .query_map([], |row| {
            let seq: i64 = row.get(0)?;
            let sealed: Vec<u8> = row.get(1)?;
            Ok((seq, sealed))
        })
        .map_err(|_| OfflineError::re_enrol_required())?;
    let mut out = Vec::new();
    for r in rows {
        let (seq, sealed) = r.map_err(|_| OfflineError::re_enrol_required())?;
        let aad = format!(
            "sale:{}:{}:{}",
            enrolment.register_id, enrolment.device_epoch, seq
        );
        let plain = unseal_bytes(key, &aad, &sealed)?;
        let entry: QueuedSaleEntry =
            serde_json::from_slice(&plain).map_err(|_| OfflineError::re_enrol_required())?;
        out.push(entry);
    }
    Ok(out)
}

fn read_raw_snapshot(
    conn: &Connection,
    key: &ZeroizingKey,
    enrolment: &EnrolmentRecord,
) -> Result<OfflineSnapshot, OfflineError> {
    let sealed: Vec<u8> = conn
        .query_row(
            "SELECT sealed FROM offline_snapshot WHERE id = 1",
            [],
            |row| row.get(0),
        )
        .map_err(|_| {
            OfflineError::new(
                "snapshot_missing",
                "no register snapshot is stored for offline selling",
            )
        })?;
    let aad = format!(
        "snapshot:{}:{}",
        enrolment.register_id, enrolment.device_epoch
    );
    let plain = unseal_bytes(key, &aad, &sealed)?;
    serde_json::from_slice(&plain).map_err(|_| OfflineError::re_enrol_required())
}

/// Produces the canonical JSON bytes of an offline sale with keys sorted
/// lexicographically at every object level and no whitespace, excluding
/// `device_sig` (ADR 0011 section 4.1).
pub fn canonical_sale_bytes(sale: &SignedOfflineSale) -> Result<Vec<u8>, OfflineError> {
    let mut value = serde_json::to_value(sale)
        .map_err(|_| OfflineError::new("serialize_failed", "failed to convert sale to JSON"))?;
    if let Some(obj) = value.as_object_mut() {
        obj.remove("device_sig");
    }
    serde_json::to_vec(&value)
        .map_err(|_| OfflineError::new("serialize_failed", "failed to encode canonical JSON"))
}

/// Computes the 64-character lowercase hex SHA-256 digest of `bytes`.
pub fn sha256_hex(bytes: &[u8]) -> String {
    hex::encode(digest(&SHA256, bytes).as_ref())
}

/// Test seam for the shared signing vector: signs canonical bytes with a
/// given seed, the sign path's own signing.
#[cfg(test)]
pub(crate) fn sign_canonical_for_vector(
    seed_hex: &str,
    canonical_bytes: &[u8],
) -> Result<String, OfflineError> {
    sign_canonical_bytes(seed_hex, canonical_bytes)
}

fn sign_canonical_bytes(seed_hex: &str, canonical_bytes: &[u8]) -> Result<String, OfflineError> {
    let mut seed_vec =
        hex::decode(seed_hex.trim()).map_err(|_| OfflineError::re_enrol_required())?;
    let seed_arr: [u8; 32] = seed_vec
        .as_slice()
        .try_into()
        .map_err(|_| OfflineError::re_enrol_required())?;
    seed_vec.zeroize();
    let key_pair = Ed25519KeyPair::from_seed_unchecked(&seed_arr)
        .map_err(|_| OfflineError::re_enrol_required())?;
    let sig = key_pair.sign(canonical_bytes);
    Ok(base64::engine::general_purpose::STANDARD.encode(sig.as_ref()))
}

/// Verifies the Ed25519 `device_sig` on a `SignedOfflineSale` against the
/// enrolled register's 64-character hex public key.
pub fn verify_sale_signature(
    public_key_hex: &str,
    sale: &SignedOfflineSale,
) -> Result<(), OfflineError> {
    let pk_bytes = hex::decode(public_key_hex.trim())
        .map_err(|_| OfflineError::new("invalid_public_key", "public key is not valid hex"))?;
    let sig_bytes = base64::engine::general_purpose::STANDARD
        .decode(sale.device_sig.trim())
        .map_err(|_| OfflineError::new("invalid_signature", "device_sig is not valid base64"))?;
    let canonical = canonical_sale_bytes(sale)?;
    let pk = UnparsedPublicKey::new(&ED25519, &pk_bytes);
    pk.verify(&canonical, &sig_bytes).map_err(|_| {
        OfflineError::new("invalid_signature", "Ed25519 signature verification failed")
    })
}
