// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! The offline counter's calls to the Gable it was enrolled against (ADR 0011
//! sections 5.4 and 3): the core fetches its own snapshot and sends its own
//! queue, over the enrolled origin, with the access token the keychain holds
//! (ADR 0012 slice 5). The page never carries a snapshot, a sale or a sync
//! answer into the core, so the figures the core signs and the outcomes that
//! free the caps come from the server alone.

use std::sync::Arc;
use std::time::Duration;

use url::Url;

use super::model::OfflineError;
use crate::shell::TokenState;

/// One answer from the server: the status and the body.
pub struct ServerAnswer {
    pub status: u16,
    pub body: String,
}

/// The core's seam to the server, so the tests run the snapshot fetch and the
/// sync without a network.
pub trait OfflineServer: Send + Sync {
    /// `GET /api/v1/pos/offline/snapshot?register_id=&high_seq=`.
    fn get_snapshot(&self, register_id: &str, high_seq: i64) -> Result<ServerAnswer, OfflineError>;
    /// `POST /api/v1/pos/sync` with the batch body and its idempotency key.
    fn post_sync(&self, body: &str, idempotency_key: &str) -> Result<ServerAnswer, OfflineError>;
}

/// Where the core's access token comes from: the keychain, and a rotation
/// when the server refuses the one it holds.
pub trait TokenSource: Send + Sync {
    fn access(&self) -> Option<String>;
    fn refreshed(&self) -> Option<String>;
}

impl TokenSource for TokenState {
    fn access(&self) -> Option<String> {
        self.store()
            .read(self.profile().identifier)
            .ok()
            .flatten()
            .map(|t| t.access().to_string())
    }

    fn refreshed(&self) -> Option<String> {
        let client = self.client()?;
        client
            .refresh(self.store(), self.profile().identifier)
            .ok()
            .map(|t| t.access().to_string())
    }
}

/// The refusal the counter shows when the server cannot be reached.
pub fn unreachable() -> OfflineError {
    OfflineError::new(
        "server_unreachable",
        "The server cannot be reached: the register keeps its sales and sends them when the connection returns.",
    )
}

/// The real server: a blocking client with short timeouts that follows no
/// redirect, on the enrolled Gable's origin only.
pub struct HttpOfflineServer {
    agent: ureq::Agent,
    base: Url,
    tokens: Arc<dyn TokenSource>,
}

impl HttpOfflineServer {
    pub fn new(base: &Url, tokens: Arc<dyn TokenSource>) -> Self {
        Self {
            agent: ureq::AgentBuilder::new()
                .timeout_connect(Duration::from_secs(5))
                .timeout(Duration::from_secs(30))
                .redirects(0)
                .build(),
            base: base.clone(),
            tokens,
        }
    }

    fn route(&self, path_and_query: &str) -> String {
        format!(
            "{}{path_and_query}",
            self.base.origin().ascii_serialization()
        )
    }

    /// Sends one request with the keychain's token, and once more with a
    /// rotated one when the server answers 401.
    fn call(
        &self,
        send: &dyn Fn(&str) -> Result<ServerAnswer, OfflineError>,
    ) -> Result<ServerAnswer, OfflineError> {
        let token = self.tokens.access().ok_or_else(no_session)?;
        let first = send(&token)?;
        if first.status != 401 {
            return Ok(first);
        }
        let rotated = self.tokens.refreshed().ok_or_else(no_session)?;
        send(&rotated)
    }

    fn answer(result: Result<ureq::Response, ureq::Error>) -> Result<ServerAnswer, OfflineError> {
        match result {
            Ok(resp) => Ok(ServerAnswer {
                status: resp.status(),
                body: resp.into_string().map_err(|_| unreachable())?,
            }),
            Err(ureq::Error::Status(status, resp)) => Ok(ServerAnswer {
                status,
                body: resp.into_string().unwrap_or_default(),
            }),
            Err(_) => Err(unreachable()),
        }
    }
}

fn no_session() -> OfflineError {
    OfflineError::new(
        "no_session",
        "Sign in on this register again: the shell holds no session to reach the server with.",
    )
}

impl OfflineServer for HttpOfflineServer {
    fn get_snapshot(&self, register_id: &str, high_seq: i64) -> Result<ServerAnswer, OfflineError> {
        let mut url =
            Url::parse(&self.route("/api/v1/pos/offline/snapshot")).map_err(|_| unreachable())?;
        url.query_pairs_mut()
            .append_pair("register_id", register_id)
            .append_pair("high_seq", &high_seq.to_string());
        let target = url.to_string();
        self.call(&|token| {
            Self::answer(
                self.agent
                    .get(&target)
                    .set("Authorization", &format!("Bearer {token}"))
                    .call(),
            )
        })
    }

    fn post_sync(&self, body: &str, idempotency_key: &str) -> Result<ServerAnswer, OfflineError> {
        let target = self.route("/api/v1/pos/sync");
        self.call(&|token| {
            Self::answer(
                self.agent
                    .post(&target)
                    .set("Content-Type", "application/json")
                    .set("Authorization", &format!("Bearer {token}"))
                    .set("Idempotency-Key", idempotency_key)
                    .send_string(body),
            )
        })
    }
}

/// The ADR 0001 error envelope's code and message, or a plain refusal.
pub fn refusal_of(answer: &ServerAnswer) -> OfflineError {
    let parsed: Option<(String, String)> = serde_json::from_str::<serde_json::Value>(&answer.body)
        .ok()
        .and_then(|v| {
            let e = v.get("error")?;
            Some((
                e.get("code")?.as_str()?.to_string(),
                e.get("message")
                    .and_then(|m| m.as_str())
                    .unwrap_or("")
                    .to_string(),
            ))
        });
    match parsed {
        Some((code, message)) => OfflineError { code, message },
        None => OfflineError::new(
            "server_refused",
            format!("the server answered {}", answer.status),
        ),
    }
}
