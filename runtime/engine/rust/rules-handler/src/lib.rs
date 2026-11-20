//! Development mode's request handler (D117; D103's step 6, its runtime
//! layout and pure request handler). A type is a table written as a
//! schema document, its behaviors as field types and `implements`; in
//! development mode its rows live in fixed tables: the types' versions,
//! the rows with their own fields as one JSON column and their owned
//! fields as another, one shared sub-table per behavior keyed by type and
//! id, the rows of the tables a use keeps for its type as a whole (D103's
//! step 4: Numbering's counts), each with a version of its own, and the
//! deadlines, events and changes the behaviors' queues hold.
//! Publishing writes rows and never a table, so it needs no migration and
//! no DDL, and it has no compatibility rule: a version may rename, retype,
//! remove and tighten, and the stored rows it no longer describes are
//! refused, each with what it fails, unless the publish says to discard
//! them.
//!
//! The handler is pure, as the core it embeds (`superschematic-rules`): a
//! [`Request`] and its [`Context`] (the caller, the time, the request's id
//! and a fresh UUID) go in with the store's answers so far; the reads it
//! needs, or its response and the [`Unit`] that applies its changes, come
//! out ([`Handler::run`]). It opens no connection and no file and reads no
//! clock. A host answers its reads ([`StoreRead`]) from the development
//! layout and applies its units in the request's transaction; [`serve`] is
//! that loop over a [`Store`] that answers at once, and [`run_due`] its
//! runner of the change queue and the deadlines due. [`MemoryStore`] is the
//! layout over memory, and `superschematic-rules-dev` over SQLite, a file or
//! a database in memory that goes with its process.
//!
//! It promises a table's answers and not a table's speed (D117). A unique
//! field refuses a second holder, a relation refuses an id no row has and
//! its target's delete cascades, sets null or is restricted as the foreign
//! key sqlgen writes, and a list's filter, sort and page answer as the
//! ORMs' do ([`filter`]); every one is answered from every row of the type
//! the store reads, which nothing tunes for volume.
//!
//! ```
//! use serde_json::json;
//! use superschematic_rules::Caller;
//! use superschematic_rules_handler::{serve, Context, Declarations, Handler, MemoryStore, Request};
//!
//! let core = superschematic_rules_behaviors::core();
//! let declarations = Declarations::ported();
//! let handler = Handler::new(&core, &declarations);
//! let mut store = MemoryStore::new();
//! let context = Context { caller: Caller::new("alice", ["orders"]), now: 1_000, request: "r1".into(), id: 7 };
//! let order = json!({"name": "Order", "fields": [{"name": "title", "typeRef": {"name": "string"}, "required": true}],
//!     "behaviors": [{"name": "Workflow", "field": "status", "config": {"states": ["draft", "paid"], "transitions": [{"from": "draft", "to": "paid"}]}}]});
//! serve(&handler, &mut store, &Request::Define { document: order }, &context).unwrap();
//! serve(&handler, &mut store, &Request::Publish { type_name: "Order".into(), discard: false }, &context).unwrap();
//! let row = serve(&handler, &mut store, &Request::Create {
//!     type_name: "Order".into(),
//!     data: json!({"title": "Desk"}).as_object().cloned().unwrap(),
//!     params: Default::default(),
//! }, &context).unwrap();
//! assert_eq!(row["status"], "draft");
//! ```

pub mod access;
mod bridge;
pub mod declarations;
mod describe;
pub mod document;
pub mod filter;
mod handler;
mod memory;
mod publish;
mod serve;
pub mod store;
mod validate;
pub mod values;
mod view;

pub use access::Policy;
pub use bridge::{core_row, hearing, image, set_owned, Bridge, Changed, Ran};
pub use declarations::Declarations;
pub use describe::describe;
pub use handler::{
    Context, Done, Handler, HandlerStep, Request, DEFAULT_LIMIT, MAX_LIMIT, VETOED_RETRY_MS,
};
pub use memory::MemoryStore;
pub use serve::{run_due, serve, Ran as RanDue, MAX_ATTEMPTS, MAX_READ_ROUNDS, MAX_RUNS};
pub use store::{
    Armed, Draft, DueDeadline, HeldSubRow, QueuedChange, QueuedEvent, Store, StoreAnswer,
    StoreAnswers, StoreRead, StoreWrite, StoredRow, Unit, Version,
};
pub use validate::{own_fields, stored_fields, Mode};
