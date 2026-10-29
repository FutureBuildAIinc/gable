// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! The shell a profile crate wraps: the managed token state, the URL
//! override, and the answers behind the profile's two token commands. One
//! implementation for the three profiles, parameterised by the profile
//! itself; a profile crate owns only its env var's name, its Tauri command
//! wrappers and its window.
//!
//! Nothing here logs or prints a value: the one warning in `configured_url`
//! names the environment variable and never the value.

use std::sync::Arc;

use url::Url;

use crate::custody::{self, Address, Custody, CustodyError, OfflineSlot};
use crate::fence::{BadOrigin, OriginFence};
use crate::offline::EnrolmentRecord;
use crate::profiles::Profile;
use crate::tokens::{TokenSet, TokenStore};

/// The permissions granted by the work shell's runtime capability to the
/// configured Gable URL and `/pos` on the `main` window (ADR 0011 section 5.2):
/// the two token commands and the seven offline counter commands, and nothing
/// else.
pub const WORK_REMOTE_PERMISSIONS: [&str; 9] = [
    "allow-core-token",
    "allow-clear-tokens",
    "allow-enrol-offline-register",
    "allow-offline-enrolment-status",
    "allow-store-offline-snapshot",
    "allow-load-offline-snapshot",
    "allow-sign-and-queue-offline-sale",
    "allow-offline-queue-status",
    "allow-record-offline-sync-outcomes",
];

/// The origins the app's own commands always answer to: the bundled shell's
/// Tauri origins. The URL override's origin joins them at startup.
const BUNDLED_ORIGINS: [&str; 3] = [
    "http://tauri.localhost",
    "https://tauri.localhost",
    "tauri://localhost",
];

/// The error words the commands answer with. A code only: no service, no
/// account, no value.
pub const ERR_ORIGIN: &str = "forbidden_origin";
pub const ERR_NO_SESSION: &str = "no_session";
pub const ERR_CUSTODY: &str = "keychain_unavailable";

/// Reads and validates a configured shell URL: http or https only.
pub fn parse_shell_url(raw: &str) -> Option<Url> {
    let url = Url::parse(raw.trim()).ok()?;
    matches!(url.scheme(), "http" | "https").then_some(url)
}

/// Reads the URL override named by `url_var` (the profile crate's own
/// environment variable): http or https only. A set but invalid value warns
/// once, naming the variable and never a value, and the bundle loads
/// instead.
pub fn configured_url(url_var: &str) -> Option<Url> {
    let raw = std::env::var(url_var)
        .ok()
        .filter(|v| !v.trim().is_empty())?;
    let parsed = parse_shell_url(&raw);
    if parsed.is_none() {
        eprintln!("{url_var} is not an http or https URL; loading the bundle");
    }
    parsed
}

/// Resolves the Gable URL the work shell loads (ADR 0011 section 5.2): the
/// enrolled device's own `gable_url` setting in the OS keychain takes
/// precedence, falling back to `configured_url(url_var)` only before the
/// register is enrolled.
pub fn enrolled_or_configured_url(
    custody: &dyn Custody,
    profile: &Profile,
    url_var: &str,
) -> Option<Url> {
    if let Ok(addr) = Address::offline(profile.identifier, OfflineSlot::Enrolment) {
        if let Ok(Some(raw)) = custody.get(&addr) {
            if let Ok(rec) = serde_json::from_str::<EnrolmentRecord>(&raw) {
                if let Some(url) = parse_shell_url(&rec.gable_url) {
                    return Some(url);
                }
            }
        }
    }
    configured_url(url_var)
}

/// Builds the runtime capability JSON for the Gable URL the work shell loaded
/// (ADR 0011 section 5.2): `windows: ["main"]`, `local: false`, `remote.urls`
/// naming the loaded URL and `/pos` on that Gable's origin, and `permissions`
/// listing the offline queue commands and the two token commands only.
pub fn work_remote_capability_json(gable_url: &Url) -> String {
    let origin = match gable_url.port() {
        Some(port) => format!(
            "{}://{}:{}",
            gable_url.scheme(),
            gable_url.host_str().unwrap_or_default(),
            port
        ),
        None => format!(
            "{}://{}",
            gable_url.scheme(),
            gable_url.host_str().unwrap_or_default()
        ),
    };
    let pos_url = format!("{origin}/pos");
    let pos_glob = format!("{origin}/pos/*");
    let mut urls = vec![gable_url.as_str().to_string()];
    if !urls.contains(&pos_url) {
        urls.push(pos_url);
    }
    if !urls.contains(&pos_glob) {
        urls.push(pos_glob);
    }
    let capability = serde_json::json!({
        "identifier": "work-remote",
        "description": "Runtime capability for the enrolled Gable origin and /pos on the work shell main window.",
        "local": false,
        "windows": ["main"],
        "remote": {
            "urls": urls,
        },
        "permissions": WORK_REMOTE_PERMISSIONS,
    });
    serde_json::to_string(&capability).expect("serialize runtime capability")
}

/// The managed state behind a profile's two token commands.
#[derive(Clone)]
pub struct TokenState {
    profile: &'static Profile,
    store: TokenStore,
    fence: OriginFence,
}

impl TokenState {
    /// The real state for `profile`: the platform keychain, and the app's
    /// own origins (the bundled ones plus the override's). A fence that
    /// cannot name its own origin refuses whole, and the shell runs no
    /// token command.
    pub fn for_profile(
        profile: &'static Profile,
        override_url: Option<&Url>,
    ) -> Result<Self, BadOrigin> {
        let custody: Arc<dyn Custody> = Arc::from(custody::platform_custody());
        Self::build(TokenStore::new(custody), profile, override_url)
    }

    /// A state over any custody (tests build it over the in memory fake;
    /// tests never touch a real keychain).
    pub fn build(
        store: TokenStore,
        profile: &'static Profile,
        override_url: Option<&Url>,
    ) -> Result<Self, BadOrigin> {
        let mut starts: Vec<&str> = BUNDLED_ORIGINS.to_vec();
        if let Some(url) = override_url {
            starts.push(url.as_str());
        }
        Ok(Self {
            profile,
            store,
            fence: OriginFence::from_starts(&starts)?,
        })
    }

    /// The profile this state serves.
    pub fn profile(&self) -> &'static Profile {
        self.profile
    }

    /// Writes a fresh token pair for this state's profile from a Rust
    /// sign-in or refresh flow. Never exposed as a webview command.
    pub fn store_tokens(&self, tokens: &TokenSet) -> Result<(), CustodyError> {
        self.store.store(self.profile.identifier, tokens)
    }

    /// Leaves this profile's session for `to` inside Rust: this profile's
    /// tokens are cleared and `to` keeps whatever is already its own. Never
    /// exposed as a webview command.
    pub fn switch_profile(&self, to: &'static Profile) -> Result<(), CustodyError> {
        self.store.switch(self.profile.identifier, to.identifier)
    }
}

/// The access token for the app's own origin, or the refusal word. The
/// refresh token never leaves the keychain.
pub fn core_token_answer(state: &TokenState, caller_url: &str) -> Result<String, String> {
    if !state.fence.allows(caller_url) {
        return Err(ERR_ORIGIN.to_string());
    }
    match state.store.read(state.profile.identifier) {
        Ok(Some(tokens)) => Ok(tokens.access().to_string()),
        Ok(None) => Err(ERR_NO_SESSION.to_string()),
        Err(e) => Err(e.code().to_string()),
    }
}

/// Sign out's custody half: both token entries are gone when this returns.
pub fn clear_tokens_answer(state: &TokenState, caller_url: &str) -> Result<(), String> {
    if !state.fence.allows(caller_url) {
        return Err(ERR_ORIGIN.to_string());
    }
    state
        .store
        .clear(state.profile.identifier)
        .map_err(|e| e.code().to_string())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::custody::MemoryCustody;
    use crate::profiles;
    use crate::tokens::TokenSet;

    const ACCESS: &str = "access-sentinel";
    const REFRESH: &str = "refresh-sentinel";
    const OWN: &str = "http://tauri.localhost/index.html";

    fn pair() -> TokenSet {
        TokenSet::new(ACCESS.to_string(), REFRESH.to_string()).expect("a valid pair")
    }

    fn state_for(profile: &'static Profile) -> (TokenState, Arc<MemoryCustody>) {
        let custody = Arc::new(MemoryCustody::new());
        let state = TokenState::build(TokenStore::new(custody.clone()), profile, None)
            .expect("a fence over the bundled origins");
        (state, custody)
    }

    #[test]
    fn the_state_serves_the_profile_it_was_built_for_and_no_other() {
        // The parameterisation is the point: a work state and a field state
        // built over the SAME TokenStore read and clear only their own
        // profile's entries.
        let store = TokenStore::new(Arc::new(MemoryCustody::new()));
        let work = TokenState::build(store.clone(), profiles::work(), None).unwrap();
        let field = TokenState::build(store, profiles::field(), None).unwrap();

        work.store_tokens(&pair()).unwrap();
        let answer = core_token_answer(&work, OWN).unwrap();
        assert_eq!(answer, ACCESS);
        assert_ne!(answer, REFRESH);

        assert_eq!(core_token_answer(&field, OWN).unwrap_err(), ERR_NO_SESSION);
        assert_eq!(
            clear_tokens_answer(&field, OWN).unwrap(),
            (),
            "clearing field's custody never needs field's entries to exist"
        );
        assert_eq!(
            core_token_answer(&work, OWN).unwrap(),
            ACCESS,
            "a field state's clear over the shared store cannot be work's sign out"
        );
    }

    #[test]
    fn rust_token_write_and_profile_switch_hand_off_to_field_over_one_store() {
        let store = TokenStore::new(Arc::new(MemoryCustody::new()));
        let work = TokenState::build(store.clone(), profiles::work(), None).unwrap();
        let field = TokenState::build(store, profiles::field(), None).unwrap();

        work.store_tokens(&pair()).unwrap();
        let field_pair =
            TokenSet::new("field-access".to_string(), "field-refresh".to_string()).unwrap();
        field.store_tokens(&field_pair).unwrap();
        work.switch_profile(profiles::field()).unwrap();

        assert_eq!(core_token_answer(&work, OWN).unwrap_err(), ERR_NO_SESSION);
        assert_eq!(core_token_answer(&field, OWN).unwrap(), "field-access");
    }

    #[test]
    fn the_state_names_the_profile_it_serves() {
        let (work, _) = state_for(profiles::work());
        assert_eq!(work.profile().identifier, profiles::work().identifier);
        let (field, _) = state_for(profiles::field());
        assert_eq!(field.profile().identifier, profiles::field().identifier);
    }

    #[test]
    fn a_wrong_origin_is_refused_with_custody_untouched() {
        let (state, custody) = state_for(profiles::pro());
        state
            .store
            .store(state.profile.identifier, &pair())
            .unwrap();
        for refused in [
            "http://evil.example/",
            "tauri://evil.example/",
            "http://tauri.localhost:8080/",
            "file:///etc/passwd",
            "not a url",
        ] {
            assert_eq!(core_token_answer(&state, refused).unwrap_err(), ERR_ORIGIN);
            assert_eq!(
                clear_tokens_answer(&state, refused).unwrap_err(),
                ERR_ORIGIN
            );
        }
        assert_eq!(custody.snapshot_len(), 2, "a refused caller clears nothing");
    }

    #[test]
    fn an_override_origin_answers_and_an_unknown_still_refuses() {
        let url = parse_shell_url("http://127.0.0.1:8080/").unwrap();
        let custody = Arc::new(MemoryCustody::new());
        let state =
            TokenState::build(TokenStore::new(custody), profiles::work(), Some(&url)).unwrap();
        state
            .store
            .store(state.profile.identifier, &pair())
            .unwrap();
        assert_eq!(
            core_token_answer(&state, "http://127.0.0.1:8080/erp").unwrap(),
            ACCESS
        );
        assert_eq!(
            core_token_answer(&state, "http://127.0.0.1:8081/").unwrap_err(),
            ERR_ORIGIN
        );
    }

    #[test]
    fn an_unavailable_keychain_answers_its_code() {
        let custody = Arc::new(MemoryCustody::unavailable());
        let state = TokenState::build(TokenStore::new(custody), profiles::work(), None).unwrap();
        assert_eq!(core_token_answer(&state, OWN).unwrap_err(), ERR_CUSTODY);
        assert_eq!(clear_tokens_answer(&state, OWN).unwrap_err(), ERR_CUSTODY);
    }

    #[test]
    fn the_url_override_is_http_or_https_only() {
        assert!(parse_shell_url("http://127.0.0.1:8080").is_some());
        assert!(parse_shell_url(" https://desk.example.test/ ").is_some());
        assert!(parse_shell_url("file:///etc/passwd").is_none());
        assert!(parse_shell_url("gable://quotes/1").is_none());
        assert!(parse_shell_url("tauri://localhost").is_none());
        assert!(parse_shell_url("not a url").is_none());
    }

    #[test]
    fn the_token_path_holds_no_logging_or_printing() {
        // Nothing in the token path prints at all, so there is no log line
        // to leak. The one exception is the URL override warning, which
        // names the environment variable and never a value: the scan strips
        // that one call and holds every other print out of the file.
        let src = include_str!("shell.rs");
        let production = src.split("#[cfg(test)]").next().unwrap();
        assert_eq!(
            production.matches("eprintln!").count(),
            1,
            "the URL override warning is the only permitted print"
        );
        let scanned = production.replace("eprintln!", "");
        for banned in [
            "println!",
            "print!(",
            "eprint!(",
            "dbg!",
            "log::",
            "tracing::",
            "info!(",
            "warn!(",
            "debug!(",
            "error!(",
            "trace!(",
        ] {
            assert!(!scanned.contains(banned), "shell.rs contains {banned}");
        }
    }
}
