//! The implementation of the notes-api API.
//!
//! superschematic wrote this crate once, as a scaffold, because it was
//! missing. It never writes it again: the crate is yours. Each method
//! answers 501 Not Implemented until you implement it.

use notes_notes_api_api::{Deps, Handlers};

/// Builds the implementation of notes-api from its dependencies: the
/// handlers the server's entrypoint joins with what it builds into the
/// API's `Implementations` (`Implementations::new`).
pub async fn create(_deps: Deps) -> Result<Handlers, Box<dyn std::error::Error + Send + Sync>> {
    Ok(Handlers {
    })
}
