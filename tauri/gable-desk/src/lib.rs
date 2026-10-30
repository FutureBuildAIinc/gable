// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! Gable Desk: a Tauri 2 shell around the front door.
//!
//! The window loads the front door bundle embedded at build time
//! (`web/apps/front-door/dist`), or the page named by `GABLE_DESK_URL` when
//! that is set (an http or https URL of a running Gable). A `gable://` link
//! opens a record route: `gable://quotes/<id>` opens `/quotes/<id>`.
//!
//! The webview holds exactly four commands (see `capabilities/default.json`):
//! the two token commands and the two sign in commands, each answering only
//! to the app's own origins. Sign in is the redirect methods through the
//! system browser: `begin_sign_in` opens the core's start route with a PKCE
//! challenge and the registered `gable://auth/callback` return target, and
//! the deep link that comes back carries a one time code that this crate
//! redeems with the verifier it kept (never a route: see `open_deep_link`).
//! Everything else native happens here.
//!
//! Smoke mode (`GABLE_DESK_SMOKE=1`) is for CI: the app logs the page load and
//! the document title, then exits cleanly. A watchdog exits non-zero if the
//! page never finishes loading.

use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use tauri::webview::PageLoadEvent;
use tauri::{AppHandle, Manager, Url, WebviewUrl, WebviewWindowBuilder};
use tauri_plugin_deep_link::DeepLinkExt;

use gable_shell_core::profiles;
use gable_shell_core::shell::{self, TokenState};
use gable_shell_core::signin::DeepLinkSignIn;

/// The deep link scheme, also declared in `tauri.conf.json`.
pub const SCHEME: &str = "gable";
/// Environment variable naming the desk to load instead of the bundle.
pub const URL_VAR: &str = "GABLE_DESK_URL";
/// Environment variable that turns on smoke mode.
pub const SMOKE_VAR: &str = "GABLE_DESK_SMOKE";

const MAX_SEGMENTS: usize = 8;
const MAX_SEGMENT_LEN: usize = 128;
const SMOKE_SETTLE: Duration = Duration::from_secs(2);
const SMOKE_DEADLINE: Duration = Duration::from_secs(90);

/// Turns a `gable://` link into an in-app route, or `None` when the link is
/// not one we open. Only plain path segments pass: no query, no fragment, no
/// dot segments, no empty segments, so a link can never steer the window
/// outside the desk's own routes.
pub fn route_from_deep_link(raw: &str) -> Option<String> {
    let rest = raw.strip_prefix("gable://")?;
    let rest = rest.trim_start_matches('/').trim_end_matches('/');
    let segments: Vec<&str> = rest.split('/').collect();
    if segments.len() > MAX_SEGMENTS
        || !segments.iter().all(|s| valid_segment(s))
        || segments[0] == "auth"
    {
        return None;
    }
    Some(format!("/{}", segments.join("/")))
}

fn valid_segment(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= MAX_SEGMENT_LEN
        && s != "."
        && s != ".."
        && s.chars()
            .all(|c| c.is_ascii_alphanumeric() || matches!(c, '-' | '_' | '.' | '~'))
}

/// Reads and validates the configured desk URL: http or https only.
pub fn parse_desk_url(raw: &str) -> Option<Url> {
    let url = Url::parse(raw.trim()).ok()?;
    matches!(url.scheme(), "http" | "https").then_some(url)
}

fn configured_url() -> Option<Url> {
    let raw = std::env::var(URL_VAR)
        .ok()
        .filter(|v| !v.trim().is_empty())?;
    let parsed = parse_desk_url(&raw);
    if parsed.is_none() {
        eprintln!(
            "gable-desk: {URL_VAR} is not an http or https URL; loading the bundled front door"
        );
    }
    parsed
}

fn smoke_mode() -> bool {
    std::env::var(SMOKE_VAR).is_ok_and(|v| v == "1")
}

/// The Tauri command wrappers, in their own module because the command macro
/// exports its hidden wrapper at the crate root.
pub mod commands {
    use std::sync::Arc;

    use gable_shell_core::shell::{self, TokenState};
    use gable_shell_core::signin::{self, DeepLinkSignIn};

    /// The access token and nothing else.
    #[derive(serde::Serialize)]
    pub struct AccessTokenAnswer {
        pub access_token: String,
    }

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

    /// Opens the system browser on the core's start route for a redirect
    /// method and answers at once; the deep link that comes back finishes
    /// the sign in.
    #[tauri::command]
    pub async fn begin_sign_in(
        window: tauri::WebviewWindow,
        state: tauri::State<'_, TokenState>,
        pending: tauri::State<'_, Arc<DeepLinkSignIn>>,
        method: String,
    ) -> Result<(), String> {
        let url = window.url().map_err(|_| shell::ERR_ORIGIN.to_string())?;
        let state = state.inner().clone();
        let pending = pending.inner().clone();
        tauri::async_runtime::spawn_blocking(move || {
            shell::begin_deep_link_sign_in_answer(
                &state,
                &pending,
                url.as_str(),
                &method,
                &signin::open_in_browser,
            )
        })
        .await
        .map_err(|_| shell::ERR_CUSTODY.to_string())?
    }

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

/// True for a `gable://auth/...` link: sign in's own, never a route.
pub fn is_auth_link(raw: &str) -> bool {
    raw.strip_prefix("gable://").is_some_and(|rest| {
        rest == "auth" || rest.starts_with("auth/") || rest.starts_with("auth?")
    })
}

/// Completes a sign in from the deep link the OS delivered. The redemption is
/// a network call, so it runs on its own thread; when it ends the window
/// reloads so the page reads the new session. A link that is not a pending
/// sign in's callback spends nothing.
fn complete_sign_in(app: &AppHandle, raw: &str) {
    let state = app.state::<TokenState>().inner().clone();
    let pending = app
        .state::<std::sync::Arc<DeepLinkSignIn>>()
        .inner()
        .clone();
    let app = app.clone();
    let raw = raw.to_string();
    std::thread::spawn(move || {
        let _ = shell::complete_deep_link_sign_in(&state, &pending, &raw);
        if let Some(window) = app.get_webview_window("main") {
            if let Ok(current) = window.url() {
                let _ = window.navigate(current);
            }
            let _ = window.set_focus();
        }
    });
}

/// Opens the route a deep link names in the main window.
fn open_deep_link(app: &AppHandle, raw: &str) {
    // The sign in callback carries a one time code: it is handed to the
    // sign in and never read as a route, so the code goes nowhere else.
    if is_auth_link(raw) {
        complete_sign_in(app, raw);
        return;
    }
    let Some(route) = route_from_deep_link(raw) else {
        eprintln!("gable-desk: ignoring deep link that is not a record route");
        return;
    };
    let Some(window) = app.get_webview_window("main") else {
        return;
    };
    let current = match window.url() {
        Ok(url) => url,
        Err(e) => {
            eprintln!("gable-desk: could not read the window url: {e}");
            return;
        }
    };
    let Ok(next) = current.join(&route) else {
        eprintln!("gable-desk: could not resolve route={route}");
        return;
    };
    println!("gable-desk: deep link opens route={route}");
    if let Err(e) = window.navigate(next) {
        eprintln!("gable-desk: navigation failed: {e}");
    }
    let _ = window.set_focus();
}

pub fn run() {
    let smoke = smoke_mode();
    let override_url = configured_url();
    let token_state = TokenState::for_profile(profiles::desk(), override_url.as_ref())
        .expect("the app's own origins build a fence");

    tauri::Builder::default()
        .manage(token_state)
        .manage(std::sync::Arc::new(DeepLinkSignIn::default()))
        .invoke_handler(tauri::generate_handler![
            commands::core_token,
            commands::clear_tokens,
            commands::begin_sign_in,
            commands::refresh_tokens
        ])
        // Registered first, as the plugin requires. A second launch (a deep
        // link on Linux and Windows starts a new process) forwards its
        // arguments here and the deep link plugin turns them into an event.
        .plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| {
            if let Some(window) = app.get_webview_window("main") {
                let _ = window.set_focus();
            }
        }))
        .plugin(tauri_plugin_deep_link::init())
        .setup(move |app| {
            let url = match override_url {
                Some(url) => WebviewUrl::External(url),
                None => WebviewUrl::App("index.html".into()),
            };
            let loaded = std::sync::Arc::new(AtomicBool::new(false));

            let handle = app.handle().clone();
            WebviewWindowBuilder::new(app, "main", url)
                .title("Gable Desk")
                .inner_size(1280.0, 800.0)
                .on_document_title_changed(|_, title| {
                    println!("gable-desk: document title={title}");
                })
                .on_page_load(move |_, payload| {
                    if payload.event() == PageLoadEvent::Finished {
                        println!("gable-desk: page loaded url={}", payload.url());
                        if smoke && !loaded.swap(true, Ordering::SeqCst) {
                            let app = handle.clone();
                            std::thread::spawn(move || {
                                std::thread::sleep(SMOKE_SETTLE);
                                println!("gable-desk: smoke ok");
                                app.exit(0);
                            });
                        }
                    }
                })
                .build()?;

            // Linux and Windows only learn the scheme from an installed
            // package; register at runtime so a dev or unpacked run works.
            #[cfg(any(target_os = "linux", windows))]
            {
                if let Err(e) = app.deep_link().register_all() {
                    eprintln!("gable-desk: could not register the {SCHEME}:// scheme: {e}");
                }
            }

            // A link that launched this process, then links while it runs.
            if let Ok(Some(urls)) = app.deep_link().get_current() {
                for url in urls {
                    open_deep_link(app.handle(), url.as_str());
                }
            }
            let handle = app.handle().clone();
            app.deep_link().on_open_url(move |event| {
                for url in event.urls() {
                    open_deep_link(&handle, url.as_str());
                }
            });

            if smoke {
                let handle = app.handle().clone();
                std::thread::spawn(move || {
                    std::thread::sleep(SMOKE_DEADLINE);
                    eprintln!("gable-desk: smoke failed: the page did not finish loading");
                    handle.exit(3);
                });
            }
            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while running Gable Desk");
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_record_link_opens_its_route() {
        assert_eq!(
            route_from_deep_link("gable://quotes/3f2b9c1e-0000-4000-8000-000000000001"),
            Some("/quotes/3f2b9c1e-0000-4000-8000-000000000001".to_string())
        );
        assert_eq!(
            route_from_deep_link("gable://quotes"),
            Some("/quotes".to_string())
        );
        assert_eq!(
            route_from_deep_link("gable:///orders/42/"),
            Some("/orders/42".to_string())
        );
    }

    #[test]
    fn anything_but_plain_segments_is_refused() {
        for bad in [
            "https://quotes/1",
            "gable://",
            "gable:///",
            "gable://quotes//1",
            "gable://quotes/../admin",
            "gable://quotes/./1",
            "gable://quotes/1?next=https://evil.example",
            "gable://quotes/1#frag",
            "gable://quotes/%2e%2e",
            "gable://quotes/a b",
            "gable://user@host/1",
            "javascript:alert(1)",
        ] {
            assert_eq!(route_from_deep_link(bad), None, "{bad}");
        }
        let long = format!("gable://quotes/{}", "a".repeat(MAX_SEGMENT_LEN + 1));
        assert_eq!(route_from_deep_link(&long), None);
        let deep = format!("gable://{}", vec!["a"; MAX_SEGMENTS + 1].join("/"));
        assert_eq!(route_from_deep_link(&deep), None);
    }

    #[test]
    fn an_auth_link_is_sign_ins_and_never_a_route() {
        for auth in [
            "gable://auth/callback?code=abc",
            "gable://auth/callback",
            "gable://auth",
            "gable://auth/anything",
        ] {
            assert!(is_auth_link(auth), "{auth}");
            assert_eq!(
                route_from_deep_link(auth),
                None,
                "{auth} must never open a route"
            );
        }
        for route in [
            "gable://quotes/1",
            "gable://authors/1",
            "gable://orders/auth",
            "https://auth/x",
        ] {
            assert!(!is_auth_link(route), "{route}");
        }
        assert_eq!(
            route_from_deep_link("gable://orders/auth"),
            Some("/orders/auth".to_string())
        );
    }

    const CAPABILITY: &str = include_str!("../capabilities/default.json");

    #[test]
    fn the_capability_allows_exactly_the_four_commands_by_name() {
        let cap: serde_json::Value = serde_json::from_str(CAPABILITY).unwrap();
        let perms: Vec<&str> = cap["permissions"]
            .as_array()
            .unwrap()
            .iter()
            .map(|p| p.as_str().unwrap())
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
        for banned in [
            "shell:",
            "fs:",
            "sql:",
            "updater",
            "core:default",
            "deep-link:",
        ] {
            assert!(
                !CAPABILITY.contains(&format!("\"{banned}")),
                "{banned} must not be granted"
            );
        }
    }

    #[test]
    fn the_sign_in_path_holds_no_logging_of_a_link() {
        // The link carries a one time code; the code handler prints nothing.
        let src = include_str!("lib.rs");
        let production = src.split("#[cfg(test)]").next().unwrap();
        let handler = production
            .split("fn complete_sign_in")
            .nth(1)
            .and_then(|rest| rest.split("/// Opens the route").next())
            .unwrap();
        for banned in ["println!", "eprintln!", "dbg!"] {
            assert!(
                !handler.contains(banned),
                "complete_sign_in contains {banned}"
            );
        }
    }

    #[test]
    fn the_desk_url_is_http_or_https_only() {
        assert!(parse_desk_url("http://localhost:8080").is_some());
        assert!(parse_desk_url(" https://desk.example.test/ ").is_some());
        assert!(parse_desk_url("file:///etc/passwd").is_none());
        assert!(parse_desk_url("gable://quotes/1").is_none());
        assert!(parse_desk_url("not a url").is_none());
    }
}
