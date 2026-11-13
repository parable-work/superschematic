//! The implementation of the notes-api API.
//!
//! The build serves the notes' own operations from notes-db's tables
//! (`@tableOperation`), so this crate writes the two things it does not:
//! the operation that asks for a note to be pinned, which writes a message
//! to notes-db's queue PinRequested in the request's transaction, and the
//! worker PinNotes, which handles each message by pinning its note.
//!
//! In the binary of the Desktop environment the worker runs in the
//! server's process (D116): the binary claims PinRequested on the file the
//! request wrote to, and the request's commit wakes it.

use std::sync::Arc;

use async_trait::async_trait;
use notes_notes_api_api::database_types::{self, PinRequested};
use notes_notes_api_api::runtime::orm;
use notes_notes_api_api::runtime::queue::{enqueue_sqlite, QueueDefinition};
use notes_notes_api_api::runtime::{ApiError, RequestContext};
use notes_notes_api_api::types;
use notes_notes_api_api::{
    BoxError, Deps, Handlers, PinsImplementation, PinsRequestPinArgs, Workers,
};

/// Builds the implementation of notes-api from its dependencies: the
/// handlers the server's entrypoint joins with what it builds into the
/// API's `Implementations` (`Implementations::new`).
pub async fn create(deps: Deps) -> Result<Handlers, BoxError> {
    Ok(Handlers {
        pins: Arc::new(Notes { deps }),
    })
}

/// Builds the API's workers from Deps of their own.
pub async fn create_workers(deps: Deps) -> Result<Arc<dyn Workers>, BoxError> {
    Ok(Arc::new(Notes { deps }))
}

/// Implements PinsImplementation and Workers over notes-db's file.
pub struct Notes {
    pub deps: Deps,
}

/// A failure of the database as the API answers it.
fn internal(err: orm::Error) -> ApiError {
    ApiError::internal(err.to_string())
}

#[async_trait]
impl PinsImplementation for Notes {
    /// Asks for a note to be pinned: a message on PinRequested, written
    /// with the check that the note is there, in one transaction.
    async fn request_pin(
        &self,
        _ctx: RequestContext,
        args: PinsRequestPinArgs,
    ) -> Result<types::PinRequest, ApiError> {
        let definition = QueueDefinition::parse(database_types::PIN_REQUESTED_QUEUE)
            .map_err(|err| ApiError::internal(err.to_string()))?;
        let id = args.id.as_uuid().to_string();
        let message = PinRequested { note_id: args.id };
        let found = self
            .deps
            .db
            .write_async(move |connection| -> Result<bool, orm::Error> {
                let notes: i64 = connection
                    .query_row("SELECT count(*) FROM note WHERE id = ?1", [&id], |row| {
                        row.get(0)
                    })
                    .map_err(|err| orm::Error::Database(err.to_string()))?;
                if notes == 0 {
                    return Ok(false);
                }
                enqueue_sqlite(connection, &definition, &message)
                    .map_err(|err| orm::Error::Database(err.to_string()))?;
                Ok(true)
            })
            .await
            .map_err(internal)?;
        if !found {
            return Err(ApiError::not_found("no such note"));
        }
        Ok(types::PinRequest { note_id: args.id })
    }
}

#[async_trait]
impl Workers for Notes {
    /// Pins the note a PinRequested names. A note deleted since is no
    /// failure: there is nothing left to pin.
    async fn pin_notes(&self, message: PinRequested) -> Result<(), BoxError> {
        let id = message.note_id.as_uuid().to_string();
        self.deps
            .db
            .write_async(move |connection| -> Result<(), orm::Error> {
                connection
                    .execute("UPDATE note SET pinned = 1 WHERE id = ?1", [&id])
                    .map_err(|err| orm::Error::Database(err.to_string()))?;
                Ok(())
            })
            .await?;
        Ok(())
    }
}
