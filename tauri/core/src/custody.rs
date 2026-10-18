// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! The OS keychain custody behind the shells: one value under a service and
//! an account, behind a trait so the token code never names a platform.
//!
//! The keychain contract: service `ai.futurebuild.gable.<profile>` (the
//! profile model's identifier, so two profiles never share an entry), account
//! `core-token:<slot>`, value one token and nothing else. Only the Rust side
//! reaches an entry; the webview holds no command that names a service or an
//! account.
//!
//! **Fail closed.** If the platform keychain cannot be reached, every
//! operation answers `KeychainUnavailable`. There is no fallback to a file,
//! to web storage or to memory, and the error carries a code only: no
//! platform message, no service, no account, no value.

use std::{collections::HashMap, sync::Mutex};

/// Which token of a profile an entry keeps.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TokenSlot {
    Access,
    Refresh,
}

impl TokenSlot {
    pub fn name(self) -> &'static str {
        match self {
            TokenSlot::Access => "access",
            TokenSlot::Refresh => "refresh",
        }
    }
}

/// The one way custody fails. A single code on purpose: a caller cannot tell
/// "no daemon" from "locked" from "denied", and none of them may be worked
/// around.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum CustodyError {
    KeychainUnavailable,
}

impl CustodyError {
    /// The error word the commands hand the webview.
    pub fn code(self) -> &'static str {
        match self {
            CustodyError::KeychainUnavailable => "keychain_unavailable",
        }
    }
}

const SERVICE_PREFIX: &str = "ai.futurebuild.gable.";
const ACCOUNT_PREFIX: &str = "core-token:";

/// Where one token lives. Built only by `token`, from the profile's
/// identifier and the slot, so no caller invents an address and the webview
/// never supplies one.
#[derive(Clone, Debug, PartialEq, Eq, Hash)]
pub struct Address {
    service: String,
    account: String,
}

/// An address that does not match the contract's shape. It carries nothing.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct BadAddress;

impl Address {
    /// `profile_identifier` is the profile model's identifier
    /// (`ai.futurebuild.gable.work`); the remainder after the service prefix
    /// must be non empty lowercase letters and digits, so an entry can never
    /// be filed under a name another install would miss.
    pub fn token(profile_identifier: &str, slot: TokenSlot) -> Result<Self, BadAddress> {
        let profile_ok = profile_identifier
            .strip_prefix(SERVICE_PREFIX)
            .is_some_and(|p| {
                !p.is_empty()
                    && p.bytes()
                        .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit())
            });
        if !profile_ok {
            return Err(BadAddress);
        }
        Ok(Self {
            service: profile_identifier.to_string(),
            account: format!("{ACCOUNT_PREFIX}{}", slot.name()),
        })
    }

    pub fn service(&self) -> &str {
        &self.service
    }

    pub fn account(&self) -> &str {
        &self.account
    }
}

/// Get, set and delete one secret. `get` of an absent entry is `Ok(None)` and
/// `delete` of an absent entry is `Ok(())`: only an unreachable keychain is
/// an error.
pub trait Custody: Send + Sync {
    fn get(&self, address: &Address) -> Result<Option<String>, CustodyError>;
    fn set(&self, address: &Address, value: &str) -> Result<(), CustodyError>;
    fn delete(&self, address: &Address) -> Result<(), CustodyError>;
}

/// The desktop implementations: the Secret Service on Linux, the macOS
/// Keychain and the Windows Credential Manager, all through the keyring
/// crate's per target backend.
#[cfg(any(target_os = "linux", target_os = "macos", target_os = "windows"))]
pub struct KeyringCustody;

#[cfg(any(target_os = "linux", target_os = "macos", target_os = "windows"))]
impl KeyringCustody {
    fn entry(address: &Address) -> Result<keyring::Entry, CustodyError> {
        keyring::Entry::new(address.service(), address.account())
            .map_err(|_| CustodyError::KeychainUnavailable)
    }
}

#[cfg(any(target_os = "linux", target_os = "macos", target_os = "windows"))]
impl Custody for KeyringCustody {
    fn get(&self, address: &Address) -> Result<Option<String>, CustodyError> {
        match Self::entry(address)?.get_password() {
            Ok(v) => Ok(Some(v)),
            Err(keyring::Error::NoEntry) => Ok(None),
            Err(_) => Err(CustodyError::KeychainUnavailable),
        }
    }

    fn set(&self, address: &Address, value: &str) -> Result<(), CustodyError> {
        Self::entry(address)?
            .set_password(value)
            .map_err(|_| CustodyError::KeychainUnavailable)
    }

    fn delete(&self, address: &Address) -> Result<(), CustodyError> {
        match Self::entry(address)?.delete_credential() {
            Ok(()) | Err(keyring::Error::NoEntry) => Ok(()),
            Err(_) => Err(CustodyError::KeychainUnavailable),
        }
    }
}

/// A target with no keychain backend yet (mobile lands with its own items):
/// every operation fails closed.
pub struct NoBackend;

impl Custody for NoBackend {
    fn get(&self, _: &Address) -> Result<Option<String>, CustodyError> {
        Err(CustodyError::KeychainUnavailable)
    }
    fn set(&self, _: &Address, _: &str) -> Result<(), CustodyError> {
        Err(CustodyError::KeychainUnavailable)
    }
    fn delete(&self, _: &Address) -> Result<(), CustodyError> {
        Err(CustodyError::KeychainUnavailable)
    }
}

/// The custody this build's target uses: the platform keychain on the three
/// desktop targets, and nothing (fail closed) elsewhere.
pub fn platform_custody() -> Box<dyn Custody> {
    #[cfg(any(target_os = "linux", target_os = "macos", target_os = "windows"))]
    {
        Box::new(KeyringCustody)
    }
    #[cfg(not(any(target_os = "linux", target_os = "macos", target_os = "windows")))]
    {
        Box::new(NoBackend)
    }
}

/// An in memory fake for tests (and nothing else: a shipping build never
/// constructs one). Tests never touch a real keychain. `unavailable()` is
/// the fail closed case.
pub struct MemoryCustody {
    entries: Mutex<HashMap<Address, String>>,
    available: bool,
    /// Counts `set` calls, so a test can fail exactly the write it chooses.
    writes: Mutex<usize>,
    fail_write_after: Option<usize>,
}

impl MemoryCustody {
    pub fn new() -> Self {
        Self {
            entries: Mutex::new(HashMap::new()),
            available: true,
            writes: Mutex::new(0),
            fail_write_after: None,
        }
    }

    pub fn unavailable() -> Self {
        Self {
            entries: Mutex::new(HashMap::new()),
            available: false,
            writes: Mutex::new(0),
            fail_write_after: None,
        }
    }

    /// Reads and deletes work; every `set` after `n` successful ones fails
    /// closed (a keychain that locked between two writes).
    pub fn failing_after(n: usize) -> Self {
        Self {
            fail_write_after: Some(n),
            ..Self::new()
        }
    }

    /// How many entries are held (tests assert a failed write stored nothing).
    pub fn snapshot_len(&self) -> usize {
        self.entries.lock().expect("custody fake lock").len()
    }

    /// The service and account of every entry held (tests assert the store
    /// filed the pair under the contract's names and nothing else).
    pub fn snapshot_pairs(&self) -> Vec<(String, String)> {
        self.entries
            .lock()
            .expect("custody fake lock")
            .keys()
            .map(|a| (a.service().to_string(), a.account().to_string()))
            .collect()
    }

    fn check(&self) -> Result<(), CustodyError> {
        if self.available {
            Ok(())
        } else {
            Err(CustodyError::KeychainUnavailable)
        }
    }
}

impl Default for MemoryCustody {
    fn default() -> Self {
        Self::new()
    }
}

impl Custody for MemoryCustody {
    fn get(&self, address: &Address) -> Result<Option<String>, CustodyError> {
        self.check()?;
        Ok(self
            .entries
            .lock()
            .expect("custody fake lock")
            .get(address)
            .cloned())
    }

    fn set(&self, address: &Address, value: &str) -> Result<(), CustodyError> {
        self.check()?;
        if let Some(after) = self.fail_write_after {
            let mut writes = self.writes.lock().expect("custody fake lock");
            if *writes >= after {
                return Err(CustodyError::KeychainUnavailable);
            }
            *writes += 1;
        }
        self.entries
            .lock()
            .expect("custody fake lock")
            .insert(address.clone(), value.to_string());
        Ok(())
    }

    fn delete(&self, address: &Address) -> Result<(), CustodyError> {
        self.check()?;
        self.entries
            .lock()
            .expect("custody fake lock")
            .remove(address);
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn addr() -> Address {
        Address::token("ai.futurebuild.gable.field", TokenSlot::Access).expect("a valid address")
    }

    #[test]
    fn the_address_is_the_contracts_service_and_account() {
        let a = addr();
        assert_eq!(a.service(), "ai.futurebuild.gable.field");
        assert_eq!(a.account(), "core-token:access");
        assert_eq!(
            Address::token("ai.futurebuild.gable.work", TokenSlot::Refresh)
                .unwrap()
                .account(),
            "core-token:refresh"
        );
    }

    #[test]
    fn an_address_refuses_a_foreign_service_and_a_bad_profile_name() {
        assert!(Address::token("com.example.other", TokenSlot::Access).is_err());
        assert!(Address::token("ai.futurebuild.gable.", TokenSlot::Access).is_err());
        for bad in [
            "ai.futurebuild.gable.Work",
            "ai.futurebuild.gable.work field",
            "ai.futurebuild.gable.work/field",
            "ai.futurebuild.gable.work_extra",
            "ai.futurebuild.gable.",
        ] {
            assert!(Address::token(bad, TokenSlot::Access).is_err(), "{bad}");
        }
    }

    #[test]
    fn the_slots_name_two_distinct_accounts() {
        let access = Address::token("ai.futurebuild.gable.pro", TokenSlot::Access).unwrap();
        let refresh = Address::token("ai.futurebuild.gable.pro", TokenSlot::Refresh).unwrap();
        assert_ne!(access, refresh);
    }

    #[test]
    fn set_then_get_round_trips_and_overwrites() {
        let c = MemoryCustody::new();
        assert_eq!(c.get(&addr()), Ok(None));
        c.set(&addr(), "one").unwrap();
        assert_eq!(c.get(&addr()), Ok(Some("one".to_string())));
        c.set(&addr(), "two").unwrap();
        assert_eq!(c.get(&addr()), Ok(Some("two".to_string())));
    }

    #[test]
    fn delete_removes_the_entry_and_an_absent_entry_is_not_an_error() {
        let c = MemoryCustody::new();
        c.delete(&addr()).unwrap();
        c.set(&addr(), "one").unwrap();
        c.delete(&addr()).unwrap();
        assert_eq!(c.get(&addr()), Ok(None));
    }

    #[test]
    fn entries_are_per_profile() {
        let c = MemoryCustody::new();
        let work = Address::token("ai.futurebuild.gable.work", TokenSlot::Access).unwrap();
        c.set(&addr(), "field").unwrap();
        assert_eq!(c.get(&work), Ok(None));
    }

    #[test]
    fn an_unavailable_keychain_fails_closed_on_every_operation() {
        let c = MemoryCustody::unavailable();
        assert_eq!(c.get(&addr()), Err(CustodyError::KeychainUnavailable));
        assert_eq!(c.set(&addr(), "x"), Err(CustodyError::KeychainUnavailable));
        assert_eq!(c.delete(&addr()), Err(CustodyError::KeychainUnavailable));
        // A failed write stored nothing behind the caller's back.
        assert_eq!(c.snapshot_len(), 0);
    }

    #[test]
    fn the_error_carries_a_code_and_no_input() {
        assert_eq!(
            CustodyError::KeychainUnavailable.code(),
            "keychain_unavailable"
        );
        assert_eq!(
            format!("{:?}", CustodyError::KeychainUnavailable),
            "KeychainUnavailable"
        );
    }
}
