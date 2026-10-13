//! The event log as server-sent events (`src/http/stream.ts`): the next
//! commit writes it. Until then a stream request answers 406.

use super::problems::Problem;
use super::{ApiOptions, Request, Response};
use crate::events::ReadEvents;
use crate::principal::Principal;
use crate::Engine;

/// Events read per page unless configured.
pub const DEFAULT_STREAM_PAGE_SIZE: i64 = 100;
/// Milliseconds between heartbeats unless configured.
pub const DEFAULT_HEARTBEAT_MS: u64 = 5_000;

/// The event stream's page size and heartbeat.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct StreamOptions {
    pub page_size: Option<i64>,
    pub heartbeat_ms: Option<u64>,
}

/// A stream of server-sent events.
#[derive(Debug)]
pub struct EventStream;

impl EventStream {
    /// The stream's next bytes.
    pub fn next(&mut self, _timeout: std::time::Duration) -> Pull {
        Pull::End
    }

    /// Ends the stream.
    pub fn close(&mut self) {}
}

/// What a pull of a stream gives.
#[derive(Debug, PartialEq, Eq)]
pub enum Pull {
    Data(Vec<u8>),
    Pending,
    End,
}

pub(crate) fn open(
    _engine: &Engine,
    _options: &ApiOptions,
    _request: &Request,
    _principal: &Principal,
    _read: &ReadEvents<'_>,
) -> Result<Response, Problem> {
    Err(Problem::new(
        406,
        "The event stream is not served yet",
        "not_acceptable",
    ))
}
