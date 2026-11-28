//! Instants as the work-queue behaviors' refusals write them: JavaScript's
//! `Date.prototype.toISOString`, which the TypeScript package's messages
//! read (`new Date(ms).toISOString()`), so a refusal reads the same on
//! either engine (D58).

/// An instant in epoch milliseconds as `toISOString` writes it:
/// `YYYY-MM-DDTHH:MM:SS.mmmZ`, a year outside 0 to 9999 as a sign and six
/// digits ([`superschematic_engine::civil::iso`]).
pub(crate) use superschematic_engine::civil::iso;
