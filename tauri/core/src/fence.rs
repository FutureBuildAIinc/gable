// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! The fence around the token commands: a command that hands out a token or
//! clears custody answers only to the app's own origin.
//!
//! The fence holds the origins the app itself loads (the bundled shell
//! origin, and the origin of a running Gable when the build's URL override
//! names one) and answers a caller URL only when that URL's origin is one of
//! them, exactly. A page the window was navigated to from anywhere else, a
//! lookalike host, another port, another scheme: all refused, with the
//! refusal carrying nothing but the word.

use url::Url;

/// The schemes an app origin can carry: the bundled shell serves from the
/// Tauri origin (http on Linux and Windows, the tauri scheme on macOS) and
/// a URL override names a running Gable over http or https.
const ALLOWED_SCHEMES: [&str; 3] = ["http", "https", "tauri"];

/// An origin the fence would never allow. It carries nothing.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct BadOrigin;

/// The origin of a caller URL, or `None` when the URL is not a shape the
/// fence could ever allow. The origin is scheme, host and port, lowercased,
/// with the port kept only when it is not the scheme's default; the path,
/// query and fragment never take part.
fn origin_of(raw: &str) -> Option<String> {
    let url = Url::parse(raw.trim()).ok()?;
    if !ALLOWED_SCHEMES.contains(&url.scheme()) {
        return None;
    }
    let host = url.host_str()?;
    if url.username().is_empty() && url.password().is_none() {
        // The url crate folds a scheme's default port away at parse time,
        // so port() is Some only when an explicit non default port is
        // present, and that origin stays its own.
        let port = match url.port() {
            None => String::new(),
            Some(p) => format!(":{p}"),
        };
        return Some(format!("{}://{host}{port}", url.scheme()));
    }
    None
}

/// The set of origins the app's own commands answer to.
#[derive(Clone, Debug)]
pub struct OriginFence {
    origins: Vec<String>,
}

impl OriginFence {
    /// A fence from the origins the app loads: each value must be a URL
    /// whose origin the fence could allow (http, https or the Tauri bundled
    /// schemes, a host, no credentials). An empty list, or any bad value,
    /// refuses the fence whole: a shell that cannot name its own origin runs
    /// no token command.
    pub fn from_starts<S: AsRef<str>>(starts: &[S]) -> Result<Self, BadOrigin> {
        if starts.is_empty() {
            return Err(BadOrigin);
        }
        let mut origins = Vec::with_capacity(starts.len());
        for start in starts {
            origins.push(origin_of(start.as_ref()).ok_or(BadOrigin)?);
        }
        origins.sort();
        origins.dedup();
        Ok(Self { origins })
    }

    /// The fence's origins, for the profiles' own tests.
    pub fn origins(&self) -> &[String] {
        &self.origins
    }

    /// Whether the caller URL sits on one of the app's own origins.
    pub fn allows(&self, url: &str) -> bool {
        match origin_of(url) {
            Some(origin) => self.origins.binary_search(&origin).is_ok(),
            None => false,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fence() -> OriginFence {
        OriginFence::from_starts(&["http://tauri.localhost", "tauri://localhost"])
            .expect("a valid fence")
    }

    #[test]
    fn the_bundled_origins_are_allowed_whatever_path_the_page_sits_at() {
        let f = fence();
        assert!(f.allows("http://tauri.localhost/"));
        assert!(f.allows("http://tauri.localhost/index.html"));
        assert!(f.allows("http://tauri.localhost/work-shell?role=owner"));
        assert!(f.allows("tauri://localhost/"));
        assert!(f.allows("tauri://localhost/index.html"));
    }

    #[test]
    fn a_foreign_origin_is_refused() {
        let f = fence();
        for bad in [
            "http://evil.example/",
            "https://tauri.localhost/",
            "http://tauri.localhost:8080/",
            "tauri://evil.example/",
            "ftp://tauri.localhost/",
            "file:///etc/passwd",
            "data:text/html,hello",
            "javascript:alert(1)",
            "not a url",
            "",
        ] {
            assert!(!f.allows(bad), "{bad}");
        }
    }

    #[test]
    fn a_caller_url_with_credentials_is_refused_whose_host_it_names() {
        let f = fence();
        assert!(!f.allows("http://user@tauri.localhost/"));
        assert!(!f.allows("http://user:pass@tauri.localhost/"));
    }

    #[test]
    fn the_default_port_folds_into_the_origin_and_an_explicit_one_matches() {
        let f = OriginFence::from_starts(&["http://tauri.localhost"]).unwrap();
        assert!(f.allows("http://tauri.localhost:80/"));
        let f = OriginFence::from_starts(&["https://desk.example.test"]).unwrap();
        assert!(f.allows("https://desk.example.test:443/index.html"));
        assert!(!f.allows("https://desk.example.test:8443/index.html"));
    }

    #[test]
    fn an_explicit_non_default_port_on_the_tauri_scheme_stays_distinct() {
        // The tauri scheme has no known default port, so any explicit port
        // is non default: an origin that carries one never folds into the
        // portless bundled origin, in either direction.
        let f = fence();
        assert!(!f.allows("tauri://localhost:65535/"));
        let f = OriginFence::from_starts(&["tauri://localhost:65535"]).unwrap();
        assert_eq!(f.origins(), ["tauri://localhost:65535"]);
        assert!(f.allows("tauri://localhost:65535/index.html"));
        assert!(!f.allows("tauri://localhost/"));
    }

    #[test]
    fn an_override_origin_joins_the_bundled_ones() {
        let f = OriginFence::from_starts(&[
            "http://tauri.localhost",
            "tauri://localhost",
            "http://127.0.0.1:8080",
        ])
        .unwrap();
        assert!(f.allows("http://127.0.0.1:8080/erp/dashboard"));
        assert!(f.allows("http://tauri.localhost/"));
        assert!(!f.allows("http://127.0.0.1:8081/"));
    }

    #[test]
    fn a_fence_that_cannot_name_its_own_origin_refuses_whole() {
        assert!(OriginFence::from_starts(&[] as &[&str]).is_err());
        for bad in [
            "file:///etc/passwd",
            "javascript:alert(1)",
            "http://user@tauri.localhost/",
            "not a url",
            "",
            "http://",
        ] {
            assert!(OriginFence::from_starts(&[bad]).is_err(), "{bad}");
        }
    }

    #[test]
    fn the_fence_holds_normalized_origins_only() {
        let f = OriginFence::from_starts(&[
            "http://TAURI.LOCALHOST/index.html",
            "http://tauri.localhost:80/",
        ])
        .unwrap();
        assert_eq!(f.origins(), ["http://tauri.localhost"]);
    }
}
