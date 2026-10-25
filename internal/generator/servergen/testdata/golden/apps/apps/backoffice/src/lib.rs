//! The program of app Backoffice of desk-app-api (D78): the project's own HTTP
//! program over the API, which a stack runs as a deployable of its own. The
//! app's entrypoint, which the stack's build writes, builds its parts from
//! the environment as the API's server builds its own, and calls `serve`.
//!
//! superschematic wrote this crate once, as a scaffold, because it was
//! missing, and never writes it again: it is yours.

use schemas_desk_app_api_api::apps::BackofficeParts;

/// Serves the app on `parts.addr` until it stops, with the API's Deps and
/// implementations, and its engine, in `parts`. Return when the process
/// should exit: an error fails it.
///
/// It serves nothing yet, and fails, saying so.
pub async fn serve(parts: BackofficeParts) -> std::io::Result<()> {
    Err(std::io::Error::other(format!(
        "app Backoffice of desk-app-api does not serve {} yet: write its serve",
        parts.addr
    )))
}
