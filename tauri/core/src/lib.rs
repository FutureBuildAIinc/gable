// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! Shared core of the Gable shells.
//!
//! Once sign in writes it, the core keeps the sign in tokens out of the
//! webview as far as the shell can hold them: `custody` and `tokens` store the
//! access and refresh tokens in the OS keychain, `core_token` hands only the
//! access token to the app's own origin (`fence`) while the refresh token
//! stays in the keychain and never crosses to the page, and the three shells
//! differ only in what `profiles` says about them (identifier, roles, start
//! app, window, device grants). The web sign in flow uses `sessionStorage`
//! today and connects to this store when native token custody is wired.
//!
//! Nothing in this crate logs or prints: the token path has no line a log
//! could leak, and the tests hold that line.

pub mod custody;
pub mod fence;
pub mod profiles;
pub mod shell;
pub mod signin;
pub mod tokens;
