//! The class of a server's error, by its SQLSTATE.

use tokio_postgres::error::DbError;

/// What a server's error means to the caller.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Class {
    /// Another transaction held what this one needed, or the server
    /// cancelled the statement: nothing of the transaction holds, and the
    /// same work may succeed when tried again. `40001`
    /// serialization_failure, `40P01` deadlock_detected, `55P03`
    /// lock_not_available (a `lock_timeout`, a `NOWAIT`), `57014`
    /// query_canceled (a `statement_timeout`, most often a wait for a lock
    /// too, or an administrator's cancel).
    Busy,
    /// The data refused the write: class `23`, integrity constraint
    /// violation (`23505` unique, `23503` foreign key, `23514` check,
    /// `23502` not null, `23P01` exclusion, `23001` restrict, `23000`).
    /// Trying again writes the same refusal.
    Conflict,
    /// The server ended the session, and its transaction with it: class
    /// `08` connection_exception, `57P01` admin_shutdown
    /// (`pg_terminate_backend`, a smart or fast shutdown), `57P02`
    /// crash_shutdown, `57P03` cannot_connect_now (starting or in
    /// recovery), `57P05` idle_session_timeout, `25P03`
    /// idle_in_transaction_session_timeout.
    Connection,
    /// The session's role holds no privilege for the statement: `42501`
    /// insufficient_privilege.
    Privilege,
    /// Anything else: a statement the server refused (a syntax error, a
    /// missing table, a value of the wrong type), which trying again
    /// refuses the same way.
    Other,
}

/// The class of a server's error.
pub fn classify(error: &DbError) -> Class {
    class_of(error.code().code())
}

/// The class of a SQLSTATE.
pub fn class_of(sqlstate: &str) -> Class {
    match sqlstate {
        "40001" | "40P01" | "55P03" | "57014" => Class::Busy,
        "57P01" | "57P02" | "57P03" | "57P05" | "25P03" => Class::Connection,
        "42501" => Class::Privilege,
        code if code.starts_with("08") => Class::Connection,
        code if code.starts_with("23") => Class::Conflict,
        _ => Class::Other,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn each_sqlstate_has_the_class_the_table_gives_it() {
        let cases = [
            ("40001", Class::Busy),
            ("40P01", Class::Busy),
            ("55P03", Class::Busy),
            ("57014", Class::Busy),
            ("23000", Class::Conflict),
            ("23001", Class::Conflict),
            ("23502", Class::Conflict),
            ("23503", Class::Conflict),
            ("23505", Class::Conflict),
            ("23514", Class::Conflict),
            ("23P01", Class::Conflict),
            ("08000", Class::Connection),
            ("08003", Class::Connection),
            ("08006", Class::Connection),
            ("08P01", Class::Connection),
            ("57P01", Class::Connection),
            ("57P02", Class::Connection),
            ("57P03", Class::Connection),
            ("57P05", Class::Connection),
            ("25P03", Class::Connection),
            ("42501", Class::Privilege),
            // Neighbours of the codes above that are none of them.
            ("40000", Class::Other),
            ("40002", Class::Other),
            ("40003", Class::Other),
            ("55000", Class::Other),
            ("55006", Class::Other),
            ("57000", Class::Other),
            ("57P04", Class::Other),
            ("25001", Class::Other),
            ("25P02", Class::Other),
            ("42000", Class::Other),
            ("42P01", Class::Other),
            ("42601", Class::Other),
            ("22P02", Class::Other),
            ("0A000", Class::Other),
            ("XX000", Class::Other),
            ("", Class::Other),
        ];
        for (code, class) in cases {
            assert_eq!(class_of(code), class, "{code}");
        }
    }
}
