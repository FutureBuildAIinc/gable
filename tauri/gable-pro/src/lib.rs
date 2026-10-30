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
//! The shell itself lives in the core (`gable_shell_core::shell`): the
//! managed token state, the URL override and the answers are one
//! implementation parameterised by the profile, and this crate owns only
//! the env var's name, the two Tauri command wrappers and the window. The
//! webview holds exactly the two commands, `core_token` and `clear_tokens`
//! (see `capabilities/default.json`), and both answer only to the app's own
//! origins, so a page the window was navigated to from anywhere else can
//! neither read a token nor clear custody. The tokens themselves never
//! leave the OS keychain: `core_token` hands the page the access token it
//! needs for the API's Authorization header, and the refresh token never
//! crosses the IPC at all. Nothing here logs or prints.

use gable_shell_core::profiles;
use gable_shell_core::shell::{self, TokenState};
use tauri::{WebviewUrl, WebviewWindowBuilder};

/// Environment variable naming the Gable to load instead of the bundle.
pub const URL_VAR: &str = "GABLE_PRO_URL";

/// The Tauri command wrappers. They live in their own module because the
/// command macro exports its hidden wrapper at the crate root, which would
/// collide with a command defined in the root module itself.
pub mod commands {
    use gable_shell_core::shell::{self, TokenState};

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
        state: tauri::State<'_, TokenState>,
    ) -> Result<AccessTokenAnswer, String> {
        let url = window.url().map_err(|_| shell::ERR_ORIGIN.to_string())?;
        let state = state.inner().clone();
        tauri::async_runtime::spawn_blocking(move || {
            shell::core_token_answer(&state, url.as_str())
                .map(|access_token| AccessTokenAnswer { access_token })
        })
        .await
        .map_err(|_| shell::ERR_CUSTODY.to_string())?
    }

    /// The fenced custody clear (sign out), on a blocking thread like the
    /// read.
    #[tauri::command]
    pub async fn clear_tokens(
        window: tauri::WebviewWindow,
        state: tauri::State<'_, TokenState>,
    ) -> Result<(), String> {
        let url = window.url().map_err(|_| shell::ERR_ORIGIN.to_string())?;
        let state = state.inner().clone();
        tauri::async_runtime::spawn_blocking(move || {
            shell::clear_tokens_answer(&state, url.as_str())
        })
        .await
        .map_err(|_| shell::ERR_CUSTODY.to_string())?
    }
    /// Starts a redirect sign in (google, microsoft or oidc): the system
    /// browser opens on the core's start route with a PKCE challenge and a
    /// loopback return target, and this call answers once the person has come
    /// back and the tokens are in the keychain. The wait is on a blocking
    /// thread, and the command answers only to the app's own origins.
    #[tauri::command]
    pub async fn begin_sign_in(
        window: tauri::WebviewWindow,
        state: tauri::State<'_, TokenState>,
        method: String,
    ) -> Result<(), String> {
        let url = window.url().map_err(|_| shell::ERR_ORIGIN.to_string())?;
        let state = state.inner().clone();
        tauri::async_runtime::spawn_blocking(move || {
            shell::begin_sign_in_answer(
                &state,
                url.as_str(),
                &method,
                &gable_shell_core::signin::open_in_browser,
                std::time::Duration::from_secs(300),
            )
        })
        .await
        .map_err(|_| shell::ERR_CUSTODY.to_string())?
    }

    /// Rotates the token pair and answers the new access token; the refresh
    /// token stays in the keychain.
    #[tauri::command]
    pub async fn refresh_tokens(
        window: tauri::WebviewWindow,
        state: tauri::State<'_, TokenState>,
    ) -> Result<AccessTokenAnswer, String> {
        let url = window.url().map_err(|_| shell::ERR_ORIGIN.to_string())?;
        let state = state.inner().clone();
        tauri::async_runtime::spawn_blocking(move || {
            shell::refresh_tokens_answer(&state, url.as_str())
                .map(|access_token| AccessTokenAnswer { access_token })
        })
        .await
        .map_err(|_| shell::ERR_CUSTODY.to_string())?
    }
}

pub fn run() {
    let profile = profiles::pro();
    let override_url = shell::configured_url(URL_VAR);
    let state = TokenState::for_profile(profile, override_url.as_ref())
        .expect("the app's own origins build a fence");

    tauri::Builder::default()
        .manage(state)
        .invoke_handler(tauri::generate_handler![
            commands::core_token,
            commands::clear_tokens,
            commands::begin_sign_in,
            commands::refresh_tokens
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
    use std::sync::Arc;

    use gable_shell_core::custody::MemoryCustody;
    use gable_shell_core::profiles;
    use gable_shell_core::shell::{self, TokenState};
    use gable_shell_core::tokens::{TokenSet, TokenStore};

    const CAPABILITY: &str = include_str!("../capabilities/default.json");
    const TAURI_CONF: &str = include_str!("../tauri.conf.json");

    const ACCESS: &str = "access-sentinel";
    const REFRESH: &str = "refresh-sentinel";
    const OWN: &str = "http://tauri.localhost/index.html";
    const FOREIGN: &str = "http://evil.example/index.html";

    fn pair() -> TokenSet {
        TokenSet::new(ACCESS.to_string(), REFRESH.to_string()).expect("a valid pair")
    }

    /// A state over the in memory fake, with the store handle the state was
    /// built over (the tests assert on what the custody was left holding;
    /// tests never touch a real keychain).
    fn state() -> (TokenState, TokenStore, Arc<MemoryCustody>) {
        let custody = Arc::new(MemoryCustody::new());
        let store = TokenStore::new(custody.clone());
        let state = TokenState::build(store.clone(), profiles::pro(), None)
            .expect("a fence over the bundled origins");
        (state, store, custody)
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
        assert_eq!(
            perms,
            [
                "allow-core-token",
                "allow-clear-tokens",
                "allow-begin-sign-in",
                "allow-refresh-tokens"
            ]
        );
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
        let (state, store, _) = state();
        store.store(profile_id(), &pair()).unwrap();
        let answer = shell::core_token_answer(&state, OWN).unwrap();
        assert_eq!(answer, ACCESS);
        assert_ne!(answer, REFRESH, "the refresh token never crosses the IPC");
    }

    #[test]
    fn a_wrong_origin_is_refused_and_custody_is_untouched() {
        let (state, store, _) = state();
        store.store(profile_id(), &pair()).unwrap();
        for refused in [
            FOREIGN,
            "tauri://evil.example/",
            "http://tauri.localhost:8080/",
            "file:///etc/passwd",
            "not a url",
        ] {
            assert_eq!(
                shell::core_token_answer(&state, refused).unwrap_err(),
                shell::ERR_ORIGIN,
                "{refused}"
            );
            assert_eq!(
                shell::clear_tokens_answer(&state, refused).unwrap_err(),
                shell::ERR_ORIGIN
            );
        }
        assert!(
            store.read(profile_id()).unwrap().is_some(),
            "a refused caller reads nothing and clears nothing"
        );
    }

    #[test]
    fn clear_tokens_clears_custody_for_the_app_origin() {
        let (state, store, custody) = state();
        store.store(profile_id(), &pair()).unwrap();
        shell::clear_tokens_answer(&state, OWN).unwrap();
        assert_eq!(store.read(profile_id()), Ok(None));
        assert_eq!(custody.snapshot_len(), 0);
    }

    #[test]
    fn an_empty_store_answers_no_session_and_an_unavailable_keychain_its_code() {
        let (state, _, _) = state();
        assert_eq!(
            shell::core_token_answer(&state, OWN).unwrap_err(),
            shell::ERR_NO_SESSION
        );
        let unavailable = Arc::new(MemoryCustody::unavailable());
        let state = TokenState::build(TokenStore::new(unavailable), profiles::pro(), None).unwrap();
        assert_eq!(
            shell::core_token_answer(&state, OWN).unwrap_err(),
            shell::ERR_CUSTODY
        );
        assert_eq!(
            shell::clear_tokens_answer(&state, OWN).unwrap_err(),
            shell::ERR_CUSTODY
        );
    }

    #[test]
    fn the_url_override_is_http_or_https_only() {
        assert!(shell::parse_shell_url("http://127.0.0.1:8080").is_some());
        assert!(shell::parse_shell_url(" https://desk.example.test/ ").is_some());
        assert!(shell::parse_shell_url("file:///etc/passwd").is_none());
        assert!(shell::parse_shell_url("gable://quotes/1").is_none());
        assert!(shell::parse_shell_url("not a url").is_none());
    }

    #[test]
    fn the_token_path_holds_no_logging_or_printing() {
        // This crate holds no print at all: the shell answers and the one
        // URL override warning they carry live in the core, whose own scan
        // holds that line.
        let src = include_str!("lib.rs");
        let production = src.split("#[cfg(test)]").next().unwrap();
        for banned in [
            "println!",
            "eprintln!",
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
            assert!(!production.contains(banned), "lib.rs contains {banned}");
        }
    }
}
