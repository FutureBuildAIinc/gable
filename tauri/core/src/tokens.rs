// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! Custody of the core tokens: the access and refresh tokens a signed in
//! session holds, and everything the contract promises about them, decided
//! here where the tests can hold it. The profile crates' commands are thin
//! wrappers.
//!
//! - `TokenStore` stores both the access and the refresh token in the OS
//!   keychain, one entry per token under the profile's own service: never in
//!   a file, never in a log line. `core_token` exposes only the access token
//!   across IPC while the refresh token stays in the keychain, and the
//!   webview's own storage is the web app's affair until its sign in writes
//!   this store.
//! - **Store** writes the refresh entry only after the access entry is in,
//!   and rolls the access entry back if the refresh write fails: a half
//!   written store is never left behind.
//! - **Read** answers a session only when both entries are there. A torn
//!   store (one entry, the other gone) is not a session: it is cleaned up
//!   and read as no session.
//! - **Clear** (sign out) removes both entries; an absent entry is fine.
//! - **Switch** (profile switch) clears the profile being left and is
//!   refused, before anything is cleared, when either profile name is not a
//!   contract shape. The profile being opened is never touched here.
//!
//! Nothing here logs or prints, every failure is the custody's one fail
//! closed code, and the values are wiped when their holding value drops.

use std::sync::Arc;

use zeroize::Zeroizing;

use crate::custody::{Address, Custody, CustodyError, TokenSlot};

/// A token pair a session holds. The strings zero when their holding value
/// drops, and the `Debug` impl redacts: a panic or a test failure prints
/// the pair's shape, never a token.
#[derive(Clone, PartialEq)]
pub struct TokenSet {
    access: Zeroizing<String>,
    refresh: Zeroizing<String>,
}

impl std::fmt::Debug for TokenSet {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("TokenSet").finish_non_exhaustive()
    }
}

/// A token pair that is not a session. It carries nothing.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct BadToken;

impl TokenSet {
    /// An empty token is never a session token; the pair is refused before
    /// any write happens.
    pub fn new(access: String, refresh: String) -> Result<Self, BadToken> {
        if access.is_empty() || refresh.is_empty() {
            return Err(BadToken);
        }
        Ok(Self {
            access: Zeroizing::new(access),
            refresh: Zeroizing::new(refresh),
        })
    }

    pub fn access(&self) -> &str {
        &self.access
    }

    pub fn refresh(&self) -> &str {
        &self.refresh
    }
}

/// The one store behind every profile's commands. `Clone` shares the custody,
/// so the store a test builds and the one a command runs are the same entries.
#[derive(Clone)]
pub struct TokenStore {
    custody: Arc<dyn Custody>,
}

impl TokenStore {
    pub fn new(custody: Arc<dyn Custody>) -> Self {
        Self { custody }
    }

    /// The address shape, fail closed: a profile name that is not a contract
    /// shape is an unreachable keychain, never a made up address.
    fn address(profile: &str, slot: TokenSlot) -> Result<Address, CustodyError> {
        Address::token(profile, slot).map_err(|_| CustodyError::KeychainUnavailable)
    }

    pub fn store(&self, profile: &str, tokens: &TokenSet) -> Result<(), CustodyError> {
        let access = Self::address(profile, TokenSlot::Access)?;
        let refresh = Self::address(profile, TokenSlot::Refresh)?;
        self.custody.set(&access, tokens.access())?;
        if let Err(e) = self.custody.set(&refresh, tokens.refresh()) {
            // The pair is the session; half of it must not survive.
            let _ = self.custody.delete(&access);
            return Err(e);
        }
        Ok(())
    }

    pub fn read(&self, profile: &str) -> Result<Option<TokenSet>, CustodyError> {
        let access = Self::address(profile, TokenSlot::Access)?;
        let refresh = Self::address(profile, TokenSlot::Refresh)?;
        match (self.custody.get(&access)?, self.custody.get(&refresh)?) {
            (Some(a), Some(r)) => TokenSet::new(a, r)
                .map(Some)
                .map_err(|_| CustodyError::KeychainUnavailable),
            (None, None) => Ok(None),
            // A torn store is not a session: clean it so the next sign in
            // starts from nothing, and answer no session.
            _ => {
                let _ = self.custody.delete(&access);
                let _ = self.custody.delete(&refresh);
                Ok(None)
            }
        }
    }

    pub fn clear(&self, profile: &str) -> Result<(), CustodyError> {
        let access = Self::address(profile, TokenSlot::Access)?;
        let refresh = Self::address(profile, TokenSlot::Refresh)?;
        self.custody.delete(&access)?;
        self.custody.delete(&refresh)
    }

    /// The profile being left loses its tokens; the one being opened keeps
    /// whatever is already its own. Either name that is not a contract
    /// shape refuses the switch before anything is cleared.
    pub fn switch(&self, from: &str, to: &str) -> Result<(), CustodyError> {
        // A `to` name that is not a contract shape refuses the whole switch
        // here, before clear touches anything; `from` is validated by clear.
        Self::address(to, TokenSlot::Access)?;
        self.clear(from)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::custody::MemoryCustody;

    const WORK: &str = "ai.futurebuild.gable.work";
    const FIELD: &str = "ai.futurebuild.gable.field";

    fn tokens() -> TokenSet {
        TokenSet::new(
            "access-sentinel".to_string(),
            "refresh-sentinel".to_string(),
        )
        .expect("a valid pair")
    }

    fn addr(profile: &str, slot: TokenSlot) -> Address {
        Address::token(profile, slot).unwrap()
    }

    #[test]
    fn a_pair_refuses_an_empty_half_before_any_write() {
        assert_eq!(TokenSet::new(String::new(), "r".to_string()), Err(BadToken));
        assert_eq!(TokenSet::new("a".to_string(), String::new()), Err(BadToken));
        assert_eq!(TokenSet::new(String::new(), String::new()), Err(BadToken));
    }

    #[test]
    fn store_then_read_round_trips_both_tokens() {
        let store = TokenStore::new(Arc::new(MemoryCustody::new()));
        assert_eq!(store.read(WORK), Ok(None));
        store.store(WORK, &tokens()).unwrap();
        let read = store.read(WORK).unwrap().expect("a session");
        assert_eq!(read.access(), "access-sentinel");
        assert_eq!(read.refresh(), "refresh-sentinel");
    }

    #[test]
    fn the_entries_are_the_contracts_service_and_account_and_nothing_else() {
        let custody = Arc::new(MemoryCustody::new());
        let store = TokenStore::new(custody.clone());
        store.store(WORK, &tokens()).unwrap();
        let mut keys: Vec<String> = custody
            .snapshot_pairs()
            .into_iter()
            .map(|(s, a)| format!("{s} {a}"))
            .collect();
        keys.sort();
        assert_eq!(
            keys,
            [
                "ai.futurebuild.gable.work core-token:access",
                "ai.futurebuild.gable.work core-token:refresh",
            ]
        );
    }

    #[test]
    fn clear_on_sign_out_removes_both_entries_and_an_absent_pair_is_fine() {
        let store = TokenStore::new(Arc::new(MemoryCustody::new()));
        store.clear(WORK).unwrap();
        store.store(WORK, &tokens()).unwrap();
        store.clear(WORK).unwrap();
        assert_eq!(store.read(WORK), Ok(None));
    }

    #[test]
    fn a_profile_switch_clears_the_left_profile_and_never_touches_the_opened_one() {
        let store = TokenStore::new(Arc::new(MemoryCustody::new()));
        store.store(WORK, &tokens()).unwrap();
        store.store(FIELD, &tokens()).unwrap();
        store.switch(WORK, FIELD).unwrap();
        assert_eq!(store.read(WORK), Ok(None), "the left profile is cleared");
        assert!(
            store.read(FIELD).unwrap().is_some(),
            "the opened profile keeps its own tokens"
        );
    }

    #[test]
    fn a_switch_to_a_name_that_is_not_a_contract_shape_refuses_before_clearing() {
        let store = TokenStore::new(Arc::new(MemoryCustody::new()));
        store.store(WORK, &tokens()).unwrap();
        assert_eq!(
            store.switch(WORK, "ai.futurebuild.gable.Field"),
            Err(CustodyError::KeychainUnavailable)
        );
        assert!(store.read(WORK).unwrap().is_some(), "nothing was cleared");
    }

    #[test]
    fn a_refresh_write_that_fails_rolls_the_access_entry_back() {
        // The first write (access) lands, the second (refresh) fails closed.
        let custody = Arc::new(MemoryCustody::failing_after(1));
        let store = TokenStore::new(custody.clone());
        assert_eq!(
            store.store(WORK, &tokens()),
            Err(CustodyError::KeychainUnavailable)
        );
        assert_eq!(
            custody.snapshot_len(),
            0,
            "a half written store is never left behind"
        );
        assert_eq!(store.read(WORK), Ok(None));
    }

    #[test]
    fn a_torn_store_reads_as_no_session_and_cleans_itself_up() {
        let custody = Arc::new(MemoryCustody::new());
        let store = TokenStore::new(custody.clone());
        custody
            .set(&addr(WORK, TokenSlot::Access), "orphan-access")
            .unwrap();
        assert_eq!(store.read(WORK), Ok(None));
        assert_eq!(
            custody.snapshot_len(),
            0,
            "the torn entries are removed, not left to read as a session later"
        );
    }

    #[test]
    fn an_unavailable_keychain_fails_closed_on_every_operation() {
        let store = TokenStore::new(Arc::new(MemoryCustody::unavailable()));
        assert_eq!(
            store.store(WORK, &tokens()),
            Err(CustodyError::KeychainUnavailable)
        );
        assert_eq!(store.read(WORK), Err(CustodyError::KeychainUnavailable));
        assert_eq!(store.clear(WORK), Err(CustodyError::KeychainUnavailable));
        assert_eq!(
            store.switch(WORK, FIELD),
            Err(CustodyError::KeychainUnavailable)
        );
    }

    #[test]
    fn profiles_never_share_entries() {
        let store = TokenStore::new(Arc::new(MemoryCustody::new()));
        store.store(WORK, &tokens()).unwrap();
        assert_eq!(store.read(FIELD), Ok(None));
        store.clear(FIELD).unwrap();
        assert!(store.read(WORK).unwrap().is_some());
    }
}
