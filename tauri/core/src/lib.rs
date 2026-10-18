// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! Shared core of the Gable shells.
//!
//! The core keeps the sign in tokens out of the webview: the access and
//! refresh tokens live only in the OS keychain (`custody`, `tokens`), the
//! command that hands a token to the page answers only to the app's own
//! origin (`fence`), and the three shells differ only in what `profiles`
//! says about them (identifier, roles, start app, window, device grants).
//!
//! Nothing in this crate logs or prints: the token path has no line a log
//! could leak, and the tests hold that line.

pub mod custody;
pub mod fence;
pub mod profiles;
pub mod shell;
pub mod tokens;
