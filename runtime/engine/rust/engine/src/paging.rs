//! Page sizes, the port of `src/paging.ts`: a list or an event read takes
//! 50 by default and at most 500.

use crate::errors::EngineError;

/// The page size of a list or an event read that names none.
pub const DEFAULT_PAGE_SIZE: i64 = 50;

/// The largest page a list or an event read returns.
pub const MAX_PAGE_SIZE: i64 = 500;

/// A requested page size, checked, with the default applied.
pub(crate) fn page_size(limit: Option<i64>) -> Result<usize, EngineError> {
    match limit {
        None => Ok(DEFAULT_PAGE_SIZE as usize),
        Some(limit) if (1..=MAX_PAGE_SIZE).contains(&limit) => Ok(limit as usize),
        Some(limit) => Err(page_size_refusal(&limit.to_string())),
    }
}

/// The refusal of a page size, as the request wrote it (`1.5`, `501`).
pub fn page_size_refusal(given: &str) -> EngineError {
    EngineError::invalid_argument(format!(
        "a page size is an integer from 1 to {MAX_PAGE_SIZE}, got {given}"
    ))
}
