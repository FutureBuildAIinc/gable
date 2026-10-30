// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! Gable Work: the Tauri 2 profile for the office and counter roles (the
//! work shell).
//!
//! The window opens the profile's start app: the front door bundle embedded
//! at build time (`web/apps/front-door/dist`) until the work shell's own
//! bundle lands, or the page named by `GABLE_WORK_URL` when that is set (an
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

use std::sync::Arc;

use gable_shell_core::custody;
use gable_shell_core::fence::{BadOrigin, OriginFence};
use gable_shell_core::offline::OfflineCounter;
use gable_shell_core::profiles;
use gable_shell_core::shell::{self, TokenState};
use gable_shell_core::signin::UreqTransport;
use gable_shell_core::tokens::TokenStore;
use tauri::{Manager, Url, WebviewUrl, WebviewWindowBuilder};

/// Fallback environment variable naming the Gable to load before the register
/// is enrolled; once enrolled, the keychain's enrolled `gable_url` replaces it
/// (ADR 0011 section 5.2).
pub const URL_VAR: &str = "GABLE_WORK_URL";

/// The Tauri command wrappers. They live in their own module because the
/// command macro exports its hidden wrapper at the crate root, which would
/// collide with a command defined in the root module itself.
pub mod commands {
    use std::sync::Arc;

    use gable_shell_core::offline::{
        CartPreview, EnrolmentPublic, HttpOfflineServer, OfflineCounter, OfflineError,
        OfflineSnapshot, QueueStatusView, SignSaleRequest, SignedOfflineSale, SnapshotRefresh,
        SyncReport,
    };
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

    #[tauri::command]
    pub async fn offline_enrolment_status(
        window: tauri::WebviewWindow,
        counter: tauri::State<'_, Arc<OfflineCounter>>,
    ) -> Result<EnrolmentPublic, OfflineError> {
        let url = window.url().map_err(|_| OfflineError::origin_denied())?;
        let counter = counter.inner().clone();
        tauri::async_runtime::spawn_blocking(move || counter.enrolment_status(Some(url.as_str())))
            .await
            .map_err(|_| OfflineError::keychain_unavailable())?
    }

    #[tauri::command]
    pub async fn load_offline_snapshot(
        window: tauri::WebviewWindow,
        counter: tauri::State<'_, Arc<OfflineCounter>>,
    ) -> Result<OfflineSnapshot, OfflineError> {
        let url = window.url().map_err(|_| OfflineError::origin_denied())?;
        let counter = counter.inner().clone();
        tauri::async_runtime::spawn_blocking(move || counter.load_snapshot(Some(url.as_str())))
            .await
            .map_err(|_| OfflineError::keychain_unavailable())?
    }

    #[tauri::command]
    pub async fn sign_and_queue_offline_sale(
        window: tauri::WebviewWindow,
        counter: tauri::State<'_, Arc<OfflineCounter>>,
        request: SignSaleRequest,
    ) -> Result<SignedOfflineSale, OfflineError> {
        let url = window.url().map_err(|_| OfflineError::origin_denied())?;
        let counter = counter.inner().clone();
        tauri::async_runtime::spawn_blocking(move || {
            counter.sign_and_queue_sale(Some(url.as_str()), request)
        })
        .await
        .map_err(|_| OfflineError::keychain_unavailable())?
    }

    #[tauri::command]
    pub async fn offline_queue_status(
        window: tauri::WebviewWindow,
        counter: tauri::State<'_, Arc<OfflineCounter>>,
    ) -> Result<QueueStatusView, OfflineError> {
        let url = window.url().map_err(|_| OfflineError::origin_denied())?;
        let counter = counter.inner().clone();
        tauri::async_runtime::spawn_blocking(move || counter.queue_status(Some(url.as_str())))
            .await
            .map_err(|_| OfflineError::keychain_unavailable())?
    }

    /// The enrolled Gable as the core's server: its origin and the keychain's
    /// token, never anything the page names.
    fn enrolled_server(
        counter: &OfflineCounter,
        tokens: &TokenState,
    ) -> Result<HttpOfflineServer, OfflineError> {
        let url = counter
            .enrolled_gable_url()?
            .and_then(|u| tauri::Url::parse(&u).ok())
            .ok_or_else(OfflineError::re_enrol_required)?;
        Ok(HttpOfflineServer::new(&url, Arc::new(tokens.clone())))
    }

    /// The core fetches the register's snapshot from the enrolled Gable
    /// (ADR 0011 5.4), once its queue has drained.
    #[tauri::command]
    pub async fn refresh_offline_snapshot(
        window: tauri::WebviewWindow,
        counter: tauri::State<'_, Arc<OfflineCounter>>,
        tokens: tauri::State<'_, TokenState>,
    ) -> Result<SnapshotRefresh, OfflineError> {
        let url = window.url().map_err(|_| OfflineError::origin_denied())?;
        let counter = counter.inner().clone();
        let tokens = tokens.inner().clone();
        tauri::async_runtime::spawn_blocking(move || {
            let server = enrolled_server(&counter, &tokens)?;
            counter.refresh_snapshot(Some(url.as_str()), &server)
        })
        .await
        .map_err(|_| OfflineError::keychain_unavailable())?
    }

    /// The core prices the clerk's cart from its own snapshot copy (ADR 0011
    /// 5.3); nothing is written or signed.
    #[tauri::command]
    pub async fn price_offline_cart(
        window: tauri::WebviewWindow,
        counter: tauri::State<'_, Arc<OfflineCounter>>,
        request: SignSaleRequest,
    ) -> Result<CartPreview, OfflineError> {
        let url = window.url().map_err(|_| OfflineError::origin_denied())?;
        let counter = counter.inner().clone();
        tauri::async_runtime::spawn_blocking(move || {
            counter.price_cart(Some(url.as_str()), request)
        })
        .await
        .map_err(|_| OfflineError::keychain_unavailable())?
    }

    /// The core sends its queue to the enrolled Gable's sync and records the
    /// server's answer (ADR 0011 section 3).
    #[tauri::command]
    pub async fn sync_offline_queue(
        window: tauri::WebviewWindow,
        counter: tauri::State<'_, Arc<OfflineCounter>>,
        tokens: tauri::State<'_, TokenState>,
    ) -> Result<SyncReport, OfflineError> {
        let url = window.url().map_err(|_| OfflineError::origin_denied())?;
        let counter = counter.inner().clone();
        let tokens = tokens.inner().clone();
        tauri::async_runtime::spawn_blocking(move || {
            let server = enrolled_server(&counter, &tokens)?;
            counter.sync_queue(Some(url.as_str()), &server)
        })
        .await
        .map_err(|_| OfflineError::keychain_unavailable())?
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

/// The work shell's token state over the shared `custody` (the offline
/// counter reads the same keychain entry). Names the Gable server when a URL
/// is set, so the shell can sign in and rotate the token it holds.
fn token_state(
    custody: Arc<dyn custody::Custody>,
    profile: &'static profiles::Profile,
    override_url: Option<&Url>,
) -> Result<TokenState, BadOrigin> {
    let state = TokenState::build(TokenStore::new(custody), profile, override_url)?;
    Ok(match override_url {
        Some(url) => state.with_server(url, Arc::new(UreqTransport::new())),
        None => state,
    })
}

pub fn run() {
    let profile = profiles::work();
    let custody: Arc<dyn custody::Custody> = Arc::from(custody::platform_custody());
    let override_url = shell::enrolled_or_configured_url(&*custody, profile, URL_VAR);
    let state = token_state(custody.clone(), profile, override_url.as_ref())
        .expect("the app's own origins build a fence");

    tauri::Builder::default()
        .manage(state)
        .invoke_handler(tauri::generate_handler![
            commands::core_token,
            commands::clear_tokens,
            commands::begin_sign_in,
            commands::refresh_tokens,
            commands::offline_enrolment_status,
            commands::load_offline_snapshot,
            commands::sign_and_queue_offline_sale,
            commands::offline_queue_status,
            commands::refresh_offline_snapshot,
            commands::price_offline_cart,
            commands::sync_offline_queue
        ])
        .setup(move |app| {
            let data_dir = app
                .path()
                .app_data_dir()
                .unwrap_or_else(|_| std::env::temp_dir());
            let store_path = data_dir.join("offline-counter.sqlite");
            let offline_fence = override_url
                .as_ref()
                .and_then(|u| OriginFence::from_starts(&[u.as_str()]).ok());
            let counter = Arc::new(OfflineCounter::new(
                profile.identifier,
                custody.clone(),
                store_path,
                offline_fence,
            ));
            app.manage(counter);

            let url = match override_url {
                Some(url) => {
                    app.add_capability(shell::work_remote_capability_json(&url))?;
                    WebviewUrl::External(url)
                }
                None => WebviewUrl::App("index.html".into()),
            };
            WebviewWindowBuilder::new(app, "main", url)
                .title(profile.window.title)
                .inner_size(profile.window.width, profile.window.height)
                .build()?;
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running Gable Work");
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
        let state = TokenState::build(store.clone(), profiles::work(), None)
            .expect("a fence over the bundled origins");
        (state, store, custody)
    }

    // RULE (PR 246 read, finding P1): the work shell's state holds a sign in
    // client whenever a Gable URL is set, so the token it holds can be
    // rotated and the page can start the shell's sign in; with no URL there is
    // no server to name and no client.
    #[test]
    fn the_work_shell_state_has_a_client_exactly_when_a_url_is_set() {
        let url = super::Url::parse("https://gable.yard.internal:8443/").unwrap();
        let with = super::token_state(Arc::new(MemoryCustody::new()), profiles::work(), Some(&url))
            .expect("a fence over the bundled origins and the url");
        assert!(with.client().is_some(), "a url names the server");
        let without = super::token_state(Arc::new(MemoryCustody::new()), profiles::work(), None)
            .expect("a fence over the bundled origins");
        assert!(without.client().is_none(), "no url, no server");
    }

    fn profile_id() -> &'static str {
        profiles::work().identifier
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
            assert!(!perms.contains(&banned), "{banned} must not be granted");
        }
    }

    #[test]
    fn the_camera_usage_description_is_prepared_for_when_the_profile_is_packaged() {
        // A tablet on the Work profile can scan at the counter: the same
        // web API camera, no Tauri grant. This test checks only that the
        // usage description is prepared in the source file: the profile is
        // not packaged yet (bundle.active false in tauri.conf.json), so
        // nothing merges the plist into an app today, and the hand proof on
        // a device is the only proof until the packaging lands.
        let plist = include_str!("../Info.plist");
        assert!(plist.contains("NSCameraUsageDescription"));
    }

    #[test]
    fn the_conf_names_this_profile() {
        let conf: serde_json::Value =
            serde_json::from_str(TAURI_CONF).expect("tauri.conf.json parses");
        assert_eq!(conf["identifier"].as_str(), Some(profile_id()));
        assert_eq!(
            conf["productName"].as_str(),
            Some(profiles::work().product_name)
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
        let state =
            TokenState::build(TokenStore::new(unavailable), profiles::work(), None).unwrap();
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

    // RULE (ADR 0011 section 5.2 and section 7): the runtime capability built
    // for the configured Gable URL grants the queue commands and the two token
    // commands to that Gable's origin and `/pos` on the `main` window only,
    // with `local: false`, and carries no extra permissions.
    #[test]
    fn the_runtime_capability_for_gable_url_grants_queue_commands_to_origin_and_pos_on_main_only() {
        use tauri::ipc::RuntimeCapability;

        let url = shell::parse_shell_url("https://gable.yard.internal:8443/").unwrap();
        let cap_json = shell::work_remote_capability_json(&url);
        let _built = cap_json.clone().build();

        let cap: serde_json::Value =
            serde_json::from_str(&cap_json).expect("runtime capability JSON parses");
        assert_eq!(cap["local"].as_bool(), Some(false));
        assert_eq!(
            cap["windows"]
                .as_array()
                .unwrap()
                .iter()
                .map(|v| v.as_str().unwrap())
                .collect::<Vec<_>>(),
            vec!["main"]
        );
        let remote_urls: Vec<&str> = cap["remote"]["urls"]
            .as_array()
            .unwrap()
            .iter()
            .map(|v| v.as_str().unwrap())
            .collect();
        assert_eq!(
            remote_urls,
            vec![
                "https://gable.yard.internal:8443/",
                "https://gable.yard.internal:8443/pos",
                "https://gable.yard.internal:8443/pos/*",
            ]
        );
        let perms: Vec<&str> = cap["permissions"]
            .as_array()
            .unwrap()
            .iter()
            .map(|v| v.as_str().unwrap())
            .collect();
        assert_eq!(perms, shell::WORK_REMOTE_PERMISSIONS);
        for banned in [
            "sql:",
            "updater",
            "shell:",
            "fs:",
            "camera",
            "geolocation",
            "core:default",
        ] {
            assert!(!cap_json.contains(banned), "{banned} must not be granted");
        }
    }

    // RULE (PR 246 read, finding P1): the page the window loads from the
    // enrolled Gable can start the shell's sign in and rotate its token, so
    // the runtime capability grants those two commands by name.
    #[test]
    fn the_runtime_capability_grants_begin_sign_in_and_refresh_tokens() {
        let url = super::Url::parse("https://gable.yard.internal:8443/").unwrap();
        let cap: serde_json::Value =
            serde_json::from_str(&shell::work_remote_capability_json(&url)).unwrap();
        let perms: Vec<&str> = cap["permissions"]
            .as_array()
            .unwrap()
            .iter()
            .map(|v| v.as_str().unwrap())
            .collect();
        for wanted in ["allow-begin-sign-in", "allow-refresh-tokens"] {
            assert!(perms.contains(&wanted), "{wanted} must be granted");
        }
    }

    // RULE (ADR 0011 section 5.2 and section 7): the Gable URL comes from the
    // enrolled device's keychain setting, and the queue commands answer that
    // Gable's origin and refuse every other origin and the embedded bundle.
    #[test]
    fn the_enrolled_device_url_takes_precedence_and_queue_commands_enforce_the_origin_fence() {
        use gable_shell_core::fence::OriginFence;
        use gable_shell_core::offline::OfflineCounter;

        let custody = Arc::new(MemoryCustody::new());
        let db_path = std::env::temp_dir().join("gable-work-fence-test.sqlite");
        let gable_origin = "https://gable.yard.internal:8443";
        let fence = OriginFence::from_starts(&[gable_origin]).unwrap();
        let counter = OfflineCounter::new(profile_id(), custody.clone(), &db_path, Some(fence));

        counter
            .enrol_register(Some("https://gable.yard.internal:8443/pos"), "REG-01", 1)
            .expect("enrol succeeds from /pos on the loaded Gable origin");

        let resolved =
            shell::enrolled_or_configured_url(&*custody, profiles::work(), "UNSET_GABLE_VAR")
                .expect("enrolled URL resolves from keychain");
        assert_eq!(resolved.as_str(), "https://gable.yard.internal:8443/");

        // Queue status answers `/pos` on the Gable origin and refuses every other origin.
        let status = counter
            .queue_status(Some("https://gable.yard.internal:8443/pos"))
            .expect("answers loaded Gable origin");
        assert_eq!(status.register_id, "REG-01");

        for refused in [
            FOREIGN,
            OWN,
            "https://gable.yard.internal:9443/pos",
            "http://gable.yard.internal:8443/pos",
        ] {
            assert_eq!(
                counter.queue_status(Some(refused)).unwrap_err().code,
                "origin_denied",
                "{refused}"
            );
        }

        // Embedded bundle (no fence) refuses every caller.
        let embedded = OfflineCounter::new(profile_id(), custody, &db_path, None);
        assert_eq!(
            embedded
                .queue_status(Some("https://gable.yard.internal:8443/pos"))
                .unwrap_err()
                .code,
            "embedded_bundle_refused"
        );

        let _ = std::fs::remove_file(&db_path);
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

    #[test]
    fn run_builds_its_token_state_only_through_token_state() {
        // A state built straight from TokenState::build names no server, so
        // the shell could neither sign in nor rotate its token (PR 247's read,
        // P3). Only token_state() may build one, and run() must call it.
        let src = include_str!("lib.rs");
        let production = src.split("#[cfg(test)]").next().unwrap();
        let helper_start = production
            .find("fn token_state(")
            .expect("lib.rs defines token_state()");
        let helper_end = helper_start
            + production[helper_start..]
                .find("\n}\n")
                .expect("token_state() closes");
        let builds: Vec<usize> = production
            .match_indices("TokenState::build(")
            .map(|(at, _)| at)
            .collect();
        assert!(!builds.is_empty(), "token_state() builds the state");
        for at in builds {
            assert!(
                (helper_start..helper_end).contains(&at),
                "TokenState::build( outside token_state() in lib.rs"
            );
        }

        let run_start = production
            .find("pub fn run()")
            .expect("lib.rs defines run()");
        let run_end = run_start + production[run_start..].find("\n}\n").expect("run() closes");
        // The code only: a comment in run() that names the call proves nothing
        // (PR 246's final read, mutation M4).
        let run_code: String = production[run_start..run_end]
            .lines()
            .filter(|l| !l.trim_start().starts_with("//"))
            .collect::<Vec<_>>()
            .join("\n");
        assert!(
            !run_code.contains("TokenState::"),
            "run() builds no TokenState of its own"
        );
        // The exact call: run() handing token_state() no URL brings back a
        // state with no sign in client (PR 246's final read, mutation M2).
        assert!(
            run_code.contains("token_state(custody.clone(), profile, override_url.as_ref())"),
            "run() builds its state through token_state() with the Gable URL it resolved"
        );
    }
}
