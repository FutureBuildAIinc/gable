// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! Gable Pro: the Tauri 2 profile for the contractors' frame (the
//! pro shell).
//!
//! The window opens the profile's start app: the front door bundle embedded
//! at build time (`web/apps/front-door/dist`) until the pro shell's own
//! bundle lands, or the page named by `GABLE_PRO_URL` when that is set (an
//! http or https URL of a running Gable).
//!
//! The webview holds exactly two commands, `core_token` and `clear_tokens`
//! (see `capabilities/default.json`), and both answer only to the app's own
//! origins (`gable_shell_core::fence`), so a page the window was navigated
//! to from anywhere else can neither read a token nor clear custody. The
//! tokens themselves never leave the OS keychain: `core_token` hands the
//! page the access token it needs for the API's Authorization header, and
//! the refresh token never crosses the IPC at all. Nothing here logs or
//! prints: the token path has no line a log could leak.

use std::sync::Arc;

use gable_shell_core::custody::{self, Custody};
use gable_shell_core::fence::{BadOrigin, OriginFence};
use gable_shell_core::profiles;
use gable_shell_core::tokens::TokenStore;
use tauri::{Url, WebviewUrl, WebviewWindowBuilder};

/// Environment variable naming the Gable to load instead of the bundle.
pub const URL_VAR: &str = "GABLE_PRO_URL";

/// The origins the app's own commands always answer to: the bundled shell's
/// Tauri origins. The URL override's origin joins them at startup.
const BUNDLED_ORIGINS: [&str; 3] = [
    "http://tauri.localhost",
    "https://tauri.localhost",
    "tauri://localhost",
];

/// The error words the commands answer with. A code only: no service, no
/// account, no value.
const ERR_ORIGIN: &str = "forbidden_origin";
const ERR_NO_SESSION: &str = "no_session";
const ERR_CUSTODY: &str = "keychain_unavailable";

/// Reads and validates the configured shell URL: http or https only.
pub fn parse_shell_url(raw: &str) -> Option<Url> {
    let url = Url::parse(raw.trim()).ok()?;
    matches!(url.scheme(), "http" | "https").then_some(url)
}

fn configured_url() -> Option<Url> {
    let raw = std::env::var(URL_VAR)
        .ok()
        .filter(|v| !v.trim().is_empty())?;
    let parsed = parse_shell_url(&raw);
    if parsed.is_none() {
        eprintln!("gable-pro: {URL_VAR} is not an http or https URL; loading the bundle");
    }
    parsed
}

/// The managed state behind the two token commands.
#[derive(Clone)]
pub struct TokenState {
    store: TokenStore,
    fence: OriginFence,
}

impl TokenState {
    /// The real state: the platform keychain, and the app's own origins (the
    /// bundled ones plus the override's). A fence that cannot name its own
    /// origin refuses whole, and the shell runs no token command.
    pub fn for_window(override_url: Option<&Url>) -> Result<Self, BadOrigin> {
        let custody: Arc<dyn Custody> = Arc::from(custody::platform_custody());
        Self::build(TokenStore::new(custody), override_url)
    }

    /// A state over any custody (tests build it over the in memory fake;
    /// tests never touch a real keychain).
    fn build(store: TokenStore, override_url: Option<&Url>) -> Result<Self, BadOrigin> {
        let mut starts: Vec<&str> = BUNDLED_ORIGINS.to_vec();
        if let Some(url) = override_url {
            starts.push(url.as_str());
        }
        Ok(Self {
            store,
            fence: OriginFence::from_starts(&starts)?,
        })
    }
}

/// The access token for the app's own origin, or the refusal word. The
/// refresh token never leaves the keychain.
pub fn core_token_answer(state: &TokenState, caller_url: &str) -> Result<String, String> {
    if !state.fence.allows(caller_url) {
        return Err(ERR_ORIGIN.to_string());
    }
    match state.store.read(profiles::pro().identifier) {
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
        .clear(profiles::pro().identifier)
        .map_err(|e| e.code().to_string())
}

/// The Tauri command wrappers. They live in their own module because the
/// command macro exports its hidden wrapper at the crate root, which would
/// collide with a command defined in the root module itself.
pub mod commands {
    /// `core_token`'s answer shape: the access token, and nothing else.
    #[derive(serde::Serialize)]
    pub struct AccessTokenAnswer {
        pub access_token: String,
    }

    /// The fenced token read. The custody call sits on a blocking thread: a
    /// keychain unlock prompt must never sit on an async worker.
    #[tauri::command]
    pub async fn core_token(
        window: tauri::WebviewWindow,
        state: tauri::State<'_, super::TokenState>,
    ) -> Result<AccessTokenAnswer, String> {
        let url = window.url().map_err(|_| super::ERR_ORIGIN.to_string())?;
        let state = state.inner().clone();
        tauri::async_runtime::spawn_blocking(move || {
            super::core_token_answer(&state, url.as_str())
                .map(|access_token| AccessTokenAnswer { access_token })
        })
        .await
        .map_err(|_| super::ERR_CUSTODY.to_string())?
    }

    /// The fenced custody clear (sign out), on a blocking thread like the
    /// read.
    #[tauri::command]
    pub async fn clear_tokens(
        window: tauri::WebviewWindow,
        state: tauri::State<'_, super::TokenState>,
    ) -> Result<(), String> {
        let url = window.url().map_err(|_| super::ERR_ORIGIN.to_string())?;
        let state = state.inner().clone();
        tauri::async_runtime::spawn_blocking(move || {
            super::clear_tokens_answer(&state, url.as_str())
        })
        .await
        .map_err(|_| super::ERR_CUSTODY.to_string())?
    }
}

pub fn run() {
    let profile = profiles::pro();
    let override_url = configured_url();
    let state =
        TokenState::for_window(override_url.as_ref()).expect("the app's own origins build a fence");

    tauri::Builder::default()
        .manage(state)
        .invoke_handler(tauri::generate_handler![
            commands::core_token,
            commands::clear_tokens
        ])
        .setup(move |app| {
            let url = match override_url {
                Some(url) => WebviewUrl::External(url),
                None => WebviewUrl::App("index.html".into()),
            };
            WebviewWindowBuilder::new(app, "main", url)
                .title(profile.window.title)
                .inner_size(profile.window.width, profile.window.height)
                .build()?;
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running Gable Pro");
}

#[cfg(test)]
mod tests {
    use super::*;
    use gable_shell_core::custody::MemoryCustody;
    use gable_shell_core::tokens::TokenSet;

    const CAPABILITY: &str = include_str!("../capabilities/default.json");
    const TAURI_CONF: &str = include_str!("../tauri.conf.json");

    const ACCESS: &str = "access-sentinel";
    const REFRESH: &str = "refresh-sentinel";
    const OWN: &str = "http://tauri.localhost/index.html";
    const FOREIGN: &str = "http://evil.example/index.html";

    fn pair() -> TokenSet {
        TokenSet::new(ACCESS.to_string(), REFRESH.to_string()).expect("a valid pair")
    }

    /// A state over the in memory fake, plus the fake (the tests assert on
    /// what the custody was left holding; tests never touch a real keychain).
    fn state() -> (TokenState, Arc<MemoryCustody>) {
        let custody = Arc::new(MemoryCustody::new());
        let state = TokenState::build(TokenStore::new(custody.clone()), None)
            .expect("a fence over the bundled origins");
        (state, custody)
    }

    fn profile_id() -> &'static str {
        profiles::pro().identifier
    }

    #[test]
    fn the_capability_allows_exactly_the_two_token_commands_and_nothing_else() {
        let cap: serde_json::Value =
            serde_json::from_str(CAPABILITY).expect("the capability parses");
        let perms: Vec<&str> = cap["permissions"]
            .as_array()
            .expect("a permissions list")
            .iter()
            .map(|p| p.as_str().expect("permission names are strings"))
            .collect();
        assert_eq!(perms, ["allow-core-token", "allow-clear-tokens"]);
        // Least privilege: no open core grant, no plugin, no device or shell
        // permission rides along. The device grants are the field profile's.
        for banned in [
            "sql:",
            "updater",
            "shell:",
            "fs:",
            "camera",
            "geolocation",
            "core:default",
        ] {
            assert!(!CAPABILITY.contains(banned), "{banned} must not be granted");
        }
    }

    #[test]
    fn the_conf_names_this_profile() {
        let conf: serde_json::Value =
            serde_json::from_str(TAURI_CONF).expect("tauri.conf.json parses");
        assert_eq!(conf["identifier"].as_str(), Some(profile_id()));
        assert_eq!(
            conf["productName"].as_str(),
            Some(profiles::pro().product_name)
        );
    }

    #[test]
    fn the_token_command_answers_its_own_origin_with_the_access_token_only() {
        let (state, _) = state();
        state.store.store(profile_id(), &pair()).unwrap();
        let answer = core_token_answer(&state, OWN).unwrap();
        assert_eq!(answer, ACCESS);
        assert_ne!(answer, REFRESH, "the refresh token never crosses the IPC");
    }

    #[test]
    fn a_wrong_origin_is_refused_and_custody_is_untouched() {
        let (state, _) = state();
        state.store.store(profile_id(), &pair()).unwrap();
        for refused in [
            FOREIGN,
            "tauri://evil.example/",
            "http://tauri.localhost:8080/",
            "file:///etc/passwd",
            "not a url",
        ] {
            assert_eq!(
                core_token_answer(&state, refused).unwrap_err(),
                ERR_ORIGIN,
                "{refused}"
            );
            assert_eq!(
                clear_tokens_answer(&state, refused).unwrap_err(),
                ERR_ORIGIN
            );
        }
        assert!(
            state.store.read(profile_id()).unwrap().is_some(),
            "a refused caller reads nothing and clears nothing"
        );
    }

    #[test]
    fn an_override_origin_answers_and_an_unknown_still_refuses() {
        let url = parse_shell_url("http://127.0.0.1:8080/").unwrap();
        let custody = Arc::new(MemoryCustody::new());
        let state = TokenState::build(TokenStore::new(custody), Some(&url)).unwrap();
        state.store.store(profile_id(), &pair()).unwrap();
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
    fn clear_tokens_clears_custody_for_the_app_origin() {
        let (state, custody) = state();
        state.store.store(profile_id(), &pair()).unwrap();
        clear_tokens_answer(&state, OWN).unwrap();
        assert_eq!(state.store.read(profile_id()), Ok(None));
        assert_eq!(custody.snapshot_len(), 0);
    }

    #[test]
    fn an_empty_store_answers_no_session_and_an_unavailable_keychain_its_code() {
        let (state, _) = state();
        assert_eq!(core_token_answer(&state, OWN).unwrap_err(), ERR_NO_SESSION);
        let unavailable = Arc::new(MemoryCustody::unavailable());
        let state = TokenState::build(TokenStore::new(unavailable), None).unwrap();
        assert_eq!(core_token_answer(&state, OWN).unwrap_err(), ERR_CUSTODY);
        assert_eq!(clear_tokens_answer(&state, OWN).unwrap_err(), ERR_CUSTODY);
    }

    #[test]
    fn the_url_override_is_http_or_https_only() {
        assert!(parse_shell_url("http://127.0.0.1:8080").is_some());
        assert!(parse_shell_url(" https://desk.example.test/ ").is_some());
        assert!(parse_shell_url("file:///etc/passwd").is_none());
        assert!(parse_shell_url("gable://quotes/1").is_none());
        assert!(parse_shell_url("not a url").is_none());
    }

    #[test]
    fn the_token_path_holds_no_logging_or_printing() {
        // Nothing in the token path prints at all, so there is no log line
        // to leak. The one exception is the URL override warning at the top
        // of the file, which names the environment variable and never a
        // value; the test holds both halves of that bargain.
        let src = include_str!("lib.rs");
        let production = src.split("#[cfg(test)]").next().unwrap();
        assert_eq!(
            production.matches("eprintln!").count(),
            1,
            "the URL override warning is the only permitted print"
        );
        // The one permitted call is stripped before the scan: eprintln!
        // contains println! as a substring, and the scan means the call,
        // not the word.
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
            assert!(!scanned.contains(banned), "lib.rs contains {banned}");
        }
        // The token path itself (state, answers, commands) holds no print of
        // any kind: the scan cuts at the state and checks to the tests.
        let token_path = &production[production.find("pub struct TokenState").unwrap()..];
        for banned in ["println!", "eprintln!", "print!", "dbg!"] {
            assert!(
                !token_path.contains(banned),
                "the token path prints {banned}"
            );
        }
    }
}
