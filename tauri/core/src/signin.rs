// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! The shell's side of staff sign in (ADR 0012 section 3). A shell is a
//! public client of the core, never of Appwrite: it opens the system browser
//! on the core's start route with a PKCE challenge (S256) and a return
//! target, receives a one time code at that target, and redeems the code with
//! its verifier for an access token and a rotating refresh token that
//! `tokens` keeps in the OS keychain.
//!
//! - The return target is the registered `gable://auth/callback` deep link
//!   (the desk) or a loopback address on a port the shell just bound (the
//!   work, field and pro shells), with the fixed path. The core accepts no
//!   other target.
//! - The verifier never leaves this process; the code is useless without it.
//! - Refresh rotates both tokens, serialised by one lock so two callers in
//!   this process never present the same refresh token (the core revokes the
//!   whole family when one is presented twice).
//! - Nothing here logs or prints, and every failure is a code word, never a
//!   value.

use std::io::{ErrorKind, Read, Write};
use std::net::{Ipv4Addr, TcpListener, TcpStream};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use sha2::{Digest, Sha256};
use url::Url;
use zeroize::Zeroizing;

use crate::custody::CustodyError;
use crate::tokens::{TokenSet, TokenStore};

/// The registered deep link the desk receives the one time code at.
pub const DEEP_LINK_TARGET: &str = "gable://auth/callback";
/// The fixed path of a loopback return target.
pub const LOOPBACK_PATH: &str = "/auth/callback";

/// How a sign in step failed. A code only: no URL, no token, no body.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SignInError {
    /// The method is not one a shell starts by redirect.
    BadMethod,
    /// No Gable server is configured for this shell.
    NoServer,
    /// The person did not come back in time.
    TimedOut,
    /// The core refused (a person Gable does not admit, a used or wrong code).
    Refused,
    /// The core or the network could not be reached, or answered a fault.
    Unavailable,
    /// There is no session to refresh.
    NoSession,
    /// The keychain could not be read or written.
    Custody,
}

impl SignInError {
    pub fn code(self) -> &'static str {
        match self {
            SignInError::BadMethod => "bad_method",
            SignInError::NoServer => "no_server",
            SignInError::TimedOut => "timed_out",
            SignInError::Refused => "sign_in_refused",
            SignInError::Unavailable => "sign_in_unavailable",
            SignInError::NoSession => "no_session",
            SignInError::Custody => "keychain_unavailable",
        }
    }
}

impl From<CustodyError> for SignInError {
    fn from(_: CustodyError) -> Self {
        SignInError::Custody
    }
}

/// A redirect sign in method a shell can start.
pub fn redirect_method(raw: &str) -> Option<&'static str> {
    match raw {
        "google" => Some("google"),
        "microsoft" => Some("microsoft"),
        "oidc" => Some("oidc"),
        _ => None,
    }
}

/// A PKCE pair. The verifier is wiped when the pair drops.
pub struct Pkce {
    verifier: Zeroizing<String>,
    challenge: String,
}

/// The S256 challenge of a verifier (RFC 7636 section 4.2).
pub fn challenge_of(verifier: &str) -> String {
    URL_SAFE_NO_PAD.encode(Sha256::digest(verifier.as_bytes()))
}

impl Pkce {
    /// A fresh pair: 32 random bytes as a 43 character base64url verifier.
    pub fn generate() -> Result<Self, SignInError> {
        let mut bytes = Zeroizing::new([0u8; 32]);
        getrandom::fill(&mut bytes[..]).map_err(|_| SignInError::Unavailable)?;
        Ok(Self::from_verifier(URL_SAFE_NO_PAD.encode(&bytes[..])))
    }

    pub fn from_verifier(verifier: String) -> Self {
        let challenge = challenge_of(&verifier);
        Self {
            verifier: Zeroizing::new(verifier),
            challenge,
        }
    }

    pub fn challenge(&self) -> &str {
        &self.challenge
    }

    pub fn verifier(&self) -> &str {
        &self.verifier
    }
}

/// The address the system browser opens: the core's start route for `method`
/// on `base`'s origin, with the shell's PKCE challenge and return target.
pub fn start_url(base: &Url, method: &str, challenge: &str, return_to: &str) -> Option<Url> {
    if !matches!(base.scheme(), "http" | "https") || redirect_method(method).is_none() {
        return None;
    }
    let mut url = Url::parse(&format!(
        "{}/api/v1/auth/start/{method}",
        base.origin().ascii_serialization()
    ))
    .ok()?;
    url.query_pairs_mut()
        .append_pair("client", "shell")
        .append_pair("code_challenge", challenge)
        .append_pair("code_challenge_method", "S256")
        .append_pair("return_to", return_to);
    Some(url)
}

/// The loopback return target for a bound port.
pub fn loopback_target(port: u16) -> String {
    format!("http://127.0.0.1:{port}{LOOPBACK_PATH}")
}

/// What came back at the return target.
#[derive(Debug, PartialEq, Eq)]
pub enum Callback {
    /// The one time code.
    Code(String),
    /// The core refused the sign in.
    Refused,
}

const MAX_CODE_LEN: usize = 256;

fn callback_from_query(url: &Url) -> Option<Callback> {
    let mut code = None;
    let mut refused = false;
    for (k, v) in url.query_pairs() {
        match k.as_ref() {
            "code" if !v.is_empty() && v.len() <= MAX_CODE_LEN => code = Some(v.into_owned()),
            "error" if v == "refused" => refused = true,
            _ => {}
        }
    }
    match (code, refused) {
        (Some(c), false) => Some(Callback::Code(c)),
        (None, true) => Some(Callback::Refused),
        _ => None,
    }
}

/// Reads a `gable://auth/callback?code=...` deep link (the desk). Anything
/// else, a different host or path, a fragment, no code, is not a callback.
pub fn callback_from_deep_link(raw: &str) -> Option<Callback> {
    let url = Url::parse(raw.trim()).ok()?;
    if url.scheme() != "gable"
        || url.host_str() != Some("auth")
        || url.path() != "/callback"
        || url.fragment().is_some()
        || url.port().is_some()
        || !url.username().is_empty()
    {
        return None;
    }
    callback_from_query(&url)
}

/// A loopback listener on a port the OS chose, bound to 127.0.0.1 only.
pub struct Loopback {
    listener: TcpListener,
    port: u16,
}

const PAGE: &str = "<!doctype html><meta charset=utf-8><title>Gable</title>\
<body style=\"font-family:sans-serif;margin:3rem\"><p>Signed in. You can close this window and return to Gable.</p>";

impl Loopback {
    pub fn bind() -> Result<Self, SignInError> {
        let listener =
            TcpListener::bind((Ipv4Addr::LOCALHOST, 0)).map_err(|_| SignInError::Unavailable)?;
        let port = listener
            .local_addr()
            .map_err(|_| SignInError::Unavailable)?
            .port();
        listener
            .set_nonblocking(true)
            .map_err(|_| SignInError::Unavailable)?;
        Ok(Self { listener, port })
    }

    pub fn port(&self) -> u16 {
        self.port
    }

    pub fn target(&self) -> String {
        loopback_target(self.port)
    }

    /// Waits for the callback request, answering it with a plain page. A
    /// request for any other path is answered 404 and does not end the wait.
    pub fn wait(self, timeout: Duration) -> Result<Callback, SignInError> {
        let deadline = Instant::now() + timeout;
        loop {
            match self.listener.accept() {
                Ok((stream, _)) => {
                    if let Some(cb) = serve_one(stream) {
                        return Ok(cb);
                    }
                }
                Err(e) if e.kind() == ErrorKind::WouldBlock => {
                    if Instant::now() >= deadline {
                        return Err(SignInError::TimedOut);
                    }
                    std::thread::sleep(Duration::from_millis(25));
                }
                Err(_) => return Err(SignInError::Unavailable),
            }
        }
    }
}

fn serve_one(mut stream: TcpStream) -> Option<Callback> {
    let _ = stream.set_nonblocking(false);
    let _ = stream.set_read_timeout(Some(Duration::from_secs(2)));
    let mut buf = [0u8; 4096];
    let mut len = 0;
    while len < buf.len() {
        match stream.read(&mut buf[len..]) {
            Ok(0) => break,
            Ok(n) => {
                len += n;
                if buf[..len].windows(4).any(|w| w == b"\r\n\r\n") {
                    break;
                }
            }
            Err(_) => break,
        }
    }
    let head = String::from_utf8_lossy(&buf[..len]);
    let line = head.lines().next().unwrap_or("");
    let mut parts = line.split(' ');
    let (method, target) = (parts.next(), parts.next());
    let parsed = match (method, target) {
        (Some("GET"), Some(t)) if t.starts_with('/') => {
            Url::parse(&format!("http://127.0.0.1{t}")).ok()
        }
        _ => None,
    };
    let outcome = parsed
        .as_ref()
        .filter(|u| u.path() == LOOPBACK_PATH)
        .and_then(callback_from_query);
    let (status, body) = match outcome {
        Some(Callback::Code(_)) => ("200 OK", PAGE),
        Some(Callback::Refused) => (
            "200 OK",
            "<!doctype html><meta charset=utf-8><title>Gable</title><body style=\"font-family:sans-serif;margin:3rem\"><p>Sign in was refused. Return to Gable to try again.</p>",
        ),
        None => ("404 Not Found", ""),
    };
    let reply = format!(
        "HTTP/1.1 {status}\r\nContent-Type: text/html; charset=utf-8\r\nCache-Control: no-store\r\nReferrer-Policy: no-referrer\r\nConnection: close\r\nContent-Length: {}\r\n\r\n{body}",
        body.len()
    );
    let _ = stream.write_all(reply.as_bytes());
    outcome
}

/// A POST answer: the status and the body, nothing else.
pub struct HttpAnswer {
    pub status: u16,
    pub body: String,
}

/// The shell's one HTTP seam, so the tests run the whole flow without a
/// network.
pub trait Transport: Send + Sync {
    fn post_json(
        &self,
        url: &str,
        bearer: Option<&str>,
        body: &str,
    ) -> Result<HttpAnswer, SignInError>;
}

/// The real transport: a blocking client with short timeouts that follows no
/// redirect.
pub struct UreqTransport {
    agent: ureq::Agent,
}

impl UreqTransport {
    pub fn new() -> Self {
        Self {
            agent: ureq::AgentBuilder::new()
                .timeout_connect(Duration::from_secs(5))
                .timeout(Duration::from_secs(15))
                .redirects(0)
                .build(),
        }
    }
}

impl Default for UreqTransport {
    fn default() -> Self {
        Self::new()
    }
}

impl Transport for UreqTransport {
    fn post_json(
        &self,
        url: &str,
        bearer: Option<&str>,
        body: &str,
    ) -> Result<HttpAnswer, SignInError> {
        let mut req = self.agent.post(url).set("Content-Type", "application/json");
        if let Some(b) = bearer {
            req = req.set("Authorization", &format!("Bearer {b}"));
        }
        match req.send_string(body) {
            Ok(resp) => Ok(HttpAnswer {
                status: resp.status(),
                body: resp.into_string().map_err(|_| SignInError::Unavailable)?,
            }),
            Err(ureq::Error::Status(status, resp)) => Ok(HttpAnswer {
                status,
                body: resp.into_string().unwrap_or_default(),
            }),
            Err(_) => Err(SignInError::Unavailable),
        }
    }
}

/// The shell's client of the core's token routes.
pub struct SignInClient {
    transport: Arc<dyn Transport>,
    base: Url,
    rotation: Mutex<()>,
}

fn tokens_from(body: &str) -> Result<TokenSet, SignInError> {
    let v: serde_json::Value = serde_json::from_str(body).map_err(|_| SignInError::Unavailable)?;
    let field = |k: &str| v.get(k).and_then(|x| x.as_str()).map(str::to_string);
    match (field("access_token"), field("refresh_token")) {
        (Some(a), Some(r)) => TokenSet::new(a, r).map_err(|_| SignInError::Unavailable),
        _ => Err(SignInError::Unavailable),
    }
}

impl SignInClient {
    pub fn new(transport: Arc<dyn Transport>, base: &Url) -> Self {
        Self {
            transport,
            base: base.clone(),
            rotation: Mutex::new(()),
        }
    }

    fn route(&self, path: &str) -> String {
        format!("{}{path}", self.base.origin().ascii_serialization())
    }

    /// Spends the one time code with its verifier for a token pair.
    pub fn redeem(&self, code: &str, verifier: &str) -> Result<TokenSet, SignInError> {
        let body = serde_json::json!({ "code": code, "code_verifier": verifier }).to_string();
        let answer =
            self.transport
                .post_json(&self.route("/api/v1/auth/shell/redeem"), None, &body)?;
        match answer.status {
            200 => tokens_from(&answer.body),
            400 | 401 => Err(SignInError::Refused),
            _ => Err(SignInError::Unavailable),
        }
    }

    /// Rotates the stored pair. One caller at a time: the refresh token read
    /// is the current one, and the pair written back is the new one before
    /// the next caller reads. A 401 means the family is gone, so the local
    /// pair is cleared too.
    pub fn refresh(&self, store: &TokenStore, profile: &str) -> Result<TokenSet, SignInError> {
        let _one_at_a_time = self.rotation.lock().map_err(|_| SignInError::Unavailable)?;
        let current = store.read(profile)?.ok_or(SignInError::NoSession)?;
        let body = serde_json::json!({ "refresh_token": current.refresh() }).to_string();
        let answer =
            self.transport
                .post_json(&self.route("/api/v1/auth/shell/refresh"), None, &body)?;
        match answer.status {
            200 => {
                let rotated = tokens_from(&answer.body)?;
                store.store(profile, &rotated)?;
                Ok(rotated)
            }
            400 | 401 => {
                store.clear(profile)?;
                Err(SignInError::NoSession)
            }
            _ => Err(SignInError::Unavailable),
        }
    }

    /// Tells the core the session is over. Best effort: the local pair is
    /// cleared by the caller whatever this answers.
    pub fn sign_out(&self, access: &str) {
        let _ = self.transport.post_json(
            &self.route("/api/v1/auth/shell/signout"),
            Some(access),
            "{}",
        );
    }

    /// Redeems a callback's code and keeps the pair in the keychain.
    pub fn complete(
        &self,
        callback: Callback,
        pkce: &Pkce,
        store: &TokenStore,
        profile: &str,
    ) -> Result<(), SignInError> {
        match callback {
            Callback::Refused => Err(SignInError::Refused),
            Callback::Code(code) => {
                let pair = self.redeem(&code, pkce.verifier())?;
                store.store(profile, &pair)?;
                Ok(())
            }
        }
    }

    /// The loopback sign in (work, field, pro): bind a port, open the system
    /// browser on the start route, wait for the code, redeem it.
    pub fn sign_in_loopback(
        &self,
        store: &TokenStore,
        profile: &str,
        method: &str,
        open: &dyn Fn(&str) -> bool,
        timeout: Duration,
    ) -> Result<(), SignInError> {
        let method = redirect_method(method).ok_or(SignInError::BadMethod)?;
        let pkce = Pkce::generate()?;
        let listener = Loopback::bind()?;
        let start = start_url(&self.base, method, pkce.challenge(), &listener.target())
            .ok_or(SignInError::NoServer)?;
        if !open(start.as_str()) {
            return Err(SignInError::Unavailable);
        }
        let callback = listener.wait(timeout)?;
        self.complete(callback, &pkce, store, profile)
    }
}

/// The desk's sign in: the start opens the system browser and remembers the
/// verifier; the deep link that comes back completes it.
#[derive(Default)]
pub struct DeepLinkSignIn {
    pending: Mutex<Option<Pkce>>,
}

impl DeepLinkSignIn {
    pub fn begin(
        &self,
        client: &SignInClient,
        method: &str,
        open: &dyn Fn(&str) -> bool,
    ) -> Result<(), SignInError> {
        let method = redirect_method(method).ok_or(SignInError::BadMethod)?;
        let pkce = Pkce::generate()?;
        let start = start_url(&client.base, method, pkce.challenge(), DEEP_LINK_TARGET)
            .ok_or(SignInError::NoServer)?;
        // A new start replaces an unfinished one: its verifier is dropped.
        *self.pending.lock().map_err(|_| SignInError::Unavailable)? = Some(pkce);
        if !open(start.as_str()) {
            let _ = self.pending.lock().map(|mut p| p.take());
            return Err(SignInError::Unavailable);
        }
        Ok(())
    }

    /// Completes the sign in a deep link answers. A link with no sign in
    /// waiting is not ours to act on, and spends nothing.
    pub fn complete(
        &self,
        client: &SignInClient,
        raw_link: &str,
        store: &TokenStore,
        profile: &str,
    ) -> Result<(), SignInError> {
        let callback = callback_from_deep_link(raw_link).ok_or(SignInError::BadMethod)?;
        let pkce = self
            .pending
            .lock()
            .map_err(|_| SignInError::Unavailable)?
            .take()
            .ok_or(SignInError::NoSession)?;
        client.complete(callback, &pkce, store, profile)
    }
}

/// Opens a URL in the system browser. http and https only.
pub fn open_in_browser(url: &str) -> bool {
    matches!(Url::parse(url), Ok(u) if matches!(u.scheme(), "http" | "https"))
        && open::that(url).is_ok()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::custody::MemoryCustody;
    use std::collections::VecDeque;

    const WORK: &str = "ai.futurebuild.gable.work";

    /// Scripted answers, and a record of what was posted.
    struct Fake {
        answers: Mutex<VecDeque<Result<HttpAnswer, SignInError>>>,
        seen: Mutex<Vec<(String, Option<String>, String)>>,
    }

    impl Fake {
        fn new(answers: Vec<Result<HttpAnswer, SignInError>>) -> Arc<Self> {
            Arc::new(Self {
                answers: Mutex::new(answers.into()),
                seen: Mutex::new(Vec::new()),
            })
        }
        fn seen(&self) -> Vec<(String, Option<String>, String)> {
            self.seen.lock().unwrap().clone()
        }
    }

    impl Transport for Fake {
        fn post_json(
            &self,
            url: &str,
            bearer: Option<&str>,
            body: &str,
        ) -> Result<HttpAnswer, SignInError> {
            self.seen.lock().unwrap().push((
                url.to_string(),
                bearer.map(str::to_string),
                body.to_string(),
            ));
            self.answers
                .lock()
                .unwrap()
                .pop_front()
                .unwrap_or(Err(SignInError::Unavailable))
        }
    }

    fn ok(access: &str, refresh: &str) -> Result<HttpAnswer, SignInError> {
        Ok(HttpAnswer {
            status: 200,
            body: serde_json::json!({"access_token": access, "refresh_token": refresh}).to_string(),
        })
    }

    fn status(code: u16) -> Result<HttpAnswer, SignInError> {
        Ok(HttpAnswer {
            status: code,
            body: String::new(),
        })
    }

    fn base() -> Url {
        Url::parse("https://gable.example/some/page?x=1").unwrap()
    }

    fn store() -> (TokenStore, Arc<MemoryCustody>) {
        let c = Arc::new(MemoryCustody::new());
        (TokenStore::new(c.clone()), c)
    }

    #[test]
    fn the_s256_challenge_matches_the_rfc_vector() {
        assert_eq!(
            challenge_of("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"),
            "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
        );
    }

    #[test]
    fn a_generated_pair_is_fresh_and_in_the_cores_shape() {
        let (a, b) = (Pkce::generate().unwrap(), Pkce::generate().unwrap());
        assert_ne!(a.verifier(), b.verifier());
        assert_eq!(a.verifier().len(), 43);
        assert_eq!(a.challenge().len(), 43);
        assert_eq!(a.challenge(), challenge_of(a.verifier()));
        assert!(a
            .verifier()
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_'));
    }

    #[test]
    fn the_start_url_is_the_cores_start_route_on_the_origin_with_the_shell_leg() {
        let url = start_url(
            &base(),
            "google",
            "CHAL",
            "http://127.0.0.1:5000/auth/callback",
        )
        .unwrap();
        assert_eq!(url.origin().ascii_serialization(), "https://gable.example");
        assert_eq!(url.path(), "/api/v1/auth/start/google");
        let q: std::collections::BTreeMap<_, _> = url.query_pairs().into_owned().collect();
        assert_eq!(q["client"], "shell");
        assert_eq!(q["code_challenge"], "CHAL");
        assert_eq!(q["code_challenge_method"], "S256");
        assert_eq!(q["return_to"], "http://127.0.0.1:5000/auth/callback");
        assert_eq!(q.len(), 4);
    }

    #[test]
    fn only_the_redirect_methods_and_http_servers_start() {
        assert!(start_url(&base(), "email_password", "c", DEEP_LINK_TARGET).is_none());
        assert!(start_url(&base(), "magic_link", "c", DEEP_LINK_TARGET).is_none());
        assert!(start_url(&base(), "../x", "c", DEEP_LINK_TARGET).is_none());
        let file = Url::parse("file:///etc/passwd").unwrap();
        assert!(start_url(&file, "google", "c", DEEP_LINK_TARGET).is_none());
    }

    #[test]
    fn the_return_targets_are_the_ones_the_core_registers() {
        assert_eq!(DEEP_LINK_TARGET, "gable://auth/callback");
        assert_eq!(
            loopback_target(41873),
            "http://127.0.0.1:41873/auth/callback"
        );
    }

    #[test]
    fn the_deep_link_callback_is_read_exactly() {
        assert_eq!(
            callback_from_deep_link("gable://auth/callback?code=abc_DEF-1"),
            Some(Callback::Code("abc_DEF-1".to_string()))
        );
        assert_eq!(
            callback_from_deep_link("gable://auth/callback?error=refused"),
            Some(Callback::Refused)
        );
        for bad in [
            "gable://auth/callback",
            "gable://auth/callback?code=",
            "gable://auth/callback?code=a#frag",
            "gable://auth/callback?code=a&error=refused",
            "gable://auth/callback?error=other",
            "gable://auth/other?code=a",
            "gable://quotes/callback?code=a",
            "gable://auth:80/callback?code=a",
            "gable://user@auth/callback?code=a",
            "https://auth/callback?code=a",
            "gable://quotes/1",
            "not a url",
        ] {
            assert_eq!(callback_from_deep_link(bad), None, "{bad}");
        }
        let long = format!(
            "gable://auth/callback?code={}",
            "a".repeat(MAX_CODE_LEN + 1)
        );
        assert_eq!(callback_from_deep_link(&long), None);
    }

    fn get(port: u16, target: &str) -> String {
        let mut s = TcpStream::connect((Ipv4Addr::LOCALHOST, port)).unwrap();
        write!(s, "GET {target} HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n").unwrap();
        let mut out = String::new();
        let _ = s.read_to_string(&mut out);
        out
    }

    #[test]
    fn the_loopback_receives_the_code_and_ignores_other_paths() {
        let l = Loopback::bind().unwrap();
        let port = l.port();
        assert_eq!(l.target(), loopback_target(port));
        let waiter = std::thread::spawn(move || l.wait(Duration::from_secs(10)));
        assert!(get(port, "/favicon.ico").starts_with("HTTP/1.1 404"));
        assert!(get(port, "/auth/callback").starts_with("HTTP/1.1 404"));
        assert!(get(port, "/other?code=x").starts_with("HTTP/1.1 404"));
        let reply = get(port, "/auth/callback?code=the-code");
        assert!(reply.starts_with("HTTP/1.1 200"));
        assert!(reply.contains("Referrer-Policy: no-referrer"));
        assert_eq!(
            waiter.join().unwrap(),
            Ok(Callback::Code("the-code".to_string()))
        );
    }

    #[test]
    fn the_loopback_reads_a_refusal_and_times_out() {
        let l = Loopback::bind().unwrap();
        let port = l.port();
        let waiter = std::thread::spawn(move || l.wait(Duration::from_secs(10)));
        let _ = get(port, "/auth/callback?error=refused");
        assert_eq!(waiter.join().unwrap(), Ok(Callback::Refused));
        let l = Loopback::bind().unwrap();
        assert_eq!(
            l.wait(Duration::from_millis(100)),
            Err(SignInError::TimedOut)
        );
    }

    #[test]
    fn the_loopback_binds_127_0_0_1_only() {
        let l = Loopback::bind().unwrap();
        let addr = l.listener.local_addr().unwrap();
        assert!(addr.ip().is_loopback());
        assert_eq!(addr.ip().to_string(), "127.0.0.1");
    }

    #[test]
    fn redeem_posts_the_code_and_verifier_and_keeps_the_pair_in_the_keychain() {
        let fake = Fake::new(vec![ok("gst_access", "fam.refresh")]);
        let client = SignInClient::new(fake.clone(), &base());
        let (store, custody) = store();
        let pkce = Pkce::from_verifier("v".repeat(43));
        client
            .complete(Callback::Code("c1".into()), &pkce, &store, WORK)
            .unwrap();
        let seen = fake.seen();
        assert_eq!(seen[0].0, "https://gable.example/api/v1/auth/shell/redeem");
        let body: serde_json::Value = serde_json::from_str(&seen[0].2).unwrap();
        assert_eq!(body["code"], "c1");
        assert_eq!(body["code_verifier"], "v".repeat(43));
        let read = store.read(WORK).unwrap().unwrap();
        assert_eq!(
            (read.access(), read.refresh()),
            ("gst_access", "fam.refresh")
        );
        assert_eq!(custody.snapshot_len(), 2);
    }

    #[test]
    fn a_refusal_or_a_fault_stores_nothing() {
        let (store, custody) = store();
        let pkce = Pkce::from_verifier("v".repeat(43));
        for (answer, want) in [
            (status(401), SignInError::Refused),
            (status(400), SignInError::Refused),
            (status(429), SignInError::Unavailable),
            (status(503), SignInError::Unavailable),
            (Err(SignInError::Unavailable), SignInError::Unavailable),
            (
                Ok(HttpAnswer {
                    status: 200,
                    body: "{}".into(),
                }),
                SignInError::Unavailable,
            ),
        ] {
            let client = SignInClient::new(Fake::new(vec![answer]), &base());
            assert_eq!(
                client.complete(Callback::Code("c".into()), &pkce, &store, WORK),
                Err(want)
            );
        }
        let client = SignInClient::new(Fake::new(vec![]), &base());
        assert_eq!(
            client.complete(Callback::Refused, &pkce, &store, WORK),
            Err(SignInError::Refused)
        );
        assert_eq!(custody.snapshot_len(), 0);
    }

    #[test]
    fn refresh_rotates_both_tokens_in_the_keychain() {
        let fake = Fake::new(vec![ok("gst_two", "fam.two")]);
        let client = SignInClient::new(fake.clone(), &base());
        let (store, _) = store();
        store
            .store(
                WORK,
                &TokenSet::new("gst_one".into(), "fam.one".into()).unwrap(),
            )
            .unwrap();
        let rotated = client.refresh(&store, WORK).unwrap();
        assert_eq!(rotated.access(), "gst_two");
        let body: serde_json::Value = serde_json::from_str(&fake.seen()[0].2).unwrap();
        assert_eq!(body["refresh_token"], "fam.one");
        let read = store.read(WORK).unwrap().unwrap();
        assert_eq!((read.access(), read.refresh()), ("gst_two", "fam.two"));
    }

    #[test]
    fn a_refresh_the_core_refuses_ends_the_local_session_and_a_fault_keeps_it() {
        let (store, _) = store();
        let seed = || {
            store
                .store(WORK, &TokenSet::new("a".into(), "f.r".into()).unwrap())
                .unwrap()
        };
        seed();
        let client = SignInClient::new(Fake::new(vec![status(401)]), &base());
        assert_eq!(
            client.refresh(&store, WORK).unwrap_err(),
            SignInError::NoSession
        );
        assert_eq!(store.read(WORK), Ok(None));
        seed();
        let client = SignInClient::new(Fake::new(vec![status(503)]), &base());
        assert_eq!(
            client.refresh(&store, WORK).unwrap_err(),
            SignInError::Unavailable
        );
        assert!(
            store.read(WORK).unwrap().is_some(),
            "a fault is not a sign out"
        );
        let client = SignInClient::new(Fake::new(vec![]), &base());
        store.clear(WORK).unwrap();
        assert_eq!(
            client.refresh(&store, WORK).unwrap_err(),
            SignInError::NoSession
        );
    }

    /// The core revokes a family when one refresh token is presented twice,
    /// so two callers in this process must never present the same one.
    #[test]
    fn simultaneous_refreshes_present_each_refresh_token_once() {
        struct Rotating {
            current: Mutex<u32>,
            presented: Mutex<Vec<String>>,
        }
        impl Transport for Rotating {
            fn post_json(
                &self,
                _: &str,
                _: Option<&str>,
                body: &str,
            ) -> Result<HttpAnswer, SignInError> {
                let v: serde_json::Value = serde_json::from_str(body).unwrap();
                let presented = v["refresh_token"].as_str().unwrap().to_string();
                self.presented.lock().unwrap().push(presented.clone());
                std::thread::sleep(Duration::from_millis(20));
                let mut cur = self.current.lock().unwrap();
                if presented != format!("fam.r{}", *cur) {
                    return Ok(HttpAnswer {
                        status: 401,
                        body: String::new(),
                    });
                }
                *cur += 1;
                Ok(HttpAnswer {
                    status: 200,
                    body: serde_json::json!({"access_token": format!("gst_{}", *cur), "refresh_token": format!("fam.r{}", *cur)}).to_string(),
                })
            }
        }
        let rot = Arc::new(Rotating {
            current: Mutex::new(0),
            presented: Mutex::new(vec![]),
        });
        let client = Arc::new(SignInClient::new(rot.clone(), &base()));
        let (store, _) = store();
        store
            .store(
                WORK,
                &TokenSet::new("gst_0".into(), "fam.r0".into()).unwrap(),
            )
            .unwrap();
        let handles: Vec<_> = (0..3)
            .map(|_| {
                let (c, s) = (client.clone(), store.clone());
                std::thread::spawn(move || c.refresh(&s, WORK))
            })
            .collect();
        for h in handles {
            assert!(
                h.join().unwrap().is_ok(),
                "a serialised refresh is never a reuse"
            );
        }
        let mut seen = rot.presented.lock().unwrap().clone();
        seen.sort();
        seen.dedup();
        assert_eq!(
            seen.len(),
            3,
            "each refresh token is presented exactly once"
        );
        assert_eq!(store.read(WORK).unwrap().unwrap().refresh(), "fam.r3");
    }

    #[test]
    fn sign_out_tells_the_core_with_the_access_token_as_the_bearer() {
        let fake = Fake::new(vec![status(204)]);
        SignInClient::new(fake.clone(), &base()).sign_out("gst_tok");
        let seen = fake.seen();
        assert_eq!(seen[0].0, "https://gable.example/api/v1/auth/shell/signout");
        assert_eq!(seen[0].1.as_deref(), Some("gst_tok"));
        SignInClient::new(Fake::new(vec![Err(SignInError::Unavailable)]), &base()).sign_out("x");
    }

    #[test]
    fn the_loopback_sign_in_runs_end_to_end_against_a_fake_core() {
        let fake = Fake::new(vec![ok("gst_a", "fam.r")]);
        let client = SignInClient::new(fake.clone(), &base());
        let (store, _) = store();
        // The "browser" opens the start URL, then calls the loopback back.
        let open = |start: &str| {
            let url = Url::parse(start).unwrap();
            let q: std::collections::BTreeMap<_, _> = url.query_pairs().into_owned().collect();
            assert_eq!(q["code_challenge_method"], "S256");
            let target = Url::parse(&q["return_to"]).unwrap();
            let port = target.port().unwrap();
            std::thread::spawn(move || get(port, "/auth/callback?code=one-time"));
            true
        };
        client
            .sign_in_loopback(&store, WORK, "google", &open, Duration::from_secs(10))
            .unwrap();
        let body: serde_json::Value = serde_json::from_str(&fake.seen()[0].2).unwrap();
        assert_eq!(body["code"], "one-time");
        let verifier = body["code_verifier"].as_str().unwrap();
        assert_eq!(
            verifier.len(),
            43,
            "the verifier that redeems is the one whose challenge started it"
        );
        assert_eq!(store.read(WORK).unwrap().unwrap().access(), "gst_a");
    }

    #[test]
    fn the_sign_in_start_refuses_a_bad_method_and_a_browser_that_will_not_open() {
        let (store, _) = store();
        let client = SignInClient::new(Fake::new(vec![]), &base());
        assert_eq!(
            client.sign_in_loopback(
                &store,
                WORK,
                "magic_link",
                &|_| true,
                Duration::from_millis(50)
            ),
            Err(SignInError::BadMethod)
        );
        assert_eq!(
            client.sign_in_loopback(
                &store,
                WORK,
                "google",
                &|_| false,
                Duration::from_millis(50)
            ),
            Err(SignInError::Unavailable)
        );
        assert_eq!(
            client.sign_in_loopback(
                &store,
                WORK,
                "google",
                &|_| true,
                Duration::from_millis(100)
            ),
            Err(SignInError::TimedOut)
        );
    }

    #[test]
    fn the_desk_deep_link_completes_only_a_sign_in_it_started() {
        let fake = Fake::new(vec![ok("gst_d", "fam.d")]);
        let client = SignInClient::new(fake.clone(), &base());
        let (store, _) = store();
        let desk = DeepLinkSignIn::default();
        // A link with nothing waiting spends nothing.
        assert_eq!(
            desk.complete(&client, "gable://auth/callback?code=stray", &store, WORK),
            Err(SignInError::NoSession)
        );
        assert!(fake.seen().is_empty());
        let opened = Mutex::new(String::new());
        desk.begin(&client, "microsoft", &|u| {
            *opened.lock().unwrap() = u.to_string();
            true
        })
        .unwrap();
        let url = Url::parse(&opened.lock().unwrap()).unwrap();
        let q: std::collections::BTreeMap<_, _> = url.query_pairs().into_owned().collect();
        assert_eq!(q["return_to"], DEEP_LINK_TARGET);
        // Not a callback: left waiting.
        assert_eq!(
            desk.complete(&client, "gable://quotes/1", &store, WORK),
            Err(SignInError::BadMethod)
        );
        desk.complete(&client, "gable://auth/callback?code=real", &store, WORK)
            .unwrap();
        let body: serde_json::Value = serde_json::from_str(&fake.seen()[0].2).unwrap();
        assert_eq!(body["code"], "real");
        assert_eq!(
            challenge_of(body["code_verifier"].as_str().unwrap()),
            q["code_challenge"]
        );
        assert_eq!(store.read(WORK).unwrap().unwrap().access(), "gst_d");
        // One link, one redemption.
        assert_eq!(
            desk.complete(&client, "gable://auth/callback?code=real", &store, WORK),
            Err(SignInError::NoSession)
        );
    }

    #[test]
    fn the_browser_opener_takes_http_and_https_only() {
        assert!(!open_in_browser("file:///etc/passwd"));
        assert!(!open_in_browser("javascript:alert(1)"));
        assert!(!open_in_browser("gable://auth/callback"));
        assert!(!open_in_browser("not a url"));
    }

    #[test]
    fn the_sign_in_path_holds_no_logging_or_printing() {
        let src = include_str!("signin.rs");
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
            assert!(!production.contains(banned), "signin.rs contains {banned}");
        }
    }
}
