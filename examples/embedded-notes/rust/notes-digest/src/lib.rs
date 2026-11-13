//! The implementation of the notes-digest API: it counts the notes by
//! calling notes-api through notes-api's SDK.
//!
//! In the binary of the Desktop environment the call never leaves the
//! process (D116): the client's transport hands each request to notes-api's
//! `handle`, with the loopback credential of this API's server, which
//! notes-api's `@requireService` admits as it would a caller on another
//! machine. The code here is what it would be between two servers.

use std::sync::Arc;

use async_trait::async_trait;
use notes_notes_api_sdk::namespaces::notes_for_services::ListNotesForServicesQueryParams;
use notes_notes_digest_api::types;
use notes_notes_digest_api::{Deps, DigestsImplementation, Handlers};
use superschematic_http_runtime::{ApiError, RequestContext};

/// Builds the implementation of notes-digest from its dependencies: the
/// handlers the server's entrypoint joins with what it builds into the
/// API's `Implementations` (`Implementations::new`).
pub async fn create(deps: Deps) -> Result<Handlers, Box<dyn std::error::Error + Send + Sync>> {
    let deps = Arc::new(deps);
    Ok(Handlers {
        digests: Arc::new(Digests {
            deps: Arc::clone(&deps),
        }),
    })
}

/// Implements DigestsImplementation.
pub struct Digests {
    pub deps: Arc<Deps>,
}

#[async_trait]
impl DigestsImplementation for Digests {
    /// The digest, counted from what notes-api answers, a page at a time.
    async fn get_digest(&self, _ctx: RequestContext) -> Result<types::Digest, ApiError> {
        let mut digest = types::Digest {
            notes: 0,
            pinned: 0,
        };
        let mut query = ListNotesForServicesQueryParams::default();
        loop {
            let page = self
                .deps
                .notes_api
                .notes_for_services
                .list_notes_for_services(Some(&query), None)
                .await
                .map_err(|err| ApiError::internal(format!("notes-api: {err}")))?;
            for note in &page.items {
                digest.notes += 1;
                digest.pinned += i64::from(note.pinned);
            }
            match page.next {
                Some(next) => query.cursor = Some(next),
                None => return Ok(digest),
            }
        }
    }
}
